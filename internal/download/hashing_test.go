package download

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestHashVariantsStripsINESHeader pins the defect that made hash matching
// useless for the single largest platform in the library.
//
// OpenVGDB declares systemHeaderSizeBytes = 16 for NES: its hashes are of the
// ROM *after* the 16-byte iNES header. Hashing the whole file therefore matched
// 0 of 1,605 real NES ROMs. The headerless variant has to be offered too.
func TestHashVariantsStripsINESHeader(t *testing.T) {
	body := make([]byte, 40*1024)
	for i := range body {
		body[i] = byte(i % 251)
	}
	header := []byte("NES\x1a") // iNES magic
	header = append(header, make([]byte, 12)...)

	path := filepath.Join(t.TempDir(), "Game.nes")
	if err := os.WriteFile(path, append(header, body...), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	variants := hashFileMD5Variants(path, info.Size())
	if len(variants) < 2 {
		t.Fatalf("got %d hash variants, want at least the whole file and the headerless one", len(variants))
	}

	whole := md5.Sum(append(header, body...))
	headerless := md5.Sum(body)

	if variants[0] != hex.EncodeToString(whole[:]) {
		t.Errorf("first variant is not the whole-file hash")
	}
	found := false
	for _, v := range variants {
		if v == hex.EncodeToString(headerless[:]) {
			found = true
		}
	}
	if !found {
		t.Error("no variant skips the 16-byte iNES header; every NES ROM would go unidentified")
	}
}

// TestHashVariantsOnlyTriesCopierHeaderWhenSizeImpliesOne keeps the extra
// lookup honest: a 512-byte offset into a headerless ROM is noise, and asking
// the database about it costs a query per file for nothing.
func TestHashVariantsOnlyTriesCopierHeaderWhenSizeImpliesOne(t *testing.T) {
	dir := t.TempDir()

	plain := filepath.Join(dir, "Plain.sfc")
	if err := os.WriteFile(plain, make([]byte, 64*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := len(hashFileMD5Variants(plain, 64*1024)); got != 2 {
		t.Errorf("a headerless-sized ROM produced %d variants, want 2 (whole file + iNES offset)", got)
	}

	copier := filepath.Join(dir, "Copier.sfc")
	size := int64(64*1024 + 512)
	if err := os.WriteFile(copier, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := len(hashFileMD5Variants(copier, size)); got != 3 {
		t.Errorf("a copier-header-sized ROM produced %d variants, want 3", got)
	}
}

// TestHashVariantsSkipsArchivesAndOversizedFiles pins the two deliberate
// exclusions, both of which exist to keep a startup scan affordable.
func TestHashVariantsSkipsArchivesAndOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	zip := filepath.Join(dir, "Game.zip")
	if err := os.WriteFile(zip, make([]byte, 40*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if v := hashFileMD5Variants(zip, 40*1024); v != nil {
		t.Error("an archive was hashed; the database records the hash of the ROM inside it")
	}
	if v := hashFileMD5Variants(zip, maxHashableSize+1); v != nil {
		t.Error("an oversized file was hashed")
	}
}
