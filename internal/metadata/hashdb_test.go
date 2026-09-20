package metadata

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHashDBLookupAgainstRealDatabase runs against a real OpenVGDB copy when
// one is present, and skips otherwise so CI without the 42 MB download stays
// green.
//
// The case that matters is the casing: OpenVGDB stores hashes uppercase and
// SQLite compares text case-sensitively, so a lowercase hash matches nothing.
// That failure is indistinguishable from an unknown game, which is exactly why
// it needs pinning rather than trusting.
func TestHashDBLookupAgainstRealDatabase(t *testing.T) {
	path := os.Getenv("OPENVGDB_PATH")
	if path == "" {
		t.Skip("set OPENVGDB_PATH to run against a real OpenVGDB copy")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no database at %s", path)
	}

	dir := t.TempDir()
	dest := filepath.Join(dir, "openvgdb.sqlite")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewHashDB(dir)
	defer h.Close()
	if !h.Available() {
		t.Fatal("database did not open")
	}

	// A known dump, asserted in both cases.
	const upper = "E6C12AC9C562D772B8EB4F3FA439A84D" // NHL 99 (N64)
	match, ok := h.LookupMD5(upper)
	if !ok {
		t.Fatalf("uppercase hash %s did not match", upper)
	}
	if match.Title == "" {
		t.Error("matched but returned an empty title")
	}
	t.Logf("matched %q (%s, %s)", match.Title, match.System, match.Region)

	lower, okLower := h.LookupMD5("e6c12ac9c562d772b8eb4f3fa439a84d")
	if !okLower || lower.Title != match.Title {
		t.Error("a lowercase hash must match too; LookupMD5 is responsible for the casing")
	}

	if _, ok := h.LookupMD5("00000000000000000000000000000000"); ok {
		t.Error("an unknown hash matched something")
	}
}

// TestHashDBUnavailableIsSafe pins the fallback: with no database, lookups
// answer "not found" rather than panicking, so the scanner degrades to title
// matching instead of failing.
func TestHashDBUnavailableIsSafe(t *testing.T) {
	h := &HashDB{}
	if h.Available() {
		t.Error("an unopened database reports itself available")
	}
	if _, ok := h.LookupMD5("E6C12AC9C562D772B8EB4F3FA439A84D"); ok {
		t.Error("an unopened database returned a match")
	}
	h.Close() // must not panic
}
