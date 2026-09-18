package download

import (
	"testing"
)

func TestCleanTitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"strip zip extension", "Game.zip", "Game"},
		{"strip nsp extension", "Zelda.nsp", "Zelda"},
		{"strip rar extension", "Game.rar", "Game"},
		{"strip 7z extension", "Game.7z", "Game"},
		{"strip iso extension", "Game.iso", "Game"},
		{"URL decode spaces", "Super%20Mario%20Bros", "Super Mario Bros"},
		// Changed deliberately: the decoded "(USA)" is a ROM-set region tag,
		// and leaving it on the title is what stopped these matching upstream.
		{"URL decode parens, then strip the region tag", "Game%28USA%29", "Game"},
		{"URL decode comma", "Game%2C Part 2", "Game, Part 2"},
		{"trim whitespace", "  Game  ", "Game"},
		{"no extension to strip", "Plain Game", "Plain Game"},
		{"case insensitive ext", "Game.ZIP", "Game"},

		// Extensions the previous hardcoded list omitted. Each of these kept
		// its extension in the stored title, and none of them matched anything
		// upstream (verified live against IGDB).
		{"snes", "EARTH BOUND.smc", "EARTH BOUND"},
		{"game boy", "Final Fantasy Adventure.gb", "Final Fantasy Adventure"},
		{"game boy color", "Game.gbc", "Game"},
		{"cue sheet", "Game.cue", "Game"},
		{"dreamcast", "Game.cdi", "Game"},

		// Region / dump / revision tags.
		{"region tag with extension", "Donkey Kong Country 2 (E).smc", "Donkey Kong Country 2"},
		{"us region tag", "ESPN Sunday Night NFL (US).smc", "ESPN Sunday Night NFL"},
		{"dump marker", "Super Mario World (U) [!].sfc", "Super Mario World"},
		{"revision tag", "Chrono Trigger (USA) (Rev 1).sfc", "Chrono Trigger"},

		// Stacked extension stripped, then the underscore normalized because
		// the remaining name has no spaces of its own.
		{"double archive extension", "RetroArch_data.tar.gz", "RetroArch data"},
		// Real files in this library are named exactly like this.
		{"doubled extension", "Lumines.rar.rar", "Lumines"},
		{"a non-extension suffix stops the loop", "Spiderman.2.rar", "Spiderman.2"},

		// Separator normalization, only when the name has no spaces of its own.
		{"underscore separated", "jikkyo_power_pro_wrestling", "jikkyo power pro wrestling"},
		{"dash separated with a trailing rar word", "Nascar-07-rar", "Nascar 07"},
		{"underscore separated with a trailing rar word", "Ape_Academy_2_rar", "Ape Academy 2"},
		{"catalogue number prefix", "0663 - Barbie in the 12 Dancing Princesses", "Barbie in the 12 Dancing Princesses"},
		{"spaced titles keep their hyphens", "Spider-Man - The Movie", "Spider-Man - The Movie"},
		{"a title that is only a separator word survives", "rar", "rar"},

		// Must not be mangled.
		{"unknown extension stays", "My Game.documentary", "My Game.documentary"},
		{"dots inside the title survive", "E.V.O. Search for Eden", "E.V.O. Search for Eden"},
		{"a title that is only a tag is kept", "(Unknown)", "(Unknown)"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanTitle(tt.in)
			if got != tt.want {
				t.Errorf("cleanTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTitlesMatch(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		torrentName string
		want        bool
	}{
		{"exact", "Super Game", "Super Game", true},
		{"case insensitive", "Super Game", "SUPER game", true},
		{"torrent renamed with suffix", "Super Game", "Super Game (USA) [Repack]", true},
		{"title contains torrent name", "Super Game Deluxe Edition", "super game deluxe", true},
		{"unrelated", "Super Game", "Other Thing", false},
		{"empty title never matches", "", "Anything", false},
		{"empty torrent name never matches", "Super Game", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := titlesMatch(tt.title, tt.torrentName); got != tt.want {
				t.Errorf("titlesMatch(%q, %q) = %v, want %v", tt.title, tt.torrentName, got, tt.want)
			}
		})
	}
}

func TestJobMatchesTorrent(t *testing.T) {
	const terraria = "Terraria (v1.4.5.0 + Bonus OST, MULTi9) [FitGirl Repack]"

	tests := []struct {
		name                     string
		infoHash, title          string
		torrentHash, torrentName string
		want                     bool
	}{
		{"hash matches a renamed torrent", "abc123", terraria, "abc123", "Terraria [FitGirl Repack]", true},
		{"hash is case insensitive", "ABC123", terraria, "abc123", "Terraria [FitGirl Repack]", true},
		{"hash mismatch is not rescued by the title", "abc123", "Super Game", "def456", "Super Game", false},
		{"hashless job falls back to the title", "", "Super Game", "abc123", "Super Game (USA)", true},
		{"hashless job with an unrelated title", "", "Super Game", "abc123", "Other Thing", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := JobMatchesTorrent(tt.infoHash, tt.title, tt.torrentHash, tt.torrentName)
			if got != tt.want {
				t.Errorf("JobMatchesTorrent(%q, %q, %q, %q) = %v, want %v",
					tt.infoHash, tt.title, tt.torrentHash, tt.torrentName, got, tt.want)
			}
		})
	}
}

func TestPlatformNameFromSlug(t *testing.T) {
	tests := []struct {
		slug string
		want string
	}{
		{"gba", "Game Boy Advance"},
		{"nes", "NES"},
		{"switch", "Switch"},
		{"psx", "PS1"},
		{"ngc", "GameCube"},
		{"unknown", "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.slug, func(t *testing.T) {
			got := platformNameFromSlug(tt.slug)
			if got != tt.want {
				t.Errorf("platformNameFromSlug(%q) = %q, want %q", tt.slug, got, tt.want)
			}
		})
	}
}

func TestGameExtensions(t *testing.T) {
	expected := []string{".nsp", ".xci", ".nes", ".gba", ".iso", ".zip", ".exe"}
	for _, ext := range expected {
		if !gameExtensions[ext] {
			t.Errorf("expected %q in gameExtensions", ext)
		}
	}

	notExpected := []string{".txt", ".pdf", ".mp3", ".jpg"}
	for _, ext := range notExpected {
		if gameExtensions[ext] {
			t.Errorf("expected %q NOT in gameExtensions", ext)
		}
	}
}

// Every extension the scanner accepts as a game must also be strippable. A
// format that is scannable but not strippable is precisely the bug that left
// thousands of ROMs with ".smc" in their title, so this pins the two together
// rather than trusting a second hand-maintained list to keep up.
func TestEveryGameExtensionIsStripped(t *testing.T) {
	for ext := range gameExtensions {
		if got := cleanTitle("Some Game" + ext); got != "Some Game" {
			t.Errorf("cleanTitle(%q) = %q, want %q", "Some Game"+ext, got, "Some Game")
		}
	}
}
