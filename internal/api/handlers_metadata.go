package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/metadata"
)

// handleMetadataSearch handles GET /api/metadata/search?q=game_name&platform=slug
//
// The response keeps its original shape - result is still the legacy
// RAWG-shaped record the UI already reads - and adds canonical/sources
// alongside it, so per-field provider attribution is available without
// breaking existing consumers.
func (s *Server) handleMetadataSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeError(w, 400, "Query parameter 'q' is required")
		return
	}

	platformSlug := r.URL.Query().Get("platform")

	game, err := s.meta.Search(r.Context(), query, platformSlug)
	if err != nil {
		writeError(w, 502, "Metadata search failed: "+err.Error())
		return
	}
	if game == nil {
		writeJSON(w, 200, map[string]interface{}{
			"found":     false,
			"result":    nil,
			"providers": s.meta.Providers(),
		})
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"found":     true,
		"result":    game.ToGameMetadata(),
		"canonical": game,
		"sources":   game.Sources,
		"providers": s.meta.Providers(),
	})
}

// handleMetadataGet handles GET /api/metadata/{rawg_id}
//
// The route is RAWG-ID-scoped by definition (the ID comes from a prior
// search's result.id, which is a RAWG ID), so this still resolves through the
// RAWG provider specifically rather than the full priority chain.
func (s *Server) handleMetadataGet(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.HasRAWG() {
		writeError(w, 503, "RAWG API key not configured (set RAWG_API_KEY env var)")
		return
	}

	rawgID, err := strconv.Atoi(chi.URLParam(r, "rawg_id"))
	if err != nil || rawgID <= 0 {
		writeError(w, 400, "Invalid RAWG ID")
		return
	}

	game, err := s.meta.GetByID(r.Context(), "rawg", strconv.Itoa(rawgID))
	if err != nil {
		writeError(w, 502, "RAWG fetch failed: "+err.Error())
		return
	}
	if game == nil {
		writeError(w, 404, "Game not found")
		return
	}

	writeJSON(w, 200, game.ToGameMetadata())
}

// handleEnrichLibraryItem handles POST /api/library/{id}/enrich
// Resolves metadata for a library item across every configured provider and
// saves the merged record.
func (s *Server) handleEnrichLibraryItem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 400, "Invalid library item ID")
		return
	}

	// Get the library item.
	item, err := s.mgr.Jobs().GetLibraryItem(id)
	if err != nil {
		writeError(w, 404, "Library item not found")
		return
	}

	game, err := s.meta.Search(r.Context(), item.Title, item.PlatformSlug)
	if err != nil {
		writeError(w, 502, "Metadata search failed: "+err.Error())
		return
	}
	if game == nil {
		writeError(w, 404, "No metadata found for: "+item.Title)
		return
	}

	// Persist the legacy flat shape the UI already reads, with attribution
	// added as extra keys rather than restructuring the stored object.
	stored, err := legacyMetadataWithAttribution(game)
	if err != nil {
		writeError(w, 500, "Failed to serialize metadata")
		return
	}

	if err := s.mgr.Jobs().UpdateLibraryItemMetadata(id, string(stored)); err != nil {
		writeError(w, 500, "Failed to save metadata: "+err.Error())
		return
	}

	providers := strings.Join(sortedSourceProviders(game.Sources), ", ")
	detail := "Enriched with metadata (" + game.Title + ")"
	if providers != "" {
		detail += " from " + providers
	}
	s.mgr.Jobs().LogActivity("metadata_enriched", item.Title, detail, "", nil)

	writeJSON(w, 200, map[string]interface{}{
		"success":   true,
		"metadata":  game.ToGameMetadata(),
		"canonical": game,
		"sources":   game.Sources,
	})
}

// legacyMetadataWithAttribution marshals the legacy flat GameMetadata object
// and splices in sources/provider_ids, so stored metadata stays readable by
// code written against the old shape while carrying the new attribution.
func legacyMetadataWithAttribution(game *metadata.Game) ([]byte, error) {
	flat, err := json.Marshal(game.ToGameMetadata())
	if err != nil {
		return nil, err
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(flat, &obj); err != nil {
		return nil, err
	}
	if len(game.Sources) > 0 {
		obj["sources"] = game.Sources
	}
	if len(game.ProviderIDs) > 0 {
		obj["provider_ids"] = game.ProviderIDs
	}
	return json.Marshal(obj)
}

// sortedSourceProviders returns the distinct provider names in an attribution
// map, sorted so the activity-log line is stable across runs.
func sortedSourceProviders(sources map[string]string) []string {
	seen := make(map[string]bool, len(sources))
	var names []string
	for _, p := range sources {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		names = append(names, p)
	}
	sort.Strings(names)
	return names
}

// ── Connection Tests for new clients ─────────────────────────────────────────

func (s *Server) handleTestTransmission(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.HasTransmission() {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": "Not configured"})
		return
	}
	tc := s.mgr.Transmission()
	if tc == nil {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": "Client not initialized"})
		return
	}
	result := tc.Diagnose()
	writeJSON(w, 200, result)
}

func (s *Server) handleTestDeluge(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.HasDeluge() {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": "Not configured"})
		return
	}
	dc := s.mgr.Deluge()
	if dc == nil {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": "Client not initialized"})
		return
	}
	result := dc.Diagnose()
	writeJSON(w, 200, result)
}

// RegisterMetadataRoutes registers metadata and new client test routes on the given chi router.
// Call this from NewRouter after the router is created.
//
// Routes to add to the router in api.go:
//
//	r.Get("/api/metadata/search", s.handleMetadataSearch)
//	r.Get("/api/metadata/{rawg_id}", s.handleMetadataGet)
//	r.Post("/api/library/{id}/enrich", s.handleEnrichLibraryItem)
//	r.Post("/api/test/transmission", s.handleTestTransmission)
//	r.Post("/api/test/deluge", s.handleTestDeluge)
func (s *Server) RegisterMetadataRoutes(r chi.Router) {
	r.Get("/api/metadata/search", s.handleMetadataSearch)
	r.Get("/api/metadata/{rawg_id}", s.handleMetadataGet)
	r.Post("/api/library/{id}/enrich", s.handleEnrichLibraryItem)
	r.Post("/api/test/transmission", requireAdmin(s.handleTestTransmission))
	r.Post("/api/test/deluge", requireAdmin(s.handleTestDeluge))
}
