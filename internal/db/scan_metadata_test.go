package db

import "testing"

func addScanRow(t *testing.T, store *JobStore, title, path, metadata string) int64 {
	t.Helper()
	id, err := store.AddLibraryItem(&LibraryItem{
		Title:        title,
		Platform:     "SNES",
		PlatformSlug: "snes",
		FilePath:     path,
		Source:       "scan",
		SourceType:   "scan",
		SourceID:     "scan:" + path,
		Metadata:     metadata,
	})
	if err != nil {
		t.Fatalf("AddLibraryItem: %v", err)
	}
	return id
}

func TestScanMetadataByPath_OnlyReturnsRowsThatHaveMetadata(t *testing.T) {
	store := newTestStore(t)
	addScanRow(t, store, "Chrono Trigger", "/roms/snes/ct.sfc", `{"name":"Chrono Trigger"}`)
	addScanRow(t, store, "Unenriched", "/roms/snes/plain.sfc", "{}")
	addScanRow(t, store, "Empty", "/roms/snes/empty.sfc", "")

	got := store.ScanMetadataByPath()
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %v", len(got), got)
	}
	if got["/roms/snes/ct.sfc"] != `{"name":"Chrono Trigger"}` {
		t.Errorf("metadata = %q", got["/roms/snes/ct.sfc"])
	}
}

// A download-sourced row is not the scan's to restore, so it must be excluded.
func TestScanMetadataByPath_IgnoresNonScanRows(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.AddLibraryItem(&LibraryItem{
		Title: "Downloaded", FilePath: "/dl/game.iso", Source: "torrent",
		SourceType: "prowlarr", SourceID: "torrent:abc", Metadata: `{"name":"Downloaded"}`,
	}); err != nil {
		t.Fatalf("AddLibraryItem: %v", err)
	}

	if got := store.ScanMetadataByPath(); len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}
}

// The whole point: a clear-and-rescan cycle must not lose enrichment.
func TestRestoreScanMetadata_SurvivesAClearAndRescan(t *testing.T) {
	store := newTestStore(t)
	const path = "/roms/snes/ct.sfc"
	addScanRow(t, store, "EARTH BOUND.smc", path, `{"name":"EarthBound","sources":{"title":"igdb"}}`)

	saved := store.ScanMetadataByPath()
	store.ClearScanEntries()

	if store.GetLibraryPage(1, 10, "", "").Total != 0 {
		t.Fatal("expected the clear to empty the library")
	}

	// The rescan re-inserts with an empty blob - and, after the filename
	// parser fix, under a different (cleaner) title. Restoring by path rather
	// than by title or id is what makes that work.
	addScanRow(t, store, "EarthBound", path, "{}")

	if n := store.RestoreScanMetadata(saved); n != 1 {
		t.Fatalf("restored %d rows, want 1", n)
	}

	item := store.FindLibraryByTitle("EarthBound", "snes")
	if item == nil {
		t.Fatal("row not found after restore")
	}
	if item.Metadata != `{"name":"EarthBound","sources":{"title":"igdb"}}` {
		t.Errorf("metadata = %q, want the saved blob", item.Metadata)
	}
}

// A file that disappeared between scans simply matches nothing.
func TestRestoreScanMetadata_IgnoresPathsThatNoLongerExist(t *testing.T) {
	store := newTestStore(t)
	addScanRow(t, store, "Still Here", "/roms/snes/here.sfc", "{}")

	n := store.RestoreScanMetadata(map[string]string{
		"/roms/snes/here.sfc": `{"name":"Still Here"}`,
		"/roms/snes/gone.sfc": `{"name":"Deleted"}`,
	})
	if n != 1 {
		t.Errorf("restored %d, want 1", n)
	}
}

// Restoring must never overwrite metadata the rescan already produced.
func TestRestoreScanMetadata_DoesNotClobberFresherMetadata(t *testing.T) {
	store := newTestStore(t)
	const path = "/roms/snes/ct.sfc"
	addScanRow(t, store, "Chrono Trigger", path, `{"name":"fresh"}`)

	if n := store.RestoreScanMetadata(map[string]string{path: `{"name":"stale"}`}); n != 0 {
		t.Errorf("restored %d rows, want 0", n)
	}
	item := store.FindLibraryByTitle("Chrono Trigger", "snes")
	if item.Metadata != `{"name":"fresh"}` {
		t.Errorf("metadata = %q, want the fresher blob untouched", item.Metadata)
	}
}

func TestRestoreScanMetadata_EmptyInput(t *testing.T) {
	store := newTestStore(t)
	if n := store.RestoreScanMetadata(nil); n != 0 {
		t.Errorf("restored %d, want 0", n)
	}
}
