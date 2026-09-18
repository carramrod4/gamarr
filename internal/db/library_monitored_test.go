package db

import (
	"database/sql"
	"errors"
	"testing"
)

// newMonitoredTestItem builds a minimal valid library item.
func newMonitoredTestItem(title string) *LibraryItem {
	return &LibraryItem{
		Title:        title,
		Platform:     "SNES",
		PlatformSlug: "snes",
		FilePath:     "/data/roms/snes/" + title + ".sfc",
		Source:       "scan",
		SourceType:   "manual",
		SourceID:     "scan:" + title,
		Metadata:     "{}",
	}
}

// A newly added item is monitored, matching Sonarr/Radarr. This is load-bearing:
// AddLibraryItem omits the column so the schema default applies, which means a
// zero-valued Monitored on the passed struct must NOT reach the database.
func TestAddLibraryItem_DefaultsToMonitored(t *testing.T) {
	store := newTestStore(t)

	id, err := store.AddLibraryItem(newMonitoredTestItem("Chrono Trigger"))
	if err != nil {
		t.Fatalf("AddLibraryItem: %v", err)
	}

	item, err := store.GetLibraryItem(id)
	if err != nil {
		t.Fatalf("GetLibraryItem: %v", err)
	}
	if !item.Monitored {
		t.Error("a newly added item should be monitored")
	}
}

func TestSetLibraryItemMonitored_RoundTrip(t *testing.T) {
	store := newTestStore(t)

	id, err := store.AddLibraryItem(newMonitoredTestItem("Super Metroid"))
	if err != nil {
		t.Fatalf("AddLibraryItem: %v", err)
	}

	for _, want := range []bool{false, true, false} {
		if err := store.SetLibraryItemMonitored(id, want); err != nil {
			t.Fatalf("SetLibraryItemMonitored(%v): %v", want, err)
		}
		item, err := store.GetLibraryItem(id)
		if err != nil {
			t.Fatalf("GetLibraryItem: %v", err)
		}
		if item.Monitored != want {
			t.Errorf("Monitored = %v, want %v", item.Monitored, want)
		}
	}
}

// A write to an id that does not exist must be reported, not silently accepted -
// the API layer turns this into a 404 rather than a false confirmation.
func TestSetLibraryItemMonitored_UnknownIDIsAnError(t *testing.T) {
	store := newTestStore(t)

	err := store.SetLibraryItemMonitored(99999, true)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err = %v, want sql.ErrNoRows", err)
	}
}

// Every read path shares one column list and one scan helper. This pins that
// they actually agree: a column added to libraryColumns but forgotten in one
// query would surface here as a zero-valued Monitored on that path only.
func TestMonitoredIsReadByEveryLibraryPath(t *testing.T) {
	store := newTestStore(t)

	id, err := store.AddLibraryItem(newMonitoredTestItem("Earthbound"))
	if err != nil {
		t.Fatalf("AddLibraryItem: %v", err)
	}
	if err := store.SetLibraryItemMonitored(id, false); err != nil {
		t.Fatalf("SetLibraryItemMonitored: %v", err)
	}

	t.Run("GetLibraryItem", func(t *testing.T) {
		item, err := store.GetLibraryItem(id)
		if err != nil {
			t.Fatalf("GetLibraryItem: %v", err)
		}
		if item.Monitored {
			t.Error("want unmonitored")
		}
	})

	t.Run("GetLibraryPage", func(t *testing.T) {
		page := store.GetLibraryPage(1, 10, "", "")
		if len(page.Items) != 1 {
			t.Fatalf("got %d items, want 1", len(page.Items))
		}
		if page.Items[0].Monitored {
			t.Error("want unmonitored")
		}
	})

	t.Run("FindLibraryByTitle", func(t *testing.T) {
		item := store.FindLibraryByTitle("Earthbound", "snes")
		if item == nil {
			t.Fatal("FindLibraryByTitle returned nil")
		}
		if item.Monitored {
			t.Error("want unmonitored")
		}
	})

	t.Run("GetAllLibraryTitles", func(t *testing.T) {
		all := store.GetAllLibraryTitles()
		item, ok := all["earthbound|snes"]
		if !ok {
			t.Fatalf("key not found; got keys %v", keysOf(all))
		}
		if item.Monitored {
			t.Error("want unmonitored")
		}
	})

	t.Run("RecentLibraryItems", func(t *testing.T) {
		items := store.RecentLibraryItems(10)
		if len(items) != 1 {
			t.Fatalf("got %d items, want 1", len(items))
		}
		if items[0].Monitored {
			t.Error("want unmonitored")
		}
	})
}

func keysOf(m map[string]*LibraryItem) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
