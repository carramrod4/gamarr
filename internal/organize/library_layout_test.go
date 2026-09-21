package organize

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalFileNameKeepsTheReleaseName(t *testing.T) {
	cases := map[string]string{
		"Star Wars: Shadows of the Empire": "Star Wars - Shadows of the Empire.n64",
		"Sonic the Hedgehog 2":             "Sonic the Hedgehog 2.n64",
		"Ratchet & Clank":                  "Ratchet & Clank.n64",
		`Who/What\Where`:                   "WhoWhatWhere.n64",
	}
	for title, want := range cases {
		if got := CanonicalFileName(title, "/roms/x/whatever.n64"); got != want {
			t.Errorf("CanonicalFileName(%q) = %q, want %q", title, got, want)
		}
	}
}

// TestPlanOnlyMovesIdentifiedFiles is the rule that matters most: a name
// derived from a filename is a guess, and renaming a file to a guess destroys
// the only evidence of what it actually was.
func TestPlanOnlyMovesIdentifiedFiles(t *testing.T) {
	root := t.TempDir()
	items := []LibraryItemView{
		{ID: 1, FilePath: filepath.Join(root, "nes", "junk name.nes"),
			CanonicalTitle: "Metroid", PlatformSlug: "nes"},
		{ID: 2, FilePath: filepath.Join(root, "nes", "unknown.nes"),
			CanonicalTitle: "", PlatformSlug: "nes"},
		{ID: 3, FilePath: filepath.Join(root, "pc", "Some Game"),
			CanonicalTitle: "Some Game", PlatformSlug: "pc", IsPC: true},
	}
	plan := PlanLayout(items, root)

	if len(plan.Moves) != 1 || plan.Moves[0].ItemID != 1 {
		t.Fatalf("expected only the identified item to move, got %+v", plan.Moves)
	}
	if filepath.Base(plan.Moves[0].To) != "Metroid.nes" {
		t.Errorf("destination is %q", plan.Moves[0].To)
	}
	if len(plan.Skipped) != 2 {
		t.Errorf("expected the unidentified and PC items skipped, got %d", len(plan.Skipped))
	}
}

// TestPlanNeverOverwrites covers both collision kinds: a file already on disk,
// and two sources planned onto one destination. The second is the dangerous
// one - it looks fine in a preview and silently loses a file when applied.
func TestPlanNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nes"), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(root, "nes", "Metroid.nes")
	if err := os.WriteFile(existing, []byte("already here"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan := PlanLayout([]LibraryItemView{
		{ID: 1, FilePath: filepath.Join(root, "nes", "a.nes"), CanonicalTitle: "Metroid", PlatformSlug: "nes"},
	}, root)
	if len(plan.Moves) != 0 {
		t.Error("planned a move onto an existing file")
	}
	if len(plan.Conflicts) != 1 {
		t.Fatalf("expected one conflict, got %d", len(plan.Conflicts))
	}

	// Two different dumps of the same release: one moves, the other is a
	// conflict rather than an overwrite.
	plan = PlanLayout([]LibraryItemView{
		{ID: 1, FilePath: filepath.Join(root, "nes", "a.nes"), CanonicalTitle: "Kid Icarus", PlatformSlug: "nes"},
		{ID: 2, FilePath: filepath.Join(root, "nes", "b.nes"), CanonicalTitle: "Kid Icarus", PlatformSlug: "nes"},
	}, root)
	if len(plan.Moves) != 1 {
		t.Errorf("expected exactly one of two identical destinations to move, got %d", len(plan.Moves))
	}
	if len(plan.Conflicts) != 1 {
		t.Errorf("expected the second to be a conflict, got %d", len(plan.Conflicts))
	}
}

// TestPlanNeverLeavesTheRoot covers a title that resolves upward.
//
// Two independent defences catch this, and the test asserts the outcome rather
// than which one fired: the filename sanitiser strips the separators and dots,
// so "../../etc/passwd" becomes a harmless "etcpasswd" inside the platform
// folder, and the root check would reject it even if the sanitiser changed.
// What matters is only that nothing lands outside the ROM tree.
func TestPlanNeverLeavesTheRoot(t *testing.T) {
	root := t.TempDir()
	plan := PlanLayout([]LibraryItemView{
		{ID: 1, FilePath: filepath.Join(root, "nes", "a.nes"),
			CanonicalTitle: "../../etc/passwd", PlatformSlug: "nes"},
		{ID: 2, FilePath: filepath.Join(root, "nes", "b.nes"),
			CanonicalTitle: "..", PlatformSlug: "nes"},
	}, root)
	for _, m := range plan.Moves {
		if !withinRoot(m.To, root) {
			t.Errorf("planned a move to %q, which escapes the root", m.To)
		}
		if filepath.Base(filepath.Dir(m.To)) != "nes" {
			t.Errorf("%q is not inside the platform folder", m.To)
		}
	}
}

// TestPlanLeavesCorrectlyNamedFilesAlone keeps a second run from churning.
func TestPlanLeavesCorrectlyNamedFilesAlone(t *testing.T) {
	root := t.TempDir()
	plan := PlanLayout([]LibraryItemView{
		{ID: 1, FilePath: filepath.Join(root, "nes", "Metroid.nes"),
			CanonicalTitle: "Metroid", PlatformSlug: "nes"},
	}, root)
	if len(plan.Moves) != 0 || len(plan.Conflicts) != 0 {
		t.Errorf("a correctly named file was not left alone: %+v %+v", plan.Moves, plan.Conflicts)
	}
}

// TestApplyMoveRechecksTheDestination: a preview and its application are
// separated by however long a person spent reading it.
func TestApplyMoveRechecksTheDestination(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nes"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "nes", "a.nes")
	dst := filepath.Join(root, "nes", "Metroid.nes")
	if err := os.WriteFile(src, []byte("rom"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Something created the destination after the plan was made.
	if err := os.WriteFile(dst, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyMove(Move{ItemID: 1, From: src, To: dst}); err == nil {
		t.Fatal("ApplyMove overwrote a file that appeared after planning")
	}
	if data, _ := os.ReadFile(dst); string(data) != "other" {
		t.Error("the destination was modified")
	}

	os.Remove(dst)
	if got, err := ApplyMove(Move{ItemID: 1, From: src, To: dst}); err != nil || got != dst {
		t.Fatalf("ApplyMove failed on a clear destination: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Error("the file did not arrive")
	}
}
