package metadata

import (
	"archive/zip"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	// The driver has to be registered here too: sql.Open("sqlite", ...) fails
	// with "unknown driver" without it, and that surfaced only as "database did
	// not open" with no hint as to why.
	_ "modernc.org/sqlite"
)

// HashDB identifies a ROM from its contents rather than its filename.
//
// Every metadata miss this project has chased has been a filename problem: an
// extension left on the title, a region tag, a separator-style name, a
// misspelling in the ROM set itself. None of that matters to a hash. OpenVGDB
// maps ~52,000 known dumps to their canonical release title, so hashing a file
// and looking it up answers "what game is this" exactly, and the cleaned-up
// title becomes a fallback rather than the primary key.
//
// The database is downloaded once into the config volume rather than baked into
// the image: it is 42 MB, changes independently of this service, and an install
// that never scans ROMs should not carry it.
type HashDB struct {
	mu   sync.RWMutex
	db   *sql.DB
	path string
}

const openVGDBURL = "https://github.com/OpenVGDB/OpenVGDB/releases/download/v29.0/openvgdb.zip"

// NewHashDB opens the database at dir/openvgdb.sqlite, downloading it first if
// it is not there. A failure is not fatal: without it the resolver simply falls
// back to title matching, which is what it did before.
func NewHashDB(dir string) *HashDB {
	h := &HashDB{path: filepath.Join(dir, "openvgdb.sqlite")}
	if err := h.ensure(); err != nil {
		log.Printf("hashdb: unavailable, falling back to title matching: %v", err)
		return h
	}
	return h
}

func (h *HashDB) ensure() error {
	if _, err := os.Stat(h.path); err != nil {
		if err := h.download(); err != nil {
			return err
		}
	}
	db, err := sql.Open("sqlite", h.path+"?_pragma=query_only(true)&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return err
	}
	h.mu.Lock()
	h.db = db
	h.mu.Unlock()
	log.Printf("hashdb: ready at %s", h.path)
	return nil
}

func (h *HashDB) download() error {
	log.Printf("hashdb: downloading OpenVGDB")
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(openVGDBURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %s", resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Dir(h.path), "openvgdb-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	zr, err := zip.OpenReader(tmp.Name())
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		// The archive also carries a __MACOSX folder of resource forks, so the
		// entry is chosen by extension rather than by being the only one.
		if !strings.HasSuffix(strings.ToLower(f.Name), ".sqlite") || strings.Contains(f.Name, "__MACOSX") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(h.path)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		return err
	}
	return fmt.Errorf("no .sqlite entry in the archive")
}

// Available reports whether lookups can succeed.
func (h *HashDB) Available() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.db != nil
}

// Match is what a hash lookup answers.
type Match struct {
	Title    string
	Region   string
	System   string
	FileName string // the No-Intro name, useful for artwork lookups
}

// LookupMD5 finds a dump by the MD5 of its contents.
//
// OpenVGDB stores hashes uppercase and SQLite's text comparison is
// case-sensitive, so the hash is upper-cased here rather than relying on the
// caller - a lower-case hash silently matches nothing, which is the worst kind
// of failure because it looks exactly like an unknown game.
func (h *HashDB) LookupMD5(md5sum string) (Match, bool) {
	h.mu.RLock()
	db := h.db
	h.mu.RUnlock()
	if db == nil || md5sum == "" {
		return Match{}, false
	}

	const q = `
		SELECT DISTINCT releaseTitleName, COALESCE(regionName, ''),
		       COALESCE(system.systemShortName, ''), COALESCE(rom.romFileName, '')
		FROM ROMs rom
		LEFT JOIN RELEASES release USING (romID)
		LEFT JOIN REGIONS region ON (regionLocalizedID = region.regionID)
		LEFT JOIN SYSTEMS system ON (rom.systemID = system.systemID)
		WHERE romHashMD5 = ?
		LIMIT 1`

	var m Match
	err := db.QueryRow(q, strings.ToUpper(md5sum)).Scan(&m.Title, &m.Region, &m.System, &m.FileName)
	if err != nil || m.Title == "" {
		return Match{}, false
	}
	return m, true
}

func (h *HashDB) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.db != nil {
		h.db.Close()
		h.db = nil
	}
}
