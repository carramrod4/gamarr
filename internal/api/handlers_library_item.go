package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/db"
)

// handleLibraryItem handles GET /api/library/{id} - detail for one library item.
//
// The list endpoint returns metadata as an opaque JSON string, which every
// client then has to decode a second time. This route decodes it once, here,
// and returns it as a real object alongside the per-field provider attribution
// the metadata resolver records - so a detail screen can show both the merged
// record and where each field came from without re-parsing anything.
//
// Tags are included because a detail view invariably wants them, and fetching
// them separately would mean a second round trip for every item opened.
func (s *Server) handleLibraryItem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid item ID")
		return
	}

	item, err := s.mgr.Jobs().GetLibraryItem(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Library item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	tags := s.mgr.Jobs().GetItemTags(id)
	if tags == nil {
		tags = []db.Tag{}
	}

	resp := map[string]interface{}{
		"success": true,
		"item":    item,
		"tags":    tags,
	}

	// metadata is a JSON blob written by the enrichment path. A row that was
	// never enriched holds "{}" or "", and a row written by an older build
	// could hold anything - so a decode failure is reported as "no metadata"
	// rather than failing the whole request.
	if meta, sources, ok := decodeItemMetadata(item.Metadata); ok {
		resp["metadata"] = meta
		if len(sources) > 0 {
			resp["sources"] = sources
			resp["providers"] = sortedSourceProviders(sources)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// decodeItemMetadata decodes a stored metadata blob and lifts out the per-field
// source attribution spliced in by the metadata resolver.
func decodeItemMetadata(blob string) (map[string]interface{}, map[string]string, bool) {
	if blob == "" || blob == "{}" {
		return nil, nil, false
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(blob), &obj); err != nil || len(obj) == 0 {
		return nil, nil, false
	}
	sources := make(map[string]string)
	if raw, ok := obj["sources"].(map[string]interface{}); ok {
		for field, provider := range raw {
			if name, ok := provider.(string); ok && name != "" {
				sources[field] = name
			}
		}
	}
	return obj, sources, true
}

// handleSetLibraryItemMonitored handles PUT /api/library/{id}/monitored.
//
// PUT rather than POST, and an explicit boolean in the body rather than a
// toggle, so the request is idempotent: a client that retries after a timeout
// cannot flip the flag back by accident the way a bare /toggle would.
func (s *Server) handleSetLibraryItemMonitored(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid item ID")
		return
	}

	// Pointer so a body that omits the field is rejected instead of silently
	// meaning "unmonitor".
	var req struct {
		Monitored *bool `json:"monitored"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Monitored == nil {
		writeError(w, http.StatusBadRequest, "Field \"monitored\" (true/false) is required")
		return
	}

	if err := s.mgr.Jobs().SetLibraryItemMonitored(id, *req.Monitored); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Library item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"id":        id,
		"monitored": *req.Monitored,
	})
}
