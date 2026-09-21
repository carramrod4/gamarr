package db

import "testing"

// TestBuildLibraryWhereFilters pins each identification filter's SQL, because
// the distinction between "not hashed" and "hashed and unrecognised" is the
// whole point of storing both columns - and getting it backwards would present
// every unreadable archive as a suspect dump.
func TestBuildLibraryWhereFilters(t *testing.T) {
	cases := map[LibraryFilter]string{
		LibraryFilterNeedsAttention: "rom_md5 <> '' AND canonical_title = ''",
		LibraryFilterIdentified:     "canonical_title <> ''",
		LibraryFilterUnhashed:       "rom_md5 = ''",
	}
	for filter, want := range cases {
		where, _ := buildLibraryWhere("", "", filter)
		if where != "WHERE "+want {
			t.Errorf("filter %q produced %q, want %q", filter, where, "WHERE "+want)
		}
	}

	if where, _ := buildLibraryWhere("", "", LibraryFilterAll); where != "" {
		t.Errorf("the empty filter narrowed the query: %q", where)
	}
	// An unknown value must not narrow anything either - the handler maps it to
	// the empty filter, and a stray condition here would hide the library.
	if where, _ := buildLibraryWhere("", "", LibraryFilter("nonsense")); where != "" {
		t.Errorf("an unknown filter narrowed the query: %q", where)
	}
}

// TestBuildLibraryWhereCombinesFilters checks the filter composes with the
// existing text and platform conditions rather than replacing them.
func TestBuildLibraryWhereCombinesFilters(t *testing.T) {
	where, args := buildLibraryWhere("mario", "snes", LibraryFilterNeedsAttention)
	if len(args) != 2 {
		t.Fatalf("got %d args, want 2 (query + platform)", len(args))
	}
	for _, fragment := range []string{"rom_md5 <> ''", "title LIKE ?", "platform_slug = ?"} {
		if !contains(where, fragment) {
			t.Errorf("%q missing from %q", fragment, where)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
