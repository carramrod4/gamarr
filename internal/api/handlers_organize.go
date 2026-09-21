package api

import (
	"encoding/json"
	"net/http"

	"gamarr/internal/organize"
)

// Laying the ROM library out by canonical name is what makes the filesystem a
// working contract between Gamarr and RomM: Gamarr acquires and names, RomM
// indexes and serves, neither reads the other's database.
//
// Preview and apply are separate endpoints on purpose. A rename is not
// meaningfully undoable once a few thousand have run, so the destructive half
// is something a caller has to ask for by name after seeing exactly what it
// would do.

type organizeResponse struct {
	organize.Plan
	// Counts save every caller from recomputing them, and make a preview
	// readable without walking the arrays.
	MoveCount     int `json:"move_count"`
	SkipCount     int `json:"skip_count"`
	ConflictCount int `json:"conflict_count"`
}

func (s *Server) buildOrganizePlan() organize.Plan {
	items := s.mgr.Jobs().AllLibraryItems()
	views := make([]organize.LibraryItemView, 0, len(items))
	for _, item := range items {
		views = append(views, organize.LibraryItemView{
			ID:             item.ID,
			FilePath:       item.FilePath,
			CanonicalTitle: item.CanonicalTitle,
			PlatformSlug:   item.PlatformSlug,
			IsPC:           item.IsPC,
		})
	}
	return organize.PlanLayout(views, s.mgr.Config().GamesRomsPath)
}

// handleOrganizePreview reports what would move. Touches nothing.
func (s *Server) handleOrganizePreview(w http.ResponseWriter, r *http.Request) {
	plan := s.buildOrganizePlan()
	writeJSON(w, http.StatusOK, organizeResponse{
		Plan:          plan,
		MoveCount:     len(plan.Moves),
		SkipCount:     len(plan.Skipped),
		ConflictCount: len(plan.Conflicts),
	})
}

// handleOrganizeApply performs the plan.
//
// The plan is rebuilt here rather than accepted from the caller. Taking a
// client-supplied list of source and destination paths would make this an
// arbitrary file-move endpoint, and the whole point is that every destination
// is derived from a hash-verified title under the ROM root.
func (s *Server) handleOrganizeApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		// Confirm has to be sent explicitly: a bare POST from a stray retry or
		// a link preview must not rename a library.
		Confirm bool `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !body.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": `this renames files; send {"confirm": true} to proceed`,
		})
		return
	}

	plan := s.buildOrganizePlan()
	var applied, failed int
	var errors []organize.Move

	for _, move := range plan.Moves {
		newPath, err := organize.ApplyMove(move)
		if err != nil {
			failed++
			move.Reason = err.Error()
			errors = append(errors, move)
			continue
		}
		// The database follows the file. If this fails the next scan repairs
		// it, since scanning is keyed on the path it finds on disk.
		s.mgr.Jobs().UpdateLibraryItemPath(move.ItemID, newPath)
		applied++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"applied":   applied,
		"failed":    failed,
		"errors":    errors,
		"conflicts": plan.Conflicts,
	})
}
