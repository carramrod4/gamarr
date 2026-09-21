package download

import (
	"archive/zip"
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

// TestHashZipEntryMatchesTheBareROM is the property that makes archive hashing
// worth doing at all: a ROM inside a zip must produce the same hashes as the
// same ROM on disk, or it would never match a database keyed on the dump.
func TestHashZipEntryMatchesTheBareROM(t *testing.T) {
	body := make([]byte, 40*1024)
	for i := range body {
		body[i] = byte(i % 251)
	}
	header := append([]byte("NES\x1a"), make([]byte, 12)...)
	rom := append(header, body...)

	dir := t.TempDir()
	bare := filepath.Join(dir, "Game.nes")
	if err := os.WriteFile(bare, rom, 0o644); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(dir, "Game.zip")
	zf, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	// A sidecar alongside the dump, which real ROM sets routinely include.
	notes, _ := zw.Create("readme.txt")
	notes.Write([]byte("scan notes"))
	w, err := zw.Create("Game.nes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(rom); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	zf.Close()

	info, _ := os.Stat(archive)
	fromZip := hashZipEntryVariants(archive, info.Size())
	fromDisk := hashFileMD5Variants(bare, int64(len(rom)))

	if len(fromZip) == 0 {
		t.Fatal("no hashes produced from the archive")
	}
	if len(fromZip) != len(fromDisk) {
		t.Fatalf("archive produced %d variants, bare file %d", len(fromZip), len(fromDisk))
	}
	for i := range fromDisk {
		if fromZip[i] != fromDisk[i] {
			t.Errorf("variant %d differs: zip %s, bare %s", i, fromZip[i], fromDisk[i])
		}
	}
}

// TestPickROMEntryPrefersTheDumpOverASidecar pins the selection rule: the
// largest entry is not reliably the right one when a scan sheet is bundled in.
func TestPickROMEntryPrefersTheDumpOverASidecar(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "Game.zip")
	zf, _ := os.Create(archive)
	zw := zip.NewWriter(zf)
	// The sidecar is deliberately the larger entry.
	big, _ := zw.Create("manual scan.txt")
	big.Write(make([]byte, 80*1024))
	small, _ := zw.Create("Game.nes")
	small.Write(make([]byte, 40*1024))
	zw.Close()
	zf.Close()

	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	entry := pickROMEntry(zr.File)
	if entry == nil || entry.Name != "Game.nes" {
		got := "<nil>"
		if entry != nil {
			got = entry.Name
		}
		t.Errorf("picked %s, want Game.nes: a game extension outranks a bigger sidecar", got)
	}
}
