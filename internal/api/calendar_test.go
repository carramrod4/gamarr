package api

import (
	"reflect"
	"testing"
)

func TestFilterByPlatform(t *testing.T) {
	entries := []calendarEntry{
		{Name: "A", Platforms: []string{"PC", "PlayStation 5"}},
		{Name: "B", Platforms: []string{"Nintendo Switch"}},
		{Name: "C", Platforms: nil},
	}

	tests := []struct {
		name     string
		platform string
		want     []string
	}{
		{"empty filter keeps everything", "", []string{"A", "B", "C"}},
		{"all keeps everything", "all", []string{"A", "B", "C"}},
		{"exact name", "Nintendo Switch", []string{"B"}},
		{"name is case-insensitive", "nintendo switch", []string{"B"}},
		// The UI sends slugs, providers return display names.
		{"slugified name", "nintendoswitch", []string{"B"}},
		{"matches any of several platforms", "PlayStation 5", []string{"A"}},
		{"no match yields nothing", "Dreamcast", nil},
		{"an entry with no platforms never matches", "PC", []string{"A"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := namesOf(filterByPlatform(entries, tt.platform))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func namesOf(entries []calendarEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

func TestSlugify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Nintendo Switch", "nintendoswitch"},
		{"PlayStation 5", "playstation5"},
		{"Sega Mega Drive/Genesis", "segamegadrivegenesis"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := slugify(tt.in); got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDecodeItemMetadata(t *testing.T) {
	tests := []struct {
		name        string
		blob        string
		wantOK      bool
		wantSources map[string]string
	}{
		{name: "empty string", blob: "", wantOK: false},
		{name: "empty object", blob: "{}", wantOK: false},
		{name: "malformed JSON is not fatal", blob: `{"title":`, wantOK: false},
		{name: "non-object JSON", blob: `["a"]`, wantOK: false},
		{
			name:   "plain metadata with no attribution",
			blob:   `{"name":"Chrono Trigger","rating":4.6}`,
			wantOK: true,
		},
		{
			name:        "attribution is lifted out",
			blob:        `{"name":"Chrono Trigger","sources":{"title":"igdb","rating":"rawg"}}`,
			wantOK:      true,
			wantSources: map[string]string{"title": "igdb", "rating": "rawg"},
		},
		{
			// A blob written by an older build could hold anything; a
			// non-string source value must be skipped, not crash or appear.
			name:        "non-string source values are skipped",
			blob:        `{"sources":{"title":"igdb","rating":42,"cover_art":null}}`,
			wantOK:      true,
			wantSources: map[string]string{"title": "igdb"},
		},
		{
			name:   "sources of the wrong type is ignored",
			blob:   `{"name":"x","sources":"igdb"}`,
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, sources, ok := decodeItemMetadata(tt.blob)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				if meta != nil {
					t.Error("expected no metadata on failure")
				}
				return
			}
			if len(tt.wantSources) == 0 {
				if len(sources) != 0 {
					t.Errorf("sources = %v, want none", sources)
				}
				return
			}
			if !reflect.DeepEqual(sources, tt.wantSources) {
				t.Errorf("sources = %v, want %v", sources, tt.wantSources)
			}
		})
	}
}
