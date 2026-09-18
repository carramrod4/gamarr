package metadata

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSortByReleaseDate(t *testing.T) {
	tests := []struct {
		name  string
		dates []string
		want  []string
	}{
		{
			name:  "already ordered",
			dates: []string{"2026-01-01", "2026-06-15", "2027-03-02"},
			want:  []string{"2026-01-01", "2026-06-15", "2027-03-02"},
		},
		{
			name:  "reversed",
			dates: []string{"2027-03-02", "2026-06-15", "2026-01-01"},
			want:  []string{"2026-01-01", "2026-06-15", "2027-03-02"},
		},
		{
			// A plain lexical sort puts "" first, which would lead a release
			// calendar with its least useful rows.
			name:  "undated entries sort last",
			dates: []string{"", "2026-06-15", "", "2026-01-01"},
			want:  []string{"2026-01-01", "2026-06-15", "", ""},
		},
		{
			name:  "all undated is stable, not an error",
			dates: []string{"", ""},
			want:  []string{"", ""},
		},
		{
			name:  "same day keeps input order",
			dates: []string{"2026-05-05", "2026-05-05"},
			want:  []string{"2026-05-05", "2026-05-05"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			games := make([]*Game, len(tt.dates))
			for i, d := range tt.dates {
				games[i] = &Game{Title: "g", ReleaseDate: d}
			}
			SortByReleaseDate(games)
			for i, want := range tt.want {
				if games[i].ReleaseDate != want {
					t.Errorf("index %d = %q, want %q", i, games[i].ReleaseDate, want)
				}
			}
		})
	}
}

func TestSortByReleaseDate_IsStableForEqualDates(t *testing.T) {
	games := []*Game{
		{Title: "first", ReleaseDate: "2026-05-05"},
		{Title: "second", ReleaseDate: "2026-05-05"},
		{Title: "third", ReleaseDate: "2026-05-05"},
	}
	SortByReleaseDate(games)
	for i, want := range []string{"first", "second", "third"} {
		if games[i].Title != want {
			t.Errorf("index %d = %q, want %q", i, games[i].Title, want)
		}
	}
}

func TestNormalizeForMatch_MatchesResolverRules(t *testing.T) {
	tests := []struct {
		a, b  string
		equal bool
	}{
		{"Chrono Trigger", "chrono trigger", true},
		{"  Chrono Trigger  ", "Chrono Trigger", true},
		{"Hollow Knight: Silksong", "Hollow Knight Silksong", true},
		{"Chrono Trigger", "Chrono Cross", false},
	}
	for _, tt := range tests {
		got := NormalizeForMatch(tt.a) == NormalizeForMatch(tt.b)
		if got != tt.equal {
			t.Errorf("NormalizeForMatch(%q) == NormalizeForMatch(%q) = %v, want %v",
				tt.a, tt.b, got, tt.equal)
		}
	}
}

// ── Resolver.ReleasesBetween ───────────────────────────────────────────────────

// stubBrowser is a Provider that also implements ReleaseBrowser.
type stubBrowser struct {
	name    string
	enabled bool
	games   []*Game
	err     error
	calls   *int
}

func (s *stubBrowser) Name() string                                          { return s.name }
func (s *stubBrowser) Enabled() bool                                         { return s.enabled }
func (s *stubBrowser) Search(context.Context, string, string) (*Game, error) { return nil, nil }
func (s *stubBrowser) GetByID(context.Context, string) (*Game, error)        { return nil, nil }
func (s *stubBrowser) GetCoverArt(context.Context, *Game) (string, error)    { return "", nil }
func (s *stubBrowser) ReleasesBetween(context.Context, time.Time, time.Time, int) ([]*Game, error) {
	if s.calls != nil {
		*s.calls++
	}
	return s.games, s.err
}

// stubPlain is a Provider with no ReleaseBrowser capability, like Steam.
type stubPlain struct{ name string }

func (s *stubPlain) Name() string                                          { return s.name }
func (s *stubPlain) Enabled() bool                                         { return true }
func (s *stubPlain) Search(context.Context, string, string) (*Game, error) { return nil, nil }
func (s *stubPlain) GetByID(context.Context, string) (*Game, error)        { return nil, nil }
func (s *stubPlain) GetCoverArt(context.Context, *Game) (string, error)    { return "", nil }

func resolverWith(providers ...Provider) *Resolver {
	return &Resolver{providers: providers}
}

func TestReleasesBetween_UsesHighestPriorityProviderThatAnswers(t *testing.T) {
	var secondCalls int
	r := resolverWith(
		&stubBrowser{name: providerIGDB, enabled: true, games: []*Game{{Title: "From IGDB", ReleaseDate: "2026-02-01"}}},
		&stubBrowser{name: providerRAWG, enabled: true, calls: &secondCalls,
			games: []*Game{{Title: "From RAWG", ReleaseDate: "2026-02-01"}}},
	)

	games, err := r.ReleasesBetween(context.Background(), time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatalf("ReleasesBetween: %v", err)
	}
	if len(games) != 1 || games[0].Title != "From IGDB" {
		t.Fatalf("got %+v, want the IGDB row only", games)
	}
	// Not merged, and the fallback is never even asked once the primary answers.
	if secondCalls != 0 {
		t.Errorf("fallback provider was called %d times, want 0", secondCalls)
	}
}

func TestReleasesBetween_FallsBackWhenPrimaryIsDisabled(t *testing.T) {
	r := resolverWith(
		&stubBrowser{name: providerIGDB, enabled: false, games: []*Game{{Title: "unreachable"}}},
		&stubBrowser{name: providerRAWG, enabled: true, games: []*Game{{Title: "From RAWG", ReleaseDate: "2026-02-01"}}},
	)

	games, err := r.ReleasesBetween(context.Background(), time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatalf("ReleasesBetween: %v", err)
	}
	if len(games) != 1 || games[0].Title != "From RAWG" {
		t.Fatalf("got %+v, want the RAWG row", games)
	}
}

func TestReleasesBetween_FallsBackWhenPrimaryFails(t *testing.T) {
	r := resolverWith(
		&stubBrowser{name: providerIGDB, enabled: true, err: errors.New("igdb down")},
		&stubBrowser{name: providerRAWG, enabled: true, games: []*Game{{Title: "From RAWG", ReleaseDate: "2026-02-01"}}},
	)

	games, err := r.ReleasesBetween(context.Background(), time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatalf("a working fallback should not surface the primary's error, got %v", err)
	}
	if len(games) != 1 || games[0].Title != "From RAWG" {
		t.Fatalf("got %+v, want the RAWG row", games)
	}
}

func TestReleasesBetween_FallsBackOnEmptyWindow(t *testing.T) {
	r := resolverWith(
		&stubBrowser{name: providerIGDB, enabled: true, games: nil},
		&stubBrowser{name: providerRAWG, enabled: true, games: []*Game{{Title: "From RAWG"}}},
	)

	games, err := r.ReleasesBetween(context.Background(), time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatalf("ReleasesBetween: %v", err)
	}
	if len(games) != 1 || games[0].Title != "From RAWG" {
		t.Fatalf("got %+v, want the RAWG row", games)
	}
}

func TestReleasesBetween_ReportsErrorOnlyWhenNothingAnswered(t *testing.T) {
	r := resolverWith(
		&stubBrowser{name: providerIGDB, enabled: true, err: errors.New("igdb down")},
		&stubBrowser{name: providerRAWG, enabled: true, err: errors.New("rawg down")},
	)

	games, err := r.ReleasesBetween(context.Background(), time.Now(), time.Now(), 10)
	if len(games) != 0 {
		t.Errorf("got %d games, want none", len(games))
	}
	if err == nil {
		t.Error("expected the first provider error when nothing resolved")
	}
}

// A provider without the capability must be skipped, not treated as a failure.
func TestReleasesBetween_SkipsProvidersWithoutTheCapability(t *testing.T) {
	r := resolverWith(
		&stubPlain{name: providerSteam},
		&stubBrowser{name: providerIGDB, enabled: true, games: []*Game{{Title: "From IGDB"}}},
	)

	games, err := r.ReleasesBetween(context.Background(), time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatalf("ReleasesBetween: %v", err)
	}
	if len(games) != 1 || games[0].Title != "From IGDB" {
		t.Fatalf("got %+v, want the IGDB row", games)
	}
}

// Every returned row carries per-field attribution, same as a resolved search.
func TestReleasesBetween_AttributesFields(t *testing.T) {
	r := resolverWith(&stubBrowser{
		name:    providerIGDB,
		enabled: true,
		games: []*Game{{
			Title:       "Chrono Trigger",
			ReleaseDate: "1995-03-11",
			Platforms:   []string{"Super Nintendo Entertainment System"},
		}},
	})

	games, err := r.ReleasesBetween(context.Background(), time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatalf("ReleasesBetween: %v", err)
	}
	if len(games) != 1 {
		t.Fatalf("got %d games, want 1", len(games))
	}
	for _, field := range []string{FieldTitle, FieldReleaseDate, FieldPlatforms} {
		if got := games[0].Sources[field]; got != providerIGDB {
			t.Errorf("Sources[%q] = %q, want %q", field, got, providerIGDB)
		}
	}
}

func TestReleasesBetween_NoProvidersAtAll(t *testing.T) {
	r := resolverWith()
	games, err := r.ReleasesBetween(context.Background(), time.Now(), time.Now(), 10)
	if err != nil {
		t.Errorf("err = %v, want nil when nothing is configured", err)
	}
	if len(games) != 0 {
		t.Errorf("got %d games, want none", len(games))
	}
}

func TestCanBrowseReleases(t *testing.T) {
	tests := []struct {
		name      string
		providers []Provider
		want      bool
	}{
		{
			name:      "nothing configured",
			providers: nil,
			want:      false,
		},
		{
			// The case that mattered in practice: Steam needs no credentials,
			// so Enabled() is true, but it cannot list releases by date.
			name:      "only a provider without the capability",
			providers: []Provider{&stubPlain{name: providerSteam}},
			want:      false,
		},
		{
			name:      "a capable provider that is disabled does not count",
			providers: []Provider{&stubBrowser{name: providerIGDB, enabled: false}},
			want:      false,
		},
		{
			name:      "a capable, enabled provider",
			providers: []Provider{&stubBrowser{name: providerIGDB, enabled: true}},
			want:      true,
		},
		{
			name: "capable one alongside an incapable one",
			providers: []Provider{
				&stubPlain{name: providerSteam},
				&stubBrowser{name: providerRAWG, enabled: true},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolverWith(tt.providers...).CanBrowseReleases(); got != tt.want {
				t.Errorf("CanBrowseReleases() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A real Resolver with no credentials at all still has Steam enabled, so this
// pins that the two methods genuinely disagree there - the property the
// calendar handler depends on.
func TestCanBrowseReleasesIsNarrowerThanEnabled(t *testing.T) {
	r := NewResolver(ResolverConfig{})
	if !r.Enabled() {
		t.Fatal("expected Steam to keep an uncredentialed resolver enabled")
	}
	if r.CanBrowseReleases() {
		t.Error("an uncredentialed resolver cannot browse releases")
	}
}
