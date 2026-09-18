package db

import (
	"log/slog"
	"time"
)

// migrateMetadataCache creates the provider-response cache used by
// internal/metadata's resolver.
//
// Cached payloads are whole resolved records keyed by provider+query, so a
// repeated enrich of the same title costs no upstream requests - which matters
// most for IGDB, whose 4-requests-per-second budget is the tightest of the
// three providers.
func (s *JobStore) migrateMetadataCache() {
	ddl := `CREATE TABLE IF NOT EXISTS metadata_cache (
		cache_key TEXT PRIMARY KEY,
		payload TEXT NOT NULL,
		fetched_at REAL NOT NULL DEFAULT (strftime('%s','now'))
	)`
	if _, err := s.db.Exec(ddl); err != nil {
		slog.Warn("migrate metadata_cache", "error", err)
	}
	_, _ = s.db.Exec("CREATE INDEX IF NOT EXISTS idx_metadata_cache_fetched ON metadata_cache(fetched_at)")
}

// GetMetadataCache returns the cached payload for key when it is younger than
// maxAge. A maxAge of zero or less disables the cache entirely (every lookup
// reports a miss), which is how callers turn caching off via config.
func (s *JobStore) GetMetadataCache(key string, maxAge time.Duration) ([]byte, bool) {
	if key == "" || maxAge <= 0 {
		return nil, false
	}
	cutoff := float64(time.Now().Add(-maxAge).Unix())

	var payload string
	err := s.db.QueryRow(
		"SELECT payload FROM metadata_cache WHERE cache_key = ? AND fetched_at > ?",
		key, cutoff,
	).Scan(&payload)
	if err != nil {
		return nil, false
	}
	return []byte(payload), true
}

// PutMetadataCache stores (or refreshes) the payload for key.
func (s *JobStore) PutMetadataCache(key string, payload []byte) error {
	if key == "" {
		return nil
	}
	_, err := s.db.Exec(`
		INSERT INTO metadata_cache (cache_key, payload, fetched_at)
		VALUES (?, ?, strftime('%s','now'))
		ON CONFLICT(cache_key) DO UPDATE SET
			payload = excluded.payload,
			fetched_at = excluded.fetched_at
	`, key, string(payload))
	return err
}

// PurgeMetadataCache deletes entries older than olderThan and returns how many
// rows were removed. Entries are also ignored on read once past their TTL, so
// this is disk housekeeping rather than a correctness requirement.
func (s *JobStore) PurgeMetadataCache(olderThan time.Duration) int64 {
	if olderThan <= 0 {
		return 0
	}
	cutoff := float64(time.Now().Add(-olderThan).Unix())
	res, err := s.db.Exec("DELETE FROM metadata_cache WHERE fetched_at <= ?", cutoff)
	if err != nil {
		slog.Warn("purge metadata_cache", "error", err)
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}
