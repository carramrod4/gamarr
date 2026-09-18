package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// LibraryItem represents a game/ROM in the library.
type LibraryItem struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Platform     string `json:"platform"`
	PlatformSlug string `json:"platform_slug"`
	IsPC         bool   `json:"is_pc"`
	FilePath     string `json:"file_path"`
	FileSize     int64  `json:"file_size"`
	Source       string `json:"source"`      // "torrent", "ddl", "scan"
	SourceType   string `json:"source_type"` // "prowlarr", "myrient", "vimm", "minerva", "manual"
	SourceID     string `json:"source_id"`   // dedup key (hash, url, etc.)
	Metadata     string `json:"metadata"`    // JSON blob
	// Monitored mirrors Sonarr/Radarr's per-item monitored flag: whether
	// Gamarr should keep looking for a better release of this game. New rows
	// default to monitored (see AddLibraryItem).
	Monitored bool   `json:"monitored"`
	AddedAt   string `json:"added_at"`
}

// WishlistItem represents a game on the wishlist.
type WishlistItem struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Platform     string `json:"platform"`
	PlatformSlug string `json:"platform_slug"`
	AddedAt      string `json:"added_at"`
}

// ActivityEntry represents an activity log entry.
type ActivityEntry struct {
	ID            int64  `json:"id"`
	EventType     string `json:"event_type"`
	Title         string `json:"title"`
	Detail        string `json:"detail"`
	LibraryItemID *int64 `json:"library_item_id,omitempty"`
	JobID         string `json:"job_id,omitempty"`
	Timestamp     string `json:"timestamp"`
}

// LibraryPage is a paginated library result.
type LibraryPage struct {
	Items      []LibraryItem `json:"items"`
	Total      int           `json:"total"`
	Page       int           `json:"page"`
	PageSize   int           `json:"page_size"`
	TotalPages int           `json:"total_pages"`
}

// libraryColumns is the canonical column list for every library_items read.
// It was inline in seven separate queries before; keeping it in one place is
// what makes adding a column (like monitored) a single edit instead of seven
// that must agree with each other and with every Scan call.
const libraryColumns = "id, title, platform, platform_slug, is_pc, file_path, " +
	"file_size, source, source_type, source_id, metadata, monitored, added_at"

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

// scanLibraryItem reads one row selected with libraryColumns.
func scanLibraryItem(sc rowScanner) (LibraryItem, error) {
	var item LibraryItem
	var isPC, monitored int
	err := sc.Scan(&item.ID, &item.Title, &item.Platform, &item.PlatformSlug,
		&isPC, &item.FilePath, &item.FileSize, &item.Source, &item.SourceType,
		&item.SourceID, &item.Metadata, &monitored, &item.AddedAt)
	if err != nil {
		return LibraryItem{}, err
	}
	item.IsPC = isPC != 0
	item.Monitored = monitored != 0
	return item, nil
}

func (s *JobStore) migrateExtra() {
	s.migrateRequests()
	s.migrateNotifications()
	s.migrateWebhooks()
	s.migrateHistory()
	s.migrateQualityProfiles()
	s.migrateBlocklist()
	s.migrateReleaseProfiles()
	s.migrateTags()
	s.migrateMetadataCache()

	tables := []string{
		`CREATE TABLE IF NOT EXISTS library_items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			platform TEXT NOT NULL DEFAULT '',
			platform_slug TEXT NOT NULL DEFAULT '',
			is_pc INTEGER NOT NULL DEFAULT 0,
			file_path TEXT NOT NULL DEFAULT '',
			file_size INTEGER NOT NULL DEFAULT 0,
			source TEXT NOT NULL DEFAULT '',
			source_type TEXT NOT NULL DEFAULT '',
			source_id TEXT NOT NULL DEFAULT '',
			metadata TEXT NOT NULL DEFAULT '{}',
			monitored INTEGER NOT NULL DEFAULT 1,
			added_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS wishlist (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			platform TEXT NOT NULL DEFAULT '',
			platform_slug TEXT NOT NULL DEFAULT '',
			added_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS activity_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_type TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			library_item_id INTEGER,
			job_id TEXT NOT NULL DEFAULT '',
			timestamp TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_library_platform ON library_items(platform_slug)`,
		`CREATE INDEX IF NOT EXISTS idx_library_source_id ON library_items(source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_activity_timestamp ON activity_log(timestamp)`,
	}
	for _, ddl := range tables {
		if _, err := s.db.Exec(ddl); err != nil {
			slog.Warn("migrate extra table", "error", err)
		}
	}

	// Upgrade path for databases created before the column existed. The error
	// is discarded deliberately: SQLite has no ADD COLUMN IF NOT EXISTS, so a
	// "duplicate column name" failure here is the normal steady state. Same
	// idiom as migrateUsers' TOTP columns.
	s.db.Exec("ALTER TABLE library_items ADD COLUMN monitored INTEGER NOT NULL DEFAULT 1")
}

// DB returns the underlying sql.DB for direct use.
func (s *JobStore) DB() *sql.DB {
	return s.db
}

// ── Library Items ──────────────────────────────────────────────────────────────

// AddLibraryItem inserts a new library item.
//
// monitored is deliberately left out of the column list so the schema default
// (1) applies: a newly added game is monitored, matching Sonarr/Radarr. Callers
// that want it off flip it afterwards with SetLibraryItemMonitored.
func (s *JobStore) AddLibraryItem(item *LibraryItem) (int64, error) {
	result, err := s.db.Exec(
		`INSERT INTO library_items (title, platform, platform_slug, is_pc, file_path, file_size, source, source_type, source_id, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.Title, item.Platform, item.PlatformSlug, boolToInt(item.IsPC),
		item.FilePath, item.FileSize, item.Source, item.SourceType, item.SourceID, item.Metadata,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetLibraryPage returns a paginated library.
func (s *JobStore) GetLibraryPage(page, pageSize int, query, platformSlug string) LibraryPage {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}

	where, args := buildLibraryWhere(query, platformSlug)

	var total int
	row := s.db.QueryRow("SELECT COUNT(*) FROM library_items "+where, args...)
	row.Scan(&total)

	totalPages := (total + pageSize - 1) / pageSize
	offset := (page - 1) * pageSize

	rows, err := s.db.Query(
		"SELECT "+libraryColumns+" FROM library_items "+
			where+" ORDER BY added_at DESC LIMIT ? OFFSET ?",
		append(args, pageSize, offset)...,
	)
	if err != nil {
		return LibraryPage{Page: page, PageSize: pageSize}
	}
	defer rows.Close()

	var items []LibraryItem
	for rows.Next() {
		item, err := scanLibraryItem(rows)
		if err != nil {
			continue
		}
		items = append(items, item)
	}
	if items == nil {
		items = []LibraryItem{}
	}

	return LibraryPage{
		Items:      items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}
}

// GetLibraryItem returns a single library item by ID.
func (s *JobStore) GetLibraryItem(id int64) (*LibraryItem, error) {
	row := s.db.QueryRow(
		"SELECT "+libraryColumns+" FROM library_items WHERE id = ?",
		id,
	)
	item, err := scanLibraryItem(row)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// UpdateLibraryItemMetadata updates the metadata JSON blob for a library item.
func (s *JobStore) UpdateLibraryItemMetadata(id int64, metadata string) error {
	_, err := s.db.Exec("UPDATE library_items SET metadata = ? WHERE id = ?", metadata, id)
	return err
}

// SetLibraryItemMonitored flips the monitored flag for one library item.
// A missing id is reported as an error rather than silently succeeding, so an
// API caller gets a 404 instead of a false confirmation.
func (s *JobStore) SetLibraryItemMonitored(id int64, monitored bool) error {
	res, err := s.db.Exec("UPDATE library_items SET monitored = ? WHERE id = ?", boolToInt(monitored), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteLibraryItem deletes a library item by ID.
func (s *JobStore) DeleteLibraryItem(id int64) error {
	_, err := s.db.Exec("DELETE FROM library_items WHERE id = ?", id)
	return err
}

// DeleteLibraryItemsByPath removes every library row pointing at a file path
// except the one keeping it. Source ids are scheme-prefixed ("scan:",
// "torrent:", "nzb:", "ddl:"), so a row recorded when a game was downloaded can
// never collide with the scan's id for the same file and LibraryHasSourceID
// cannot see it. The path can.
//
// keepID is the row that supersedes the others, so the caller inserts first and
// prunes after -- pruning first would leave the file with no row at all if the
// insert then failed.
func (s *JobStore) DeleteLibraryItemsByPath(path string, keepID int64) int64 {
	if path == "" {
		return 0
	}
	res, err := s.db.Exec("DELETE FROM library_items WHERE file_path = ? AND id != ?", path, keepID)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// LibraryHasSourceID checks if a source_id already exists in the library.
func (s *JobStore) LibraryHasSourceID(sourceID string) bool {
	if sourceID == "" {
		return false
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM library_items WHERE source_id = ?", sourceID).Scan(&count)
	return count > 0
}

// LibraryStats returns counts by platform.
func (s *JobStore) LibraryStats() map[string]int {
	stats := make(map[string]int)
	rows, err := s.db.Query(`SELECT COALESCE(NULLIF(platform_slug,''), CASE WHEN is_pc THEN 'pc' ELSE 'unknown' END) as p, COUNT(*) FROM library_items GROUP BY p`)
	if err != nil {
		return stats
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var c int
		rows.Scan(&p, &c)
		stats[p] = c
	}
	return stats
}

// LibraryTotal returns the total number of library items.
func (s *JobStore) LibraryTotal() int {
	var total int
	s.db.QueryRow("SELECT COUNT(*) FROM library_items").Scan(&total)
	return total
}

// ScanLibraryDir scans a directory and adds new items to the library.
func (s *JobStore) ScanLibraryDir(dir, platform, platformSlug string, isPC bool) int {
	// Implemented in download/manager.go or called from main
	return 0
}

func buildLibraryWhere(query, platformSlug string) (string, []interface{}) {
	var conditions []string
	var args []interface{}
	if query != "" {
		conditions = append(conditions, "title LIKE ?")
		args = append(args, "%"+query+"%")
	}
	if platformSlug != "" && platformSlug != "all" {
		if platformSlug == "pc" {
			conditions = append(conditions, "is_pc = 1")
		} else {
			conditions = append(conditions, "platform_slug = ?")
			args = append(args, platformSlug)
		}
	}
	if len(conditions) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

// ── Wishlist ───────────────────────────────────────────────────────────────────

// AddWishlistItem adds an item to the wishlist.
func (s *JobStore) AddWishlistItem(title, platform, platformSlug string) (int64, error) {
	result, err := s.db.Exec(
		"INSERT INTO wishlist (title, platform, platform_slug) VALUES (?, ?, ?)",
		title, platform, platformSlug,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetWishlist returns all wishlist items.
func (s *JobStore) GetWishlist() []WishlistItem {
	rows, err := s.db.Query("SELECT id, title, platform, platform_slug, added_at FROM wishlist ORDER BY added_at DESC")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var items []WishlistItem
	for rows.Next() {
		var item WishlistItem
		rows.Scan(&item.ID, &item.Title, &item.Platform, &item.PlatformSlug, &item.AddedAt)
		items = append(items, item)
	}
	return items
}

// DeleteWishlistItem removes a wishlist item.
func (s *JobStore) DeleteWishlistItem(id int64) error {
	_, err := s.db.Exec("DELETE FROM wishlist WHERE id = ?", id)
	return err
}

// ── Activity Log ───────────────────────────────────────────────────────────────

// LogActivity writes an activity log entry.
// ActivityCount returns the total number of activity log entries.
func (s *JobStore) ActivityCount() int {
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM activity_log").Scan(&count)
	return count
}

func (s *JobStore) LogActivity(eventType, title, detail, jobID string, libraryItemID *int64) {
	_, err := s.db.Exec(
		"INSERT INTO activity_log (event_type, title, detail, library_item_id, job_id) VALUES (?, ?, ?, ?, ?)",
		eventType, title, detail, libraryItemID, jobID,
	)
	if err != nil {
		slog.Warn("failed to log activity", "error", err)
	}
}

// GetActivity returns recent activity with pagination.
func (s *JobStore) GetActivity(page, pageSize int) ([]ActivityEntry, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	var total int
	s.db.QueryRow("SELECT COUNT(*) FROM activity_log").Scan(&total)

	offset := (page - 1) * pageSize
	rows, err := s.db.Query(
		"SELECT id, event_type, title, detail, library_item_id, job_id, timestamp FROM activity_log ORDER BY timestamp DESC LIMIT ? OFFSET ?",
		pageSize, offset,
	)
	if err != nil {
		return nil, total
	}
	defer rows.Close()

	var entries []ActivityEntry
	for rows.Next() {
		var e ActivityEntry
		var libID sql.NullInt64
		rows.Scan(&e.ID, &e.EventType, &e.Title, &e.Detail, &libID, &e.JobID, &e.Timestamp)
		if libID.Valid {
			e.LibraryItemID = &libID.Int64
		}
		entries = append(entries, e)
	}
	return entries, total
}

// ── Helpers ────────────────────────────────────────────────────────────────────

// ScanMetadataByPath returns the metadata blob of every scan-sourced row that
// has one, keyed by file path.
//
// It exists so a rescan can put enrichment back. ClearScanEntries deletes every
// scan row and the rescan re-inserts them with an empty metadata blob, so
// without this every container restart silently discarded all enriched
// metadata - thousands of provider lookups thrown away on each boot. The file
// path is the key because it is the one identifier that survives the
// delete/re-insert cycle: ids are reassigned and titles can change when the
// filename parser improves.
func (s *JobStore) ScanMetadataByPath() map[string]string {
	rows, err := s.db.Query(
		"SELECT file_path, metadata FROM library_items WHERE source = 'scan' AND metadata != '' AND metadata != '{}'")
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var path, metadata string
		if err := rows.Scan(&path, &metadata); err != nil || path == "" {
			continue
		}
		out[path] = metadata
	}
	return out
}

// RestoreScanMetadata writes saved metadata blobs back onto rows matched by
// file path, and reports how many were restored. Paths no longer present (the
// file was deleted or moved) simply match nothing.
func (s *JobStore) RestoreScanMetadata(byPath map[string]string) int {
	if len(byPath) == 0 {
		return 0
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0
	}
	stmt, err := tx.Prepare("UPDATE library_items SET metadata = ? WHERE file_path = ? AND (metadata = '' OR metadata = '{}')")
	if err != nil {
		tx.Rollback()
		return 0
	}
	defer stmt.Close()

	restored := 0
	for path, metadata := range byPath {
		res, err := stmt.Exec(metadata, path)
		if err != nil {
			continue
		}
		if n, err := res.RowsAffected(); err == nil {
			restored += int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0
	}
	return restored
}

// ClearScanEntries removes all library items added by directory scanning.
// This is called before a rescan to ensure accuracy.
func (s *JobStore) ClearScanEntries() {
	result, _ := s.db.Exec("DELETE FROM library_items WHERE source = 'scan'")
	if n, _ := result.RowsAffected(); n > 0 {
		slog.Info("cleared scan entries for rescan", "count", n)
	}
}

// FindLibraryByTitle checks if a game with a matching title+platform exists in the library.
// Uses case-insensitive LIKE matching. Returns nil if not found.
func (s *JobStore) FindLibraryByTitle(title, platformSlug string) *LibraryItem {
	if title == "" {
		return nil
	}

	var query string
	var args []interface{}

	normalizedTitle := strings.ToLower(strings.TrimSpace(title))

	if platformSlug != "" && platformSlug != "all" {
		if platformSlug == "pc" {
			query = "SELECT " + libraryColumns + " FROM library_items WHERE LOWER(title) = ? AND is_pc = 1 LIMIT 1"
			args = []interface{}{normalizedTitle}
		} else {
			query = "SELECT " + libraryColumns + " FROM library_items WHERE LOWER(title) = ? AND platform_slug = ? LIMIT 1"
			args = []interface{}{normalizedTitle, platformSlug}
		}
	} else {
		query = "SELECT " + libraryColumns + " FROM library_items WHERE LOWER(title) = ? LIMIT 1"
		args = []interface{}{normalizedTitle}
	}

	item, err := scanLibraryItem(s.db.QueryRow(query, args...))
	if err != nil {
		return nil
	}
	return &item
}

// GetAllLibraryTitles returns a map of normalized "title|platform_slug" to LibraryItem for bulk lookups.
func (s *JobStore) GetAllLibraryTitles() map[string]*LibraryItem {
	rows, err := s.db.Query("SELECT " + libraryColumns + " FROM library_items")
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := make(map[string]*LibraryItem)
	for rows.Next() {
		item, err := scanLibraryItem(rows)
		if err != nil {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(item.Title)) + "|" + item.PlatformSlug
		cp := item
		result[key] = &cp
	}
	return result
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// FormatSize formats bytes into a human-readable string.
func FormatSize(size int64) string {
	if size == 0 {
		return "?"
	}
	s := float64(size)
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if s < 1024 {
			return fmt.Sprintf("%.1f %s", s, unit)
		}
		s /= 1024
	}
	return fmt.Sprintf("%.1f TB", s)
}

// RecentLibraryItems returns the most recently added items.
func (s *JobStore) RecentLibraryItems(limit int) []LibraryItem {
	rows, err := s.db.Query(
		"SELECT "+libraryColumns+" FROM library_items ORDER BY added_at DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var items []LibraryItem
	for rows.Next() {
		item, err := scanLibraryItem(rows)
		if err != nil {
			continue
		}
		items = append(items, item)
	}
	return items
}

// init extra tables during New()
func init() {
	// We'll call migrateExtra from New after the initial migrate
	_ = time.Now // avoid unused import
}
