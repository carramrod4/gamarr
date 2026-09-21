package organize

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Laying the ROM library out by canonical name is what makes the filesystem a
// usable contract between Gamarr and RomM: Gamarr acquires and names, RomM
// indexes and serves, and neither needs the other's database. It is the same
// arrangement Radarr has with Plex.
//
// Two rules keep it safe, and both matter more than the feature itself:
//
//   - Only files a content hash identified are moved. A name derived from a
//     filename is a guess, and renaming a file to a guess destroys the only
//     evidence of what it actually was.
//   - Nothing is ever overwritten, and nothing moves outside the ROM root.
//
// Every caller previews before applying. A rename is not undoable in any
// meaningful way once a few thousand have run.

// Move is one planned rename.
type Move struct {
	ItemID int64  `json:"item_id"`
	From   string `json:"from"`
	To     string `json:"to"`
	// Reason is why a move is not being made, when To is empty.
	Reason string `json:"reason,omitempty"`
}

// Plan is the result of a preview.
type Plan struct {
	Moves   []Move `json:"moves"`
	Skipped []Move `json:"skipped"`
	// Conflicts are moves whose destination already holds a different file.
	Conflicts []Move `json:"conflicts"`
}

// filesystemUnsafe are characters a title may legitimately contain that do not
// belong in a filename. `:` is the common one - "Star Wars: Shadows of the
// Empire" - and the convention every *arr uses is to spell it " - ".
var filesystemUnsafe = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// CanonicalFileName turns a release title into a filename, preserving the
// original extension.
//
// Deliberately conservative: it does not strip parenthesised region tags or
// anything else, because the canonical title came from a hash lookup and is
// already exactly what the release is called. Cleaning it further would only
// introduce disagreement with the database it came from.
func CanonicalFileName(canonicalTitle, originalPath string) string {
	ext := filepath.Ext(originalPath)
	name := strings.ReplaceAll(canonicalTitle, ":", " -")
	name = filesystemUnsafe.ReplaceAllString(name, "")
	name = multiSpaceRe.ReplaceAllString(name, " ")
	name = strings.TrimSpace(strings.Trim(name, ". "))
	if name == "" {
		return ""
	}
	// Leave room for the extension inside the common 255-byte limit.
	if len(name) > 200 {
		name = strings.TrimSpace(name[:200])
	}
	return name + ext
}

// LibraryItemView is the subset of a library row the planner needs, so this
// package does not depend on the database one.
type LibraryItemView struct {
	ID             int64
	FilePath       string
	CanonicalTitle string
	PlatformSlug   string
	IsPC           bool
}

// PlanLayout works out what would move, without touching anything.
//
// romsRoot bounds every destination: a planned path outside it is dropped
// rather than clamped, because a path that escaped the root is a bug and
// carrying on with a "corrected" one would hide it.
func PlanLayout(items []LibraryItemView, romsRoot string) Plan {
	var plan Plan
	root, err := filepath.Abs(romsRoot)
	if err != nil {
		return plan
	}

	// Destinations claimed within this plan, so two sources cannot be planned
	// onto one path - which would silently lose a file when applied.
	claimed := make(map[string]int64, len(items))

	for _, item := range items {
		switch {
		case item.IsPC:
			plan.Skipped = append(plan.Skipped, Move{ItemID: item.ID, From: item.FilePath,
				Reason: "PC titles are folders, not single files"})
			continue
		case item.CanonicalTitle == "":
			plan.Skipped = append(plan.Skipped, Move{ItemID: item.ID, From: item.FilePath,
				Reason: "not identified by content hash"})
			continue
		case item.PlatformSlug == "":
			plan.Skipped = append(plan.Skipped, Move{ItemID: item.ID, From: item.FilePath,
				Reason: "no platform"})
			continue
		}

		name := CanonicalFileName(item.CanonicalTitle, item.FilePath)
		if name == "" {
			plan.Skipped = append(plan.Skipped, Move{ItemID: item.ID, From: item.FilePath,
				Reason: "canonical title is empty once made filename-safe"})
			continue
		}

		dest := filepath.Join(root, item.PlatformSlug, name)
		if !withinRoot(dest, root) {
			plan.Skipped = append(plan.Skipped, Move{ItemID: item.ID, From: item.FilePath,
				Reason: "destination would fall outside the ROM root"})
			continue
		}
		if dest == item.FilePath {
			continue // already where it belongs
		}

		if other, taken := claimed[dest]; taken {
			plan.Conflicts = append(plan.Conflicts, Move{ItemID: item.ID, From: item.FilePath, To: dest,
				Reason: fmt.Sprintf("item %d is already planned for this path", other)})
			continue
		}
		if pathExists(dest) {
			plan.Conflicts = append(plan.Conflicts, Move{ItemID: item.ID, From: item.FilePath, To: dest,
				Reason: "a file already exists there"})
			continue
		}

		claimed[dest] = item.ID
		plan.Moves = append(plan.Moves, Move{ItemID: item.ID, From: item.FilePath, To: dest})
	}
	return plan
}

// withinRoot reports whether dest is inside root, defending against a title
// that resolves to a traversal.
func withinRoot(dest, root string) bool {
	rel, err := filepath.Rel(root, dest)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ApplyMove performs one planned rename and reports the path it ended at.
//
// Re-checks the destination immediately before moving rather than trusting the
// plan: a preview and its application are separated by however long a person
// spent reading it, and a scan or a download may have created the file in
// between.
func ApplyMove(move Move) (string, error) {
	if move.To == "" {
		return "", fmt.Errorf("no destination")
	}
	if _, err := os.Stat(move.From); err != nil {
		return "", fmt.Errorf("source is gone: %w", err)
	}
	if pathExists(move.To) {
		return "", fmt.Errorf("destination now exists")
	}
	if err := os.MkdirAll(filepath.Dir(move.To), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(move.From, move.To); err != nil {
		return "", err
	}
	return move.To, nil
}
