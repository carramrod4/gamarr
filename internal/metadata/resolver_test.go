package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// ── merge logic ──────────────────────────────────────────────────────────────

func TestGameMergeFirstNonEmptyWins(t *testing.T) {
	igdb := &Game{
		Title:       "Chrono Trigger",
		Platforms:   []string{"SNES"},
		ReleaseDate: "1995-03-11",
		CoverArt:    "https://igdb.test/ct.jpg",
		Rating:      92,
	}
	rawg := &Game{
		Title:       "Chrono Trigger",
		Platforms:   []string{"SNES", "Nintendo DS"},
		ReleaseDate: "1995-08-22",
		CoverArt:    "https://rawg.test/ct.jpg",
		Description: "A time-travelling RPG.",
		Rating:      88,
	}
	steam := &Game{
		Title:       "Chrono Trigger",
		Platforms:   []string{"PC"},
		Description: "Steam's copy.",
		Metacritic:  92,
	}

	tests := []struct {
		name  string
		merge []struct {
			game *Game
			from string
		}
		want        Game
		wantSources map[string]string
	}{
		{
			name: "single provider supplies everything it has",
			merge: []struct {
				game *Game
				from string
			}{{igdb, providerIGDB}},
			want: Game{
				Title:       "Chrono Trigger",
				Platforms:   []string{"SNES"},
				ReleaseDate: "1995-03-11",
				CoverArt:    "https://igdb.test/ct.jpg",
				Rating:      92,
			},
			wantSources: map[string]string{
				FieldTitle:       providerIGDB,
				FieldPlatforms:   providerIGDB,
				FieldReleaseDate: providerIGDB,
				FieldCoverArt:    providerIGDB,
				FieldRating:      providerIGDB,
			},
		},
		{
			name: "higher priority wins contested fields, lower fills gaps",
			merge: []struct {
				game *Game
				from string
			}{{igdb, providerIGDB}, {rawg, providerRAWG}},
			want: Game{
				Title:       "Chrono Trigger",
				Platforms:   []string{"SNES"},
				ReleaseDate: "1995-03-11",
				CoverArt:    "https://igdb.test/ct.jpg",
				Description: "A time-travelling RPG.", // only RAWG had one
				Rating:      92,
			},
			wantSources: map[string]string{
				FieldTitle:       providerIGDB,
				FieldPlatforms:   providerIGDB,
				FieldReleaseDate: providerIGDB,
				FieldCoverArt:    providerIGDB,
				FieldDescription: providerRAWG,
				FieldRating:      providerIGDB,
			},
		},
		{
			name: "merge order determines the winner, not provider identity",
			merge: []struct {
				game *Game
				from string
			}{{rawg, providerRAWG}, {igdb, providerIGDB}},
			want: Game{
				Title:       "Chrono Trigger",
				Platforms:   []string{"SNES", "Nintendo DS"},
				ReleaseDate: "1995-08-22",
				CoverArt:    "https://rawg.test/ct.jpg",
				Description: "A time-travelling RPG.",
				Rating:      88,
			},
			wantSources: map[string]string{
				FieldTitle:       providerRAWG,
				FieldPlatforms:   providerRAWG,
				FieldReleaseDate: providerRAWG,
				FieldCoverArt:    providerRAWG,
				FieldDescription: providerRAWG,
				FieldRating:      providerRAWG,
			},
		},
		{
			name: "three providers, each contributing something different",
			merge: []struct {
				game *Game
				from string
			}{{steam, providerSteam}, {igdb, providerIGDB}, {rawg, providerRAWG}},
			want: Game{
				Title:       "Chrono Trigger",
				Platforms:   []string{"PC"},
				ReleaseDate: "1995-03-11", // steam had none, igdb won
				CoverArt:    "https://igdb.test/ct.jpg",
				Description: "Steam's copy.",
				Rating:      92,
				Metacritic:  92,
			},
			wantSources: map[string]string{
				FieldTitle:       providerSteam,
				FieldPlatforms:   providerSteam,
				FieldReleaseDate: providerIGDB,
				FieldCoverArt:    providerIGDB,
				FieldDescription: providerSteam,
				FieldRating:      providerIGDB,
			},
		},
		{
			name: "merging nil is a no-op",
			merge: []struct {
				game *Game
				from string
			}{{igdb, providerIGDB}, {nil, providerRAWG}},
			want: Game{
				Title:       "Chrono Trigger",
				Platforms:   []string{"SNES"},
				ReleaseDate: "1995-03-11",
				CoverArt:    "https://igdb.test/ct.jpg",
				Rating:      92,
			},
			wantSources: map[string]string{
				FieldTitle:       providerIGDB,
				FieldPlatforms:   providerIGDB,
				FieldReleaseDate: providerIGDB,
				FieldCoverArt:    providerIGDB,
				FieldRating:      providerIGDB,
			},
		},
		{
			name: "re-merging the same source is idempotent",
			merge: []struct {
				game *Game
				from string
			}{{igdb, providerIGDB}, {igdb, providerIGDB}},
			want: Game{
				Title:       "Chrono Trigger",
				Platforms:   []string{"SNES"},
				ReleaseDate: "1995-03-11",
				CoverArt:    "https://igdb.test/ct.jpg",
				Rating:      92,
			},
			wantSources: map[string]string{
				FieldTitle:       providerIGDB,
				FieldPlatforms:   providerIGDB,
				FieldReleaseDate: providerIGDB,
				FieldCoverArt:    providerIGDB,
				FieldRating:      providerIGDB,
			},
		},
		{
			name: "empty source contributes nothing and attributes nothing",
			merge: []struct {
				game *Game
				from string
			}{{&Game{}, providerIGDB}, {rawg, providerRAWG}},
			want: Game{
				Title:       "Chrono Trigger",
				Platforms:   []string{"SNES", "Nintendo DS"},
				ReleaseDate: "1995-08-22",
				CoverArt:    "https://rawg.test/ct.jpg",
				Description: "A time-travelling RPG.",
				Rating:      88,
			},
			wantSources: map[string]string{
				FieldTitle:       providerRAWG,
				FieldPlatforms:   providerRAWG,
				FieldReleaseDate: providerRAWG,
				FieldCoverArt:    providerRAWG,
				FieldDescription: providerRAWG,
				FieldRating:      providerRAWG,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := &Game{}
			for _, step := range tc.merge {
				got.Merge(step.game, step.from)
			}

			if got.Title != tc.want.Title {
				t.Errorf("Title = %q, want %q", got.Title, tc.want.Title)
			}
			if !reflect.DeepEqual(got.Platforms, tc.want.Platforms) {
				t.Errorf("Platforms = %v, want %v", got.Platforms, tc.want.Platforms)
			}
			if got.ReleaseDate != tc.want.ReleaseDate {
				t.Errorf("ReleaseDate = %q, want %q", got.ReleaseDate, tc.want.ReleaseDate)
			}
			if got.CoverArt != tc.want.CoverArt {
				t.Errorf("CoverArt = %q, want %q", got.CoverArt, tc.want.CoverArt)
			}
			if got.Description != tc.want.Description {
				t.Errorf("Description = %q, want %q", got.Description, tc.want.Description)
			}
			if got.Rating != tc.want.Rating {
				t.Errorf("Rating = %v, want %v", got.Rating, tc.want.Rating)
			}
			if got.Metacritic != tc.want.Metacritic {
				t.Errorf("Metacritic = %d, want %d", got.Metacritic, tc.want.Metacritic)
			}
			if !reflect.DeepEqual(got.Sources, tc.wantSources) {
				t.Errorf("Sources = %v, want %v", got.Sources, tc.wantSources)
			}
		})
	}
}

func TestGameMergeDoesNotAliasSourceSlices(t *testing.T) {
	src := &Game{Platforms: []string{"SNES"}, Genres: []string{"RPG"}}
	dst := &Game{}
	dst.Merge(src, providerIGDB)

	// Mutating the merged record must not write back into the provider's slice.
	dst.Platforms[0] = "MUTATED"
	dst.Genres[0] = "MUTATED"

	if src.Platforms[0] != "SNES" {
		t.Errorf("source Platforms aliased: got %q", src.Platforms[0])
	}
	if src.Genres[0] != "RPG" {
		t.Errorf("source Genres aliased: got %q", src.Genres[0])
	}
}

func TestGameMergeCollectsProviderIDs(t *testing.T) {
	dst := &Game{}
	dst.Merge(&Game{Title: "A", ProviderIDs: map[string]string{providerIGDB: "1"}}, providerIGDB)
	dst.Merge(&Game{Title: "A", ProviderIDs: map[string]string{providerRAWG: "2"}}, providerRAWG)
	dst.Merge(&Game{Title: "A", ProviderIDs: map[string]string{providerSteam: "3"}}, providerSteam)

	want := map[string]string{providerIGDB: "1", providerRAWG: "2", providerSteam: "3"}
	if !reflect.DeepEqual(dst.ProviderIDs, want) {
		t.Errorf("ProviderIDs = %v, want %v", dst.ProviderIDs, want)
	}
}

func TestRatingScaleRoundTrip(t *testing.T) {
	tests := []struct {
		name         string
		legacyRating float64 // RAWG's native 0-5
		wantCanon    float64 // canonical 0-100
	}{
		{"zero stays zero", 0, 0},
		{"mid scale", 2.5, 50},
		{"full marks", 5, 100},
		{"typical value", 4.6, 92},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := fromGameMetadata(&GameMetadata{Rating: tc.legacyRating}, providerRAWG)
			if g.Rating != tc.wantCanon {
				t.Errorf("canonical rating = %v, want %v", g.Rating, tc.wantCanon)
			}
			back := g.ToGameMetadata()
			if back.Rating != tc.legacyRating {
				t.Errorf("round-tripped legacy rating = %v, want %v", back.Rating, tc.legacyRating)
			}
		})
	}
}

func TestToGameMetadataOnlyCarriesRAWGID(t *testing.T) {
	tests := []struct {
		name   string
		ids    map[string]string
		wantID int
	}{
		{"rawg id is surfaced", map[string]string{providerRAWG: "123"}, 123},
		{"igdb id is not a rawg id", map[string]string{providerIGDB: "456"}, 0},
		{"steam appid is not a rawg id", map[string]string{providerSteam: "789"}, 0},
		{"rawg wins when several are present", map[string]string{providerIGDB: "1", providerRAWG: "2"}, 2},
		{"no ids at all", nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &Game{Title: "X", ProviderIDs: tc.ids}
			if got := g.ToGameMetadata().ID; got != tc.wantID {
				t.Errorf("ID = %d, want %d", got, tc.wantID)
			}
		})
	}
}

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Chrono Trigger", "chrono trigger"},
		{"chrono  trigger", "chrono trigger"},
		{"Chrono-Trigger", "chrono trigger"},
		{"Chrono Trigger:", "chrono trigger"},
		{"  Chrono Trigger  ", "chrono trigger"},
		{"Half-Life 2: Episode One", "half life 2 episode one"},
		{"DOOM (2016)", "doom 2016"},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := normalizeTitle(tc.in); got != tc.want {
				t.Errorf("normalizeTitle(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestExactTitleMatch covers the real upstream-ranking cases both providers
// hit: IGDB genuinely returns "Portal 2: In Motion" ahead of "Portal 2"
// (verified against the live API), so blindly taking result 0 picks the wrong
// game.
func TestExactTitleMatch(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		titles []string
		want   int
	}{
		{
			name:   "live IGDB ordering for Portal 2",
			query:  "Portal 2",
			titles: []string{"Portal 2: In Motion", "Portal 2", "Portal 2: Community Edition"},
			want:   1,
		},
		{
			name:   "exact match already first",
			query:  "Portal 2",
			titles: []string{"Portal 2", "Portal 2: In Motion"},
			want:   0,
		},
		{
			name:   "match ignores case and punctuation",
			query:  "chrono-trigger",
			titles: []string{"Chrono Cross", "Chrono Trigger"},
			want:   1,
		},
		{
			name:   "no exact match reports -1 so caller can fall back",
			query:  "Portal",
			titles: []string{"Portal 2", "Portal Knights"},
			want:   -1,
		},
		{
			name:   "empty query never matches",
			query:  "",
			titles: []string{"Portal 2"},
			want:   -1,
		},
		{
			name:   "empty candidate list",
			query:  "Portal 2",
			titles: nil,
			want:   -1,
		},
		{
			name:   "first of duplicate titles wins",
			query:  "Doom",
			titles: []string{"Doom Eternal", "Doom", "Doom"},
			want:   1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := exactTitleMatch(tc.query, tc.titles); got != tc.want {
				t.Errorf("exactTitleMatch(%q, %v) = %d, want %d", tc.query, tc.titles, got, tc.want)
			}
		})
	}
}

// TestPickIGDBBest covers the two real mis-rankings observed against the live
// IGDB API, both of which made the primary provider answer with the wrong game.
func TestPickIGDBBest(t *testing.T) {
	tests := []struct {
		name  string
		query string
		games []igdbGame
		want  int // index into games
	}{
		{
			name:  "live: spinoff ranked above the real Portal 2",
			query: "Portal 2",
			games: []igdbGame{
				{ID: 99969, Name: "Portal 2: In Motion", GameType: igdbTypeMainGame},
				{ID: 72, Name: "Portal 2", GameType: igdbTypeMainGame},
				{ID: 169962, Name: "Portal 2: Community Edition", GameType: 5},
			},
			want: 1,
		},
		{
			name:  "live: ROM hack ranked above the real Chrono Trigger",
			query: "Chrono Trigger",
			games: []igdbGame{
				{ID: 219077, Name: "Chrono Trigger+", GameType: 5}, // mod
				{ID: 1802, Name: "Chrono Trigger", GameType: igdbTypeMainGame},
				{ID: 38266, Name: "Chrono Trigger: Crimson Echoes", GameType: 5},
			},
			want: 1,
		},
		{
			name:  "a mod that normalizes identically must not win over the main game",
			query: "Chrono Trigger",
			games: []igdbGame{
				{ID: 219077, Name: "Chrono Trigger+", GameType: 5},
				{ID: 1802, Name: "Chrono Trigger", GameType: igdbTypeMainGame},
			},
			want: 1,
		},
		{
			name:  "main game preferred even without an exact title match",
			query: "Chrono",
			games: []igdbGame{
				{ID: 219077, Name: "Chrono Trigger+", GameType: 5},
				{ID: 1802, Name: "Chrono Trigger", GameType: igdbTypeMainGame},
			},
			want: 1,
		},
		{
			name:  "absent game_type decodes to main game",
			query: "Portal 2",
			games: []igdbGame{
				{ID: 72, Name: "Portal 2"}, // GameType zero value
			},
			want: 0,
		},
		{
			name:  "when nothing is a main game, fall back to the first result",
			query: "Crimson Echoes",
			games: []igdbGame{
				{ID: 38266, Name: "Chrono Trigger: Crimson Echoes", GameType: 5},
				{ID: 42332, Name: "Chrono Trigger: Flames of Eternity", GameType: 5},
			},
			want: 0,
		},
		{
			name:  "exact match still wins among mods when no main game exists",
			query: "Chrono Trigger: Flames of Eternity",
			games: []igdbGame{
				{ID: 38266, Name: "Chrono Trigger: Crimson Echoes", GameType: 5},
				{ID: 42332, Name: "Chrono Trigger: Flames of Eternity", GameType: 5},
			},
			want: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pickIGDBBest(tc.query, tc.games)
			if got != tc.want {
				t.Errorf("pickIGDBBest = %d (%q), want %d (%q)",
					got, tc.games[got].Name, tc.want, tc.games[tc.want].Name)
			}
		})
	}
}

// TestNormalizeTitleCollapsesTrailingPunctuation documents the known, deliberate
// property that motivates the game_type filter above: normalization cannot tell
// "Chrono Trigger+" from "Chrono Trigger" on its own.
func TestNormalizeTitleCollapsesTrailingPunctuation(t *testing.T) {
	if normalizeTitle("Chrono Trigger+") != normalizeTitle("Chrono Trigger") {
		t.Skip("normalizeTitle now distinguishes '+'; the game_type preference in pickIGDBBest may be reconsidered")
	}
}

func TestTitlesMatch(t *testing.T) {
	tests := []struct {
		name, a, b string
		want       bool
	}{
		{"identical", "Chrono Trigger", "Chrono Trigger", true},
		{"casing and punctuation differ", "chrono-trigger", "Chrono Trigger", true},
		{"different games", "Doom", "Doom Eternal", false},
		{"sequel is not the same game", "Half-Life", "Half-Life 2", false},
		{"empty left is permissive", "", "Anything", true},
		{"empty right is permissive", "Anything", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := titlesMatch(tc.a, tc.b); got != tc.want {
				t.Errorf("titlesMatch(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestIsComplete(t *testing.T) {
	full := Game{
		Title: "T", Platforms: []string{"PC"}, ReleaseDate: "2020-01-01",
		CoverArt: "u", Description: "d", Rating: 50,
	}
	if !full.isComplete() {
		t.Error("fully-populated game should be complete")
	}

	for _, field := range []string{"title", "platforms", "release", "cover", "description", "rating"} {
		t.Run("missing "+field, func(t *testing.T) {
			g := full
			switch field {
			case "title":
				g.Title = ""
			case "platforms":
				g.Platforms = nil
			case "release":
				g.ReleaseDate = ""
			case "cover":
				g.CoverArt = ""
			case "description":
				g.Description = ""
			case "rating":
				g.Rating = 0
			}
			if g.isComplete() {
				t.Errorf("game missing %s should not be complete", field)
			}
		})
	}
}

// ── resolver behavior ────────────────────────────────────────────────────────

// stubProvider is a Provider whose every response is scripted.
type stubProvider struct {
	name    string
	enabled bool
	game    *Game
	err     error
	mu      sync.Mutex
	calls   int
}

func (s *stubProvider) Name() string  { return s.name }
func (s *stubProvider) Enabled() bool { return s.enabled }

func (s *stubProvider) Search(context.Context, string, string) (*Game, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.game, s.err
}

func (s *stubProvider) GetByID(context.Context, string) (*Game, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.game, s.err
}

func (s *stubProvider) GetCoverArt(_ context.Context, g *Game) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if s.game != nil {
		return s.game.CoverArt, nil
	}
	return "", nil
}

func (s *stubProvider) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// newStubResolver builds a Resolver over scripted providers, bypassing
// NewResolver so no real provider is ever constructed.
func newStubResolver(providers ...Provider) *Resolver {
	return &Resolver{providers: providers}
}

func TestResolverSearchMergesInPriorityOrder(t *testing.T) {
	primary := &stubProvider{name: providerIGDB, enabled: true, game: &Game{
		Title: "Chrono Trigger", Platforms: []string{"SNES"}, Rating: 92,
	}}
	fallback := &stubProvider{name: providerRAWG, enabled: true, game: &Game{
		Title: "Chrono Trigger", ReleaseDate: "1995-03-11", Description: "RPG",
		CoverArt: "https://rawg.test/x.jpg",
	}}

	r := newStubResolver(primary, fallback)
	got, err := r.Search(context.Background(), "chrono trigger", "snes")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Rating != 92 || got.Sources[FieldRating] != providerIGDB {
		t.Errorf("rating should come from primary: %v / %v", got.Rating, got.Sources[FieldRating])
	}
	if got.Description != "RPG" || got.Sources[FieldDescription] != providerRAWG {
		t.Errorf("description should come from fallback: %q / %v", got.Description, got.Sources[FieldDescription])
	}
}

func TestResolverSkipsDisabledProviders(t *testing.T) {
	disabled := &stubProvider{name: providerIGDB, enabled: false, game: &Game{Title: "Never"}}
	enabled := &stubProvider{name: providerRAWG, enabled: true, game: &Game{Title: "Used"}}

	r := newStubResolver(disabled, enabled)
	got, err := r.Search(context.Background(), "q", "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if disabled.callCount() != 0 {
		t.Errorf("disabled provider was called %d times", disabled.callCount())
	}
	if got.Title != "Used" {
		t.Errorf("Title = %q, want %q", got.Title, "Used")
	}
}

func TestResolverSurvivesProviderError(t *testing.T) {
	broken := &stubProvider{name: providerIGDB, enabled: true, err: errors.New("upstream down")}
	working := &stubProvider{name: providerRAWG, enabled: true, game: &Game{Title: "Still Works"}}

	r := newStubResolver(broken, working)
	got, err := r.Search(context.Background(), "q", "")
	if err != nil {
		t.Fatalf("a single failing provider must not fail the resolve: %v", err)
	}
	if got.Title != "Still Works" {
		t.Errorf("Title = %q, want %q", got.Title, "Still Works")
	}
}

func TestResolverReturnsErrorOnlyWhenNothingResolved(t *testing.T) {
	broken := &stubProvider{name: providerIGDB, enabled: true, err: errors.New("boom")}
	empty := &stubProvider{name: providerRAWG, enabled: true}

	r := newStubResolver(broken, empty)
	got, err := r.Search(context.Background(), "q", "")
	if got != nil {
		t.Errorf("expected nil game, got %+v", got)
	}
	if err == nil {
		t.Error("expected the upstream error to surface when nothing resolved")
	}
}

func TestResolverDoesNotMergeMismatchedTitles(t *testing.T) {
	primary := &stubProvider{name: providerIGDB, enabled: true, game: &Game{
		Title: "Doom", Platforms: []string{"PC"},
	}}
	wrongGame := &stubProvider{name: providerRAWG, enabled: true, game: &Game{
		Title: "Doom Eternal", Description: "Different game entirely", Rating: 88,
	}}

	r := newStubResolver(primary, wrongGame)
	got, err := r.Search(context.Background(), "doom", "pc")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Description != "" {
		t.Errorf("a different game's description leaked in: %q", got.Description)
	}
	if got.Rating != 0 {
		t.Errorf("a different game's rating leaked in: %v", got.Rating)
	}
}

func TestResolverStopsEarlyWhenComplete(t *testing.T) {
	complete := &stubProvider{name: providerIGDB, enabled: true, game: &Game{
		Title: "T", Platforms: []string{"PC"}, ReleaseDate: "2020-01-01",
		CoverArt: "u", Description: "d", Rating: 50,
	}}
	later := &stubProvider{name: providerRAWG, enabled: true, game: &Game{Title: "T"}}

	r := newStubResolver(complete, later)
	if _, err := r.Search(context.Background(), "t", ""); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if later.callCount() != 0 {
		t.Errorf("lower-priority provider was queried %d times despite a complete record", later.callCount())
	}
}

func TestResolverSearchRejectsEmptyQuery(t *testing.T) {
	r := newStubResolver(&stubProvider{name: providerIGDB, enabled: true})
	if _, err := r.Search(context.Background(), "   ", ""); err == nil {
		t.Error("expected an error for a blank query")
	}
}

func TestResolverGetByIDRequiresKnownProvider(t *testing.T) {
	r := newStubResolver(&stubProvider{name: providerRAWG, enabled: true, game: &Game{Title: "X"}})

	if _, err := r.GetByID(context.Background(), "nope", "1"); err == nil {
		t.Error("expected an error for an unknown provider name")
	}

	got, err := r.GetByID(context.Background(), providerRAWG, "1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Sources[FieldTitle] != providerRAWG {
		t.Errorf("by-ID lookup should attribute its fields, got %v", got.Sources)
	}
}

func TestResolverGetByIDRejectsDisabledProvider(t *testing.T) {
	r := newStubResolver(&stubProvider{name: providerIGDB, enabled: false})
	if _, err := r.GetByID(context.Background(), providerIGDB, "1"); err == nil {
		t.Error("expected an error when the named provider is not configured")
	}
}

// ── caching ──────────────────────────────────────────────────────────────────

// memCache is an in-memory CacheStore for tests.
type memCache struct {
	mu      sync.Mutex
	entries map[string][]byte
	reads   int
	writes  int
}

func newMemCache() *memCache {
	return &memCache{entries: make(map[string][]byte)}
}

func (m *memCache) GetMetadataCache(key string, _ time.Duration) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	v, ok := m.entries[key]
	return v, ok
}

func (m *memCache) PutMetadataCache(key string, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	m.entries[key] = payload
	return nil
}

func TestResolverCacheAvoidsRepeatProviderCalls(t *testing.T) {
	p := &stubProvider{name: providerIGDB, enabled: true, game: &Game{Title: "Cached", Rating: 70}}
	cache := newMemCache()
	r := &Resolver{providers: []Provider{p}, cache: cache, ttl: time.Hour}

	first, err := r.Search(context.Background(), "q", "")
	if err != nil {
		t.Fatalf("first Search: %v", err)
	}
	second, err := r.Search(context.Background(), "q", "")
	if err != nil {
		t.Fatalf("second Search: %v", err)
	}

	if p.callCount() != 1 {
		t.Errorf("provider called %d times, want 1 (second call should be cached)", p.callCount())
	}
	if first.Title != second.Title || second.Rating != 70 {
		t.Errorf("cached result differs: %+v vs %+v", first, second)
	}
	if second.Sources[FieldTitle] != providerIGDB {
		t.Errorf("attribution lost through the cache: %v", second.Sources)
	}
}

func TestResolverCacheStoresMisses(t *testing.T) {
	p := &stubProvider{name: providerIGDB, enabled: true} // returns (nil, nil)
	cache := newMemCache()
	r := &Resolver{providers: []Provider{p}, cache: cache, ttl: time.Hour}

	if _, err := r.Search(context.Background(), "unknown game", ""); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if _, err := r.Search(context.Background(), "unknown game", ""); err != nil {
		t.Fatalf("Search: %v", err)
	}

	if p.callCount() != 1 {
		t.Errorf("provider called %d times, want 1 (a miss should also be cached)", p.callCount())
	}
}

func TestResolverDisabledCacheAlwaysCallsProvider(t *testing.T) {
	p := &stubProvider{name: providerIGDB, enabled: true, game: &Game{Title: "X"}}
	cache := newMemCache()
	r := &Resolver{providers: []Provider{p}, cache: cache, ttl: -1} // negative TTL disables

	for i := 0; i < 3; i++ {
		if _, err := r.Search(context.Background(), "q", ""); err != nil {
			t.Fatalf("Search: %v", err)
		}
	}
	if p.callCount() != 3 {
		t.Errorf("provider called %d times, want 3 with caching disabled", p.callCount())
	}
	if cache.reads != 0 || cache.writes != 0 {
		t.Errorf("cache was touched despite being disabled: %d reads, %d writes", cache.reads, cache.writes)
	}
}

func TestResolverCorruptCacheEntryFallsBackToProvider(t *testing.T) {
	p := &stubProvider{name: providerIGDB, enabled: true, game: &Game{Title: "Fresh"}}
	cache := newMemCache()
	key := cacheKeyFor(providerIGDB, "search", "q", "")
	_ = cache.PutMetadataCache(key, []byte("{not json"))

	r := &Resolver{providers: []Provider{p}, cache: cache, ttl: time.Hour}
	got, err := r.Search(context.Background(), "q", "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Title != "Fresh" {
		t.Errorf("Title = %q, want %q (should have re-fetched)", got.Title, "Fresh")
	}
	if p.callCount() != 1 {
		t.Errorf("provider called %d times, want 1", p.callCount())
	}
}

func TestCacheKeyForIsCaseInsensitive(t *testing.T) {
	a := cacheKeyFor(providerIGDB, "search", "Chrono Trigger", "SNES")
	b := cacheKeyFor(providerIGDB, "search", "chrono trigger", "snes")
	if a != b {
		t.Errorf("cache keys differ by case: %q vs %q", a, b)
	}
}

func TestGameJSONRoundTripPreservesAttribution(t *testing.T) {
	orig := &Game{}
	orig.Merge(&Game{Title: "T", Rating: 80, ProviderIDs: map[string]string{providerIGDB: "9"}}, providerIGDB)

	payload, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Game
	if err := json.Unmarshal(payload, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(orig.Sources, back.Sources) {
		t.Errorf("Sources = %v, want %v", back.Sources, orig.Sources)
	}
	if !reflect.DeepEqual(orig.ProviderIDs, back.ProviderIDs) {
		t.Errorf("ProviderIDs = %v, want %v", back.ProviderIDs, orig.ProviderIDs)
	}
}

// ── IGDB rate limiter ────────────────────────────────────────────────────────

func TestIGDBRateLimitAllowsBurstThenThrottles(t *testing.T) {
	p := newIGDBProvider("id", "secret", nil)
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < igdbRateLimit; i++ {
		if err := p.rateLimit(ctx); err != nil {
			t.Fatalf("rateLimit %d: %v", i, err)
		}
	}
	burst := time.Since(start)
	if burst > 100*time.Millisecond {
		t.Errorf("first %d requests took %v; they should not be throttled", igdbRateLimit, burst)
	}

	// The window is now full, so the next one must wait for it to slide.
	if err := p.rateLimit(ctx); err != nil {
		t.Fatalf("rateLimit (throttled): %v", err)
	}
	total := time.Since(start)
	if total < igdbRateWindow {
		t.Errorf("request %d returned after %v; expected to wait out the %v window",
			igdbRateLimit+1, total, igdbRateWindow)
	}
}

func TestIGDBRateLimitHonorsContextCancellation(t *testing.T) {
	p := newIGDBProvider("id", "secret", nil)
	ctx := context.Background()

	for i := 0; i < igdbRateLimit; i++ {
		if err := p.rateLimit(ctx); err != nil {
			t.Fatalf("rateLimit %d: %v", i, err)
		}
	}

	cancelCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := p.rateLimit(cancelCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestIGDBProviderEnabledRequiresBothCredentials(t *testing.T) {
	tests := []struct {
		name, id, secret string
		want             bool
	}{
		{"both present", "id", "secret", true},
		{"missing secret", "id", "", false},
		{"missing id", "", "secret", false},
		{"neither", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := newIGDBProvider(tc.id, tc.secret, nil).Enabled(); got != tc.want {
				t.Errorf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ── Steam provider ───────────────────────────────────────────────────────────

func TestSteamHandlesPlatform(t *testing.T) {
	p := newSteamProvider(nil)
	tests := []struct {
		slug string
		want bool
	}{
		{"", true},
		{"pc", true},
		{"snes", false},
		{"ps2", false},
		{"switch", false},
	}
	for _, tc := range tests {
		t.Run("slug="+tc.slug, func(t *testing.T) {
			if got := p.handlesPlatform(tc.slug); got != tc.want {
				t.Errorf("handlesPlatform(%q) = %v, want %v", tc.slug, got, tc.want)
			}
		})
	}
}

func TestSteamSearchDeclinesConsolePlatforms(t *testing.T) {
	// A nil http.Client would panic if a request were attempted, which proves
	// the platform check short-circuits before any network call.
	p := &steamProvider{httpClient: nil}
	got, err := p.Search(context.Background(), "Chrono Trigger", "snes")
	if err != nil || got != nil {
		t.Errorf("Search on a console platform = (%v, %v), want (nil, nil)", got, err)
	}
}

// TestSteamToGameUnescapesDescription pins the entity-decoding of
// short_description, which Steam really does return encoded (verified against
// the live appdetails response for appid 620).
func TestSteamToGameUnescapesDescription(t *testing.T) {
	p := newSteamProvider(nil)
	got := p.toGame(620, &steamAppDetails{
		Name:             "Portal 2",
		ShortDescription: `The &quot;Perpetual Testing Initiative&quot; has been expanded &amp; improved.`,
	})
	want := `The "Perpetual Testing Initiative" has been expanded & improved.`
	if got.Description != want {
		t.Errorf("Description = %q, want %q", got.Description, want)
	}
}

func TestSteamToGameMapsLivePayloadShape(t *testing.T) {
	// Field-for-field mirror of the live appdetails response for appid 620.
	d := &steamAppDetails{
		Name:             "Portal 2",
		ShortDescription: "Puzzle game.",
		HeaderImage:      "https://shared.akamai.steamstatic.com/store_item_assets/steam/apps/620/header.jpg",
		Developers:       []string{"Valve"},
		Publishers:       []string{"Valve"},
	}
	d.Platforms.Windows = true
	d.Platforms.Linux = true
	d.Metacritic.Score = 95
	d.Genres = []struct {
		Description string `json:"description"`
	}{{Description: "Action"}, {Description: "Adventure"}}
	d.ReleaseDate.Date = "Apr 18, 2011"

	got := newSteamProvider(nil).toGame(620, d)

	if got.Title != "Portal 2" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.ReleaseDate != "2011-04-18" {
		t.Errorf("ReleaseDate = %q, want 2011-04-18", got.ReleaseDate)
	}
	if !reflect.DeepEqual(got.Platforms, []string{"PC"}) {
		t.Errorf("Platforms = %v, want [PC]", got.Platforms)
	}
	if !reflect.DeepEqual(got.Genres, []string{"Action", "Adventure"}) {
		t.Errorf("Genres = %v", got.Genres)
	}
	if got.Metacritic != 95 {
		t.Errorf("Metacritic = %d, want 95", got.Metacritic)
	}
	// Steam exposes no aggregate user score, so Rating must stay unset for the
	// resolver to fill it from IGDB/RAWG rather than reusing a critic score.
	if got.Rating != 0 {
		t.Errorf("Rating = %v, want 0 (Steam has no user rating)", got.Rating)
	}
	if got.ProviderIDs[providerSteam] != "620" {
		t.Errorf("ProviderIDs = %v", got.ProviderIDs)
	}
}

func TestNormalizeSteamDate(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Nov 10, 2020", "2020-11-10"},
		{"10 Nov, 2020", "2020-11-10"},
		{"November 10, 2020", "2020-11-10"},
		{"2020-11-10", "2020-11-10"},
		{"Q4 2026", "Q4 2026"}, // unparseable, passed through
		{"Coming Soon", "Coming Soon"},
		{"", ""},
		{"  Nov 10, 2020  ", "2020-11-10"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := normalizeSteamDate(tc.in); got != tc.want {
				t.Errorf("normalizeSteamDate(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMapPlatformSlugToIGDB(t *testing.T) {
	tests := []struct {
		slug, want string
	}{
		{"pc", "6"},
		{"snes", "19"},
		{"ps5", "167"},
		{"switch", "130"},
		{"unknown-platform", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run("slug="+tc.slug, func(t *testing.T) {
			if got := mapPlatformSlugToIGDB(tc.slug); got != tc.want {
				t.Errorf("mapPlatformSlugToIGDB(%q) = %q, want %q", tc.slug, got, tc.want)
			}
		})
	}
}

// ── back-compat surface ──────────────────────────────────────────────────────

// TestLegacyClientSurfaceUnchanged pins the public API that pre-existing
// callers (internal/api) depend on, so a future refactor can't silently drop it.
func TestLegacyClientSurfaceUnchanged(t *testing.T) {
	var c *Client = NewClient("key")
	if !c.Enabled() {
		t.Error("NewClient(non-empty).Enabled() should be true")
	}
	var (
		_ func(string, string) (*GameMetadata, error) = c.SearchGame
		_ func(int) (*GameMetadata, error)            = c.GetGame
		_ func(string) string                         = MapRAWGPlatformToSlug
	)
}
