package download

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	"os"
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

// An archive is a container, not a dump: hashing it would never match,
// because OpenVGDB records the hash of the ROM inside. Extracting to hash is
// possible but not worth doing during a startup scan, so archives fall back to
// title matching. `archiveExtensions` is the list already used for extraction.

// hashFileMD5 returns the lowercase MD5 of a file's contents, or "" when the
// file is not worth hashing. Callers treat "" as "unknown", never as an error.
func hashFileMD5(path string, size int64) string {
	if size <= 0 || size > maxHashableSize {
		return ""
	}
	lower := strings.ToLower(path)
	for ext := range archiveExtensions {
		if strings.HasSuffix(lower, ext) {
			return ""
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
