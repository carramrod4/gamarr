package db

import (
	"testing"
	"time"
)

func TestMetadataCache_PutAndGet(t *testing.T) {
	store := newTestStore(t)

	if err := store.PutMetadataCache("igdb|search|chrono trigger|snes", []byte(`{"title":"Chrono Trigger"}`)); err != nil {
		t.Fatalf("PutMetadataCache: %v", err)
	}

	payload, ok := store.GetMetadataCache("igdb|search|chrono trigger|snes", time.Hour)
	if !ok {
		t.Fatal("expected a cache hit")
	}
	if string(payload) != `{"title":"Chrono Trigger"}` {
		t.Errorf("payload = %q, want the stored JSON", payload)
	}
}

func TestMetadataCache_MissOnUnknownKey(t *testing.T) {
	store := newTestStore(t)

	if _, ok := store.GetMetadataCache("never-written", time.Hour); ok {
		t.Error("expected a miss for a key that was never written")
	}
}

func TestMetadataCache_UpsertReplacesPayload(t *testing.T) {
	store := newTestStore(t)
	const key = "rawg|id|123|"

	if err := store.PutMetadataCache(key, []byte(`{"v":1}`)); err != nil {
		t.Fatalf("first put: %v", err)
	}
	if err := store.PutMetadataCache(key, []byte(`{"v":2}`)); err != nil {
		t.Fatalf("second put: %v", err)
	}

	payload, ok := store.GetMetadataCache(key, time.Hour)
	if !ok {
		t.Fatal("expected a hit after upsert")
	}
	if string(payload) != `{"v":2}` {
		t.Errorf("payload = %q, want the second write (upsert should replace, not duplicate)", payload)
	}
}

// TestMetadataCache_TTLExpiry covers the TTL comparison against the stored
// float unix timestamp - the part most likely to be silently wrong, since a
// sign error or unit mismatch would make entries either never or always expire.
func TestMetadataCache_TTLExpiry(t *testing.T) {
	store := newTestStore(t)
	const key = "igdb|search|old|"

	if err := store.PutMetadataCache(key, []byte(`{"stale":true}`)); err != nil {
		t.Fatalf("PutMetadataCache: %v", err)
	}

	// Backdate the row an hour so it is older than the TTL under test.
	if _, err := store.db.Exec(
		"UPDATE metadata_cache SET fetched_at = strftime('%s','now') - 3600 WHERE cache_key = ?", key,
	); err != nil {
		t.Fatalf("backdate row: %v", err)
	}

	if _, ok := store.GetMetadataCache(key, 30*time.Minute); ok {
		t.Error("entry older than the TTL should be a miss")
	}
	if _, ok := store.GetMetadataCache(key, 2*time.Hour); !ok {
		t.Error("entry younger than the TTL should be a hit")
	}
}

func TestMetadataCache_NonPositiveTTLDisablesReads(t *testing.T) {
	store := newTestStore(t)
	const key = "igdb|search|fresh|"

	if err := store.PutMetadataCache(key, []byte(`{"fresh":true}`)); err != nil {
		t.Fatalf("PutMetadataCache: %v", err)
	}

	for _, ttl := range []time.Duration{0, -time.Minute} {
		if _, ok := store.GetMetadataCache(key, ttl); ok {
			t.Errorf("ttl=%v should disable the cache and report a miss", ttl)
		}
	}
}

func TestMetadataCache_EmptyKeyIsIgnored(t *testing.T) {
	store := newTestStore(t)

	if err := store.PutMetadataCache("", []byte(`{}`)); err != nil {
		t.Errorf("PutMetadataCache with an empty key should no-op, got %v", err)
	}
	if _, ok := store.GetMetadataCache("", time.Hour); ok {
		t.Error("an empty key should never hit")
	}
}

func TestMetadataCache_Purge(t *testing.T) {
	store := newTestStore(t)

	if err := store.PutMetadataCache("old", []byte(`{}`)); err != nil {
		t.Fatalf("put old: %v", err)
	}
	if err := store.PutMetadataCache("new", []byte(`{}`)); err != nil {
		t.Fatalf("put new: %v", err)
	}
	if _, err := store.db.Exec(
		"UPDATE metadata_cache SET fetched_at = strftime('%s','now') - 7200 WHERE cache_key = 'old'",
	); err != nil {
		t.Fatalf("backdate row: %v", err)
	}

	if n := store.PurgeMetadataCache(time.Hour); n != 1 {
		t.Errorf("PurgeMetadataCache removed %d rows, want 1", n)
	}
	if _, ok := store.GetMetadataCache("old", 24*time.Hour); ok {
		t.Error("purged entry should be gone")
	}
	if _, ok := store.GetMetadataCache("new", time.Hour); !ok {
		t.Error("recent entry should have survived the purge")
	}

	if n := store.PurgeMetadataCache(0); n != 0 {
		t.Errorf("PurgeMetadataCache(0) removed %d rows, want 0 (no-op)", n)
	}
}
