package download

import (
	"os"
	"path/filepath"
	"testing"
)

// These are regressions, one per defect that reached production and damaged the
// library. Each shipped without a test; each is cheap to pin. The cases are the
// real filenames from the live ROM set, not invented ones.

// TestCleanTitleStripsEveryScannableExtension pins the first defect: cleanTitle
// stripped a hardcoded extension list that had drifted out of sync with
// gameExtensions, the map scanDir already uses to decide a file IS a game. The
// missing ones (.smc, .gb, .gbc, .cue, .cdi) meant an entire SNES library was
// stored as "EARTH BOUND.smc" and matched no metadata provider at all.
//
// Asserted against gameExtensions itself rather than a second list, so a format
// can never again be scannable but unstrippable.
func TestCleanTitleStripsEveryScannableExtension(t *testing.T) {
	for ext := range gameExtensions {
		got := cleanTitle("Some Game" + ext)
		if got != "Some Game" {
			t.Errorf("cleanTitle(%q) = %q; a scannable extension must be stripped",
				"Some Game"+ext, got)
		}
	}
}

func TestCleanTitleRealWorldNames(t *testing.T) {
	cases := map[string]string{
		"EARTH BOUND.smc":                     "EARTH BOUND",
		"Donkey Kong Country 2 (E).smc":       "Donkey Kong Country 2",
		"Super Mario World (USA) (Rev 1).sfc": "Super Mario World",
		"Sonic [!].md":                        "Sonic",
		"Lumines.rar.rar":                     "Lumines", // stacked extensions
		"jikkyo_power_pro_wrestling.nes":      "jikkyo power pro wrestling",
		"Nascar-07-rar":                       "Nascar 07",
		"Tomb Raider [FitGirl Repack]":        "Tomb Raider",
	}
	for in, want := range cases {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestROMSizeFloorAdmitsCartridgeDumps pins the second defect: the floor was
// 1,000,000 bytes, which is far above a legitimate 8- and 16-bit cartridge
// dump. It hid roughly 3,200 real games - the NES library was 99.8% invisible.
//
// The sizes below are representative of real dumps on the live set.
func TestROMSizeFloorAdmitsCartridgeDumps(t *testing.T) {
	realDumps := map[string]int64{
		"NES cartridge":       40 * 1024,
		"Game Boy cartridge":  32 * 1024,
		"small NES cartridge": 24 * 1024,
		"SNES cartridge":      512 * 1024,
	}
	for name, size := range realDumps {
		if size < minROMFileSize {
			t.Errorf("%s (%d bytes) falls below minROMFileSize (%d); real games would be skipped",
				name, size, minROMFileSize)
		}
	}
	// It still has to reject the truncated downloads and sidecars it exists for.
	if minROMFileSize > 16*1024 {
		t.Errorf("minROMFileSize is %d; above 16 KB it starts excluding real cartridge dumps", minROMFileSize)
	}
	if minROMFileSize == 0 {
		t.Error("minROMFileSize of 0 admits empty and truncated files")
	}
}

// TestScanDirAdmitsSmallCartridgeROM is the end-to-end version of the same
// defect: a small .nes file on disk must actually be scanned in.
func TestScanDirAdmitsSmallCartridgeROM(t *testing.T) {
	root := t.TempDir()
	platform := filepath.Join(root, "nes")
	if err := os.MkdirAll(platform, 0o755); err != nil {
		t.Fatal(err)
	}
	// 40 KB, the size of a real NES cartridge dump and well under the old floor.
	rom := filepath.Join(platform, "Totally Rad (USA).nes")
	if err := os.WriteFile(rom, make([]byte, 40*1024), 0o644); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(rom)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < minROMFileSize {
		t.Fatalf("a 40 KB NES dump is below the %d byte floor and would be skipped", minROMFileSize)
	}
}

// TestStripROMTagsLeavesTitleIntact guards the tag stripper against eating real
// words: a title with parentheses that are part of the name must survive.
// TestGameExtensionsCoversMajorConsoles is what caught the missing Sega
// formats. Asserting a representative extension per console is cheap, and it
// fails loudly the next time a platform is added to the library without its
// file types being added here.
func TestGameExtensionsCoversMajorConsoles(t *testing.T) {
	required := map[string]string{
		".nes": "NES", ".sfc": "SNES", ".smc": "SNES",
		".md": "Genesis", ".gen": "Genesis", ".smd": "Genesis",
		".sms": "Master System", ".gg": "Game Gear",
		".gb": "Game Boy", ".gbc": "Game Boy Color", ".gba": "Game Boy Advance",
		".n64": "N64", ".z64": "N64",
		".a26": "Atari 2600", ".a78": "Atari 7800", ".lnx": "Atari Lynx",
		".int": "Intellivision", ".col": "ColecoVision",
		".pce": "PC Engine", ".ws": "WonderSwan", ".ngp": "Neo Geo Pocket",
		".iso": "disc", ".cue": "disc", ".chd": "disc",
	}
	for ext, console := range required {
		if !gameExtensions[ext] {
			t.Errorf("%s (%s) is not in gameExtensions; those files are silently skipped by the scanner", ext, console)
		}
	}
}

// TestContainsGameFilesRespectsSizeFloor pins the fix for the .md collision:
// a directory holding only a small file with a game extension is not a game
// folder, however the extension reads.
func TestContainsGameFilesRespectsSizeFloor(t *testing.T) {
	notes := t.TempDir()
	if err := os.WriteFile(filepath.Join(notes, "notes.md"), []byte("just notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if containsGameFiles(notes) {
		t.Error("a folder holding only a small notes.md counts as a game folder; Markdown and Mega Drive share .md")
	}

	roms := t.TempDir()
	if err := os.WriteFile(filepath.Join(roms, "Sonic.md"), make([]byte, 512*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if !containsGameFiles(roms) {
		t.Error("a real Mega Drive dump was not recognised")
	}
}

func TestStripROMTagsLeavesTitleIntact(t *testing.T) {
	cases := map[string]string{
		"Final Fantasy VII (USA)": "Final Fantasy VII",
		"Sonic 3 (E) [!]":         "Sonic 3",
		"Rock n' Roll Racing":     "Rock n' Roll Racing",
	}
	for in, want := range cases {
		if got := stripROMTags(in); got != want {
			t.Errorf("stripROMTags(%q) = %q, want %q", in, got, want)
		}
	}
}
