package download

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gamarr/internal/db"
)

// ScanLibraryDirs scans vault and ROM directories to populate the library.
// Clears previous scan entries and rescans from scratch for accuracy.
func (m *Manager) ScanLibraryDirs() {
	// Enrichment lives on the library row, and the clear-and-rescan below
	// deletes every scan row, re-inserting it with an empty metadata blob. So
	// without carrying it across, every restart silently discarded all
	// enriched metadata and it had to be fetched from the providers again.
	// Keyed by file path, which survives the delete/re-insert cycle.
	savedMetadata := m.jobs.ScanMetadataByPath()

	// Clear previous scan entries so we always reflect current disk state
	m.jobs.ClearScanEntries()
	total := 0

	// Scan PC games vault — each top-level entry is one game (no recursion)
	if m.cfg.GamesVaultPath != "" {
		n := m.scanVault(m.cfg.GamesVaultPath)
		total += n
	}

	// Scan ROM platform directories
	if m.cfg.GamesRomsPath != "" {
		entries, err := os.ReadDir(m.cfg.GamesRomsPath)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					slug := e.Name()
					platName := platformNameFromSlug(slug)
					n := m.scanDir(filepath.Join(m.cfg.GamesRomsPath, slug), platName, slug, false)
					total += n
				}
			}
		}
	}

	if restored := m.jobs.RestoreScanMetadata(savedMetadata); restored > 0 {
		slog.Info("restored metadata after rescan", "items", restored)
	}

	if total > 0 {
		slog.Info("library scan complete", "new_items", total)
	}
}

// scanVault scans the PC games vault. Each top-level entry (dir or archive) is one game.
func (m *Manager) scanVault(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	added := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		// Skip loose .gamarr.json sidecars
		if strings.HasSuffix(name, ".gamarr.json") {
			continue
		}
		fp := filepath.Join(dir, name)
		added += m.addLibraryEntry(fp, name, "PC", "", true)
	}
	return added
}

// gameExtensions are file extensions that represent playable games/ROMs.
var gameExtensions = map[string]bool{
	".nsp": true, ".xci": true, ".nsz": true, // Switch
	".nes": true, ".sfc": true, ".smc": true, // NES/SNES
	".gba": true, ".gb": true, ".gbc": true, // Game Boy
	".nds": true, ".3ds": true, ".cia": true, // DS/3DS
	".n64": true, ".z64": true, ".v64": true, // N64
	".iso": true, ".bin": true, ".cue": true, // Disc images
	".chd": true, ".gdi": true, ".cdi": true, // Compressed disc
	".gcz": true, ".gcm": true, ".rvz": true, // GameCube
	".wbfs": true, ".wad": true, // Wii
	".pbp": true, ".cso": true, // PSP
	".zip": true, ".7z": true, ".rar": true, // Archives (common for ROMs)
	".exe": true, ".msi": true, // PC
}

func (m *Manager) scanDir(dir, platform, platformSlug string, isPC bool) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	added := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if strings.HasSuffix(name, ".gamarr.json") || strings.HasSuffix(name, ".extracted") {
			continue
		}
		fp := filepath.Join(dir, name)

		if e.IsDir() {
			// For ROM platforms, check if this directory contains game files
			// or is just an organizational subdirectory (like "roms/")
			if !isPC && containsGameFiles(fp) {
				// This is a game folder (e.g., "TowerFall [NSP]/")
				added += m.addLibraryEntry(fp, name, platform, platformSlug, isPC)
			} else {
				// Recurse into subdirectories (handles nested "roms/" dirs)
				added += m.scanDir(fp, platform, platformSlug, isPC)
			}
		} else {
			// Single file — check if it's a game file
			ext := strings.ToLower(filepath.Ext(name))
			if isPC || gameExtensions[ext] {
				// Skip small files (DLC, updates, sidecars)
				if info, err := e.Info(); err == nil && info.Size() < 1_000_000 && !isPC {
					continue
				}
				// Skip update files
				nameLower := strings.ToLower(name)
				if strings.HasPrefix(nameLower, "[update]") || strings.Contains(nameLower, "update v") {
					continue
				}
				// Skip DLC/costume files for Smash etc.
				if strings.Contains(nameLower, "costume") || strings.Contains(nameLower, "challenger pack") ||
					strings.Contains(nameLower, "spirit board") || strings.Contains(nameLower, "fighters pass") ||
					strings.Contains(nameLower, "[dlc]") || strings.Contains(nameLower, "vault shopper") {
					continue
				}
				added += m.addLibraryEntry(fp, name, platform, platformSlug, isPC)
			}
		}
	}
	return added
}

func (m *Manager) addLibraryEntry(fp, name, platform, platformSlug string, isPC bool) int {
	sourceID := "scan:" + fp
	if m.jobs.LibraryHasSourceID(sourceID) {
		return 0
	}

	var fileSize int64
	info, err := os.Stat(fp)
	if err == nil {
		if info.IsDir() {
			fileSize = dirSize(fp)
		} else {
			fileSize = info.Size()
		}
	}

	title := cleanTitle(name)
	id, err := m.jobs.AddLibraryItem(&db.LibraryItem{
		Title:        title,
		Platform:     platform,
		PlatformSlug: platformSlug,
		IsPC:         isPC,
		FilePath:     fp,
		FileSize:     fileSize,
		Source:       "scan",
		SourceType:   "scan",
		SourceID:     sourceID,
		Metadata:     "{}",
	})
	if err != nil {
		return 0
	}

	// A row recorded when the game was downloaded points at this same file under
	// a different source scheme, so the guard above cannot see it and the game
	// shows twice: once titled from the torrent, with no size, and once from this
	// scan. The scan is the better record - it has the real size from disk and a
	// title derived from the filename rather than the raw torrent name. Pruned
	// after the insert, so a failed insert cannot leave the file unrecorded.
	if n := m.jobs.DeleteLibraryItemsByPath(fp, id); n > 0 {
		slog.Info("library scan superseded download-time rows", "path", fp, "removed", n)
	}
	return 1
}

// containsGameFiles checks if a directory directly contains game ROM files.
func containsGameFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if gameExtensions[ext] {
			return true
		}
	}
	return false
}

// TrackInLibrary adds a completed download to the library.
func (m *Manager) TrackInLibrary(title, platform, platformSlug string, isPC bool, filePath string, fileSize int64, source, sourceType, sourceID string) {
	if m.jobs.LibraryHasSourceID(sourceID) {
		return
	}
	id, err := m.jobs.AddLibraryItem(&db.LibraryItem{
		Title:        title,
		Platform:     platform,
		PlatformSlug: platformSlug,
		IsPC:         isPC,
		FilePath:     filePath,
		FileSize:     fileSize,
		Source:       source,
		SourceType:   sourceType,
		SourceID:     sourceID,
		Metadata:     "{}",
	})
	if err != nil {
		slog.Warn("failed to add to library", "error", err)
		return
	}
	m.jobs.LogActivity("import_completed", title, "Added to library: "+platform, "", &id)
}

func dirSize(path string) int64 {
	var total int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// archiveExtensions are container formats that are not themselves game files,
// so they are absent from gameExtensions but still need stripping off a title.
var archiveExtensions = map[string]bool{
	".tar": true, ".gz": true, ".bz2": true, ".xz": true,
}

// cleanTitle turns a filename into something a metadata provider can match.
func cleanTitle(name string) string {
	// URL-decode first, so a percent-encoded name still exposes its extension
	// and tags to the steps below.
	name = strings.ReplaceAll(name, "%20", " ")
	name = strings.ReplaceAll(name, "%28", "(")
	name = strings.ReplaceAll(name, "%29", ")")
	name = strings.ReplaceAll(name, "%2C", ",")

	// Strip the extension using gameExtensions - the same map scanDir uses to
	// decide a file IS a game - rather than a second hardcoded list. That
	// duplicate list was a real bug: it omitted .smc, .gb, .gbc, .cue, .cdi and
	// more, so thousands of ROMs kept ".smc" in their stored title and matched
	// nothing upstream (verified live: "EARTH BOUND.smc" resolves to nothing).
	// Deriving it from one map means a newly supported format cannot be
	// scannable but unstrippable again.
	if ext := strings.ToLower(filepath.Ext(name)); ext != "" {
		if gameExtensions[ext] || archiveExtensions[ext] {
			name = name[:len(name)-len(ext)]
			// ".tar.gz" leaves a trailing ".tar" behind.
			if ext2 := strings.ToLower(filepath.Ext(name)); archiveExtensions[ext2] {
				name = name[:len(name)-len(ext2)]
			}
		}
	}

	return strings.TrimSpace(stripROMTags(name))
}

// stripROMTags removes the trailing "(E)", "(USA)", "(Rev 1)", "[!]" style
// annotations that ROM sets append.
//
// These are meaningless to metadata providers and actively prevent a match -
// verified live against IGDB: "Donkey Kong Country 2 (E)" returns nothing while
// "Donkey Kong Country 2" resolves correctly. Only *trailing* groups are
// removed, and never the whole title, so a name that is nothing but a
// parenthesised group survives rather than becoming empty.
func stripROMTags(name string) string {
	for {
		trimmed := strings.TrimSpace(name)
		if len(trimmed) == 0 {
			return name
		}
		var open byte
		switch trimmed[len(trimmed)-1] {
		case ')':
			open = '('
		case ']':
			open = '['
		default:
			return trimmed
		}
		idx := strings.LastIndexByte(trimmed, open)
		// No opener, or the group is the entire title - leave it alone rather
		// than returning an empty string.
		if idx <= 0 {
			return trimmed
		}
		name = trimmed[:idx]
	}
}

func platformNameFromSlug(slug string) string {
	names := map[string]string{
		"gba": "Game Boy Advance", "gb": "Game Boy", "gbc": "Game Boy Color",
		"nes": "NES", "snes": "SNES", "n64": "Nintendo 64",
		"nds": "DS", "3ds": "3DS", "switch": "Switch",
		"psx": "PS1", "ps2": "PS2", "ps3": "PS3", "ps4": "PS4",
		"psp": "PSP", "dc": "Dreamcast",
		"genesis": "Sega Genesis", "saturn": "Sega Saturn",
		"ngc": "GameCube", "wii": "Wii", "wiiu": "Wii U",
		"xbox": "Xbox", "xbox360": "Xbox 360",
		"psvita": "PS Vita",
	}
	if name, ok := names[slug]; ok {
		return name
	}
	return strings.ToUpper(slug)
}
