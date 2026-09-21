package download

import (
	"archive/zip"
	"crypto/md5"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxHashableSize caps what is hashed during a scan.
//
// MD5 has to read the whole file, so hashing a 700 MB disc image costs real
// seconds and the scan runs at startup. OpenVGDB's coverage is overwhelmingly
// cartridge-era anyway - the systems whose filenames are worst and whose files
// are smallest - so the cap buys most of the benefit for almost none of the
// cost. Anything larger falls back to title matching, exactly as before.
const maxHashableSize = 256 * 1024 * 1024

// containerExtensions are formats holding a ROM rather than being one.
// Hashing the container never matches: OpenVGDB records the hash of the dump
// inside. Extracting to hash is possible but not worth doing during a startup
// scan, so these fall back to title matching.
//
// Its own list rather than the package's existing `archiveExtensions`, which
// covers .tar/.gz/.bz2/.xz for a different purpose entirely and contains none
// of the formats ROMs actually ship in. Reusing it silently hashed every .zip
// in the library.
var containerExtensions = map[string]bool{
	".zip": true, ".7z": true, ".rar": true,
	".tar": true, ".gz": true, ".bz2": true, ".xz": true,
}

// headerOffsets are the leading byte counts a dump may carry that the hash in
// the database does not include.
//
// This is not a refinement, it is the difference between working and not.
// OpenVGDB's SYSTEMS table declares systemHeaderSizeBytes = 16 for NES, meaning
// its hashes are of the ROM *after* the 16-byte iNES header - so a whole-file
// hash of a .nes matches nothing, ever. Measured on a real library: 0 of 1,605
// NES ROMs identified before this, because every one of them has that header.
//
// 512 covers the SNES/Genesis copier header, which OpenVGDB does not declare
// but which real dumps frequently carry; it is only tried when the file size
// implies one, since a 512-byte offset into a headerless ROM is just noise.
var headerOffsets = []int64{0, 16, 512}

// plausibleOffsets narrows the header offsets worth trying for a given size.
func plausibleOffsets(size int64) []int64 {
	offsets := make([]int64, 0, len(headerOffsets))
	for _, off := range headerOffsets {
		if off >= size {
			continue
		}
		// A 512-byte copier header shows up as a ROM whose size is a whole
		// number of KB plus 512. Trying it on anything else only wastes a
		// lookup on a hash that cannot be in the database.
		if off == 512 && size%1024 != 512 {
			continue
		}
		offsets = append(offsets, off)
	}
	return offsets
}

// hashFileMD5Variants returns the MD5 of the file's contents at each plausible
// header offset, in preference order, all from a single read.
//
// Several hashes rather than one because the caller cannot know which is right
// without asking the database: the header may or may not be present in any
// given dump of the same game.
func hashFileMD5Variants(path string, size int64) []string {
	if size <= 0 || size > maxHashableSize {
		return nil
	}
	lower := strings.ToLower(path)
	for ext := range containerExtensions {
		if strings.HasSuffix(lower, ext) {
			return nil
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return hashStreamVariants(f, size)
}

// hashStreamVariants hashes one stream at every plausible header offset, in a
// single pass. Split out so a file on disk and an entry inside an archive go
// through identical logic rather than two copies that can drift.
func hashStreamVariants(r io.Reader, size int64) []string {
	offsets := plausibleOffsets(size)
	if len(offsets) == 0 {
		return nil
	}

	// One pass feeding every hasher, each ignoring its own leading bytes.
	// Reading the source once per offset would triple the cost of the slowest
	// part of a scan.
	hashers := make([]hash.Hash, len(offsets))
	for i := range offsets {
		hashers[i] = md5.New()
	}

	buf := make([]byte, 1<<20)
	var consumed int64
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			for i, off := range offsets {
				switch {
				case consumed >= off:
					hashers[i].Write(chunk)
				case consumed+int64(n) > off:
					hashers[i].Write(chunk[off-consumed:])
				}
			}
			consumed += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}
	}

	out := make([]string, 0, len(hashers))
	for _, h := range hashers {
		out = append(out, hex.EncodeToString(h.Sum(nil)))
	}
	return out
}

// hashFileMD5 returns the whole-file MD5, which is what gets stored so a later
// pass can tell "not hashed" from "hashed and unrecognised".
func hashFileMD5(path string, size int64) string {
	variants := hashFileMD5Variants(path, size)
	if len(variants) == 0 {
		return ""
	}
	return variants[0]
}

// hashZipEntryVariants identifies a ROM stored inside a zip.
//
// This is where most of the library lives: 3,646 of the 4,444 unhashed items
// were .zip, and those are disproportionately the files whose names are worst -
// exactly the ones title matching fails on. Hashing the container is useless
// (OpenVGDB records the hash of the dump inside), so the entry is decompressed
// through the hashers instead.
//
// Nothing is written to disk. The entry is streamed straight into the same
// multi-offset hashers used for a bare file, so the cost is decompression and
// nothing else.
//
// Zip only, deliberately. Go's standard library covers it with no new
// dependency, while .rar and .7z together account for 351 files and would each
// need a third-party decoder - not a trade worth making for under a tenth of
// the remainder.
func hashZipEntryVariants(path string, size int64) []string {
	if size <= 0 || !strings.HasSuffix(strings.ToLower(path), ".zip") {
		return nil
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil
	}
	defer zr.Close()

	entry := pickROMEntry(zr.File)
	if entry == nil {
		return nil
	}
	uncompressed := int64(entry.UncompressedSize64)
	if uncompressed <= 0 || uncompressed > maxHashableSize {
		return nil
	}

	rc, err := entry.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	return hashStreamVariants(rc, uncompressed)
}

// pickROMEntry chooses the dump inside an archive.
//
// Preference, then size: a ROM set commonly zips the dump alongside a text
// file or a cue sheet, and the largest entry is not reliably the right one
// when a scan sheet or manual scan is bundled in.
func pickROMEntry(files []*zip.File) *zip.File {
	var best *zip.File
	var bestPreferred bool
	for _, f := range files {
		if f.FileInfo().IsDir() || strings.HasPrefix(f.Name, "__MACOSX") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(f.Name))
		preferred := gameExtensions[ext] && !containerExtensions[ext]
		switch {
		case best == nil:
			best, bestPreferred = f, preferred
		case preferred && !bestPreferred:
			best, bestPreferred = f, true
		case preferred == bestPreferred && f.UncompressedSize64 > best.UncompressedSize64:
			best = f
		}
	}
	return best
}
