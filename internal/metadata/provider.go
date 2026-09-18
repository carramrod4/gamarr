package metadata

import (
	"context"
	"strconv"
	"strings"
)

// Provider names, used as Game.Sources values and ProviderIDs keys.
const (
	providerIGDB  = "igdb"
	providerRAWG  = "rawg"
	providerSteam = "steam"
)

// userAgent identifies Gamarr to every upstream metadata API.
const userAgent = "Gamarr/2.0 (game download manager)"

// Field names used as keys in Game.Sources for per-field attribution.
const (
	FieldTitle       = "title"
	FieldPlatforms   = "platforms"
	FieldReleaseDate = "release_date"
	FieldCoverArt    = "cover_art"
	FieldDescription = "description"
	FieldRating      = "rating"
)

// Provider is a single upstream metadata source (IGDB, RAWG, Steam, ...).
//
// Implementations must be safe for concurrent use and must not block
// indefinitely: every method takes a context and is expected to honor it.
// A disabled provider (missing credentials) returns false from Enabled and
// (nil, nil) from Search/GetByID rather than an error, so the resolver can
// skip it without treating configuration as a failure.
type Provider interface {
	// Name is the short, stable identifier recorded in Game.Sources
	// ("igdb", "rawg", "steam").
	Name() string

	// Enabled reports whether this provider has the credentials it needs.
	Enabled() bool

	// Search finds the best match for a title, optionally constrained to a
	// Gamarr platform slug. A miss is (nil, nil), not an error.
	Search(ctx context.Context, query, platformSlug string) (*Game, error)

	// GetByID fetches full detail for this provider's own ID. IDs are
	// provider-scoped strings (IGDB and RAWG use numbers, Steam uses appids),
	// so they are never interchangeable across providers.
	GetByID(ctx context.Context, id string) (*Game, error)

	// GetCoverArt returns a cover image URL for the game. Providers that
	// already populate Game.CoverArt during Search/GetByID may return it
	// directly; IGDB needs a second call, which is why this is separate.
	GetCoverArt(ctx context.Context, g *Game) (string, error)
}

// Game is the canonical, provider-independent game record produced by the
// resolver. Rating is normalized to a 0-100 scale across every provider
// (RAWG's native 0-5 and Steam's 0-100 both land here), so merged records
// are comparable regardless of which source won a given field.
type Game struct {
	Title       string   `json:"title"`
	Platforms   []string `json:"platforms"`
	ReleaseDate string   `json:"release_date"`
	CoverArt    string   `json:"cover_art"`
	Description string   `json:"description"`
	Rating      float64  `json:"rating"`

	// Genres, Developers, Publishers, Metacritic, ESRB and Slug are carried
	// through for the legacy GameMetadata shape. They are merged
	// first-non-empty-wins like the canonical fields but are not attributed,
	// since the legacy API has nowhere to surface attribution.
	Genres     []string `json:"genres,omitempty"`
	Developers []string `json:"developers,omitempty"`
	Publishers []string `json:"publishers,omitempty"`
	Metacritic int      `json:"metacritic,omitempty"`
	ESRB       string   `json:"esrb_rating,omitempty"`
	Slug       string   `json:"slug,omitempty"`

	// ProviderIDs maps provider name to that provider's own ID for this game,
	// so a later refresh can go straight to GetByID instead of re-searching.
	ProviderIDs map[string]string `json:"provider_ids,omitempty"`

	// Sources maps a canonical field name (the Field* constants) to the name
	// of the provider that supplied the value currently in that field.
	Sources map[string]string `json:"sources,omitempty"`
}

// setSource records which provider supplied a field, allocating the map lazily.
func (g *Game) setSource(field, provider string) {
	if provider == "" {
		return
	}
	if g.Sources == nil {
		g.Sources = make(map[string]string)
	}
	g.Sources[field] = provider
}

// addProviderID records a provider's own ID for this game.
func (g *Game) addProviderID(provider, id string) {
	if provider == "" || id == "" {
		return
	}
	if g.ProviderIDs == nil {
		g.ProviderIDs = make(map[string]string)
	}
	g.ProviderIDs[provider] = id
}

// Merge folds src into dst using first-non-empty-wins semantics: a field is
// taken from src only when dst does not already have a value for it. Because
// the resolver merges in provider-priority order, this means the
// highest-priority provider that actually returned a value for a field wins
// that field, and lower-priority providers fill only the gaps.
//
// srcName is recorded in dst.Sources for every canonical field src supplies,
// which is what makes a merged record auditable field by field.
//
// Merge never overwrites a non-empty value, so calling it repeatedly with the
// same inputs is idempotent, and merge order fully determines the result.
func (dst *Game) Merge(src *Game, srcName string) {
	if src == nil {
		return
	}

	if dst.Title == "" && src.Title != "" {
		dst.Title = src.Title
		dst.setSource(FieldTitle, srcName)
	}
	if len(dst.Platforms) == 0 && len(src.Platforms) > 0 {
		dst.Platforms = append([]string(nil), src.Platforms...)
		dst.setSource(FieldPlatforms, srcName)
	}
	if dst.ReleaseDate == "" && src.ReleaseDate != "" {
		dst.ReleaseDate = src.ReleaseDate
		dst.setSource(FieldReleaseDate, srcName)
	}
	if dst.CoverArt == "" && src.CoverArt != "" {
		dst.CoverArt = src.CoverArt
		dst.setSource(FieldCoverArt, srcName)
	}
	if dst.Description == "" && src.Description != "" {
		dst.Description = src.Description
		dst.setSource(FieldDescription, srcName)
	}
	// Rating is normalized to 0-100 by every provider, so 0 unambiguously
	// means "no rating" rather than "rated zero" - no provider reports a
	// genuine 0 for a game that has any ratings at all.
	if dst.Rating == 0 && src.Rating != 0 {
		dst.Rating = src.Rating
		dst.setSource(FieldRating, srcName)
	}

	// Legacy passthrough fields: same first-non-empty-wins rule, no attribution.
	if len(dst.Genres) == 0 && len(src.Genres) > 0 {
		dst.Genres = append([]string(nil), src.Genres...)
	}
	if len(dst.Developers) == 0 && len(src.Developers) > 0 {
		dst.Developers = append([]string(nil), src.Developers...)
	}
	if len(dst.Publishers) == 0 && len(src.Publishers) > 0 {
		dst.Publishers = append([]string(nil), src.Publishers...)
	}
	if dst.Metacritic == 0 && src.Metacritic != 0 {
		dst.Metacritic = src.Metacritic
	}
	if dst.ESRB == "" && src.ESRB != "" {
		dst.ESRB = src.ESRB
	}
	if dst.Slug == "" && src.Slug != "" {
		dst.Slug = src.Slug
	}

	for provider, id := range src.ProviderIDs {
		dst.addProviderID(provider, id)
	}
}

// ToGameMetadata converts a canonical Game back into the legacy GameMetadata
// shape the pre-existing public API returns, so callers of SearchGame/GetGame
// keep receiving exactly the struct they already handle.
//
// Rating is converted back to RAWG's native 0-5 scale, since that is what
// every existing consumer of GameMetadata.Rating was written against.
func (g *Game) ToGameMetadata() *GameMetadata {
	if g == nil {
		return nil
	}
	meta := &GameMetadata{
		Name:            g.Title,
		Slug:            g.Slug,
		Description:     g.Description,
		Released:        g.ReleaseDate,
		Rating:          g.Rating / 20.0,
		Metacritic:      g.Metacritic,
		BackgroundImage: g.CoverArt,
		Genres:          g.Genres,
		Platforms:       g.Platforms,
		Developers:      g.Developers,
		Publishers:      g.Publishers,
		ESRB:            g.ESRB,
	}
	// GameMetadata.ID is a RAWG ID by definition (the legacy
	// /api/metadata/{rawg_id} route feeds it straight back to GetGame), so
	// only populate it when RAWG actually identified this game.
	if id, ok := g.ProviderIDs[providerRAWG]; ok {
		meta.ID = atoiSafe(id)
	}
	return meta
}

// fromGameMetadata adapts a legacy RAWG-shaped record into the canonical form.
func fromGameMetadata(m *GameMetadata, providerName string) *Game {
	if m == nil {
		return nil
	}
	g := &Game{
		Title:       m.Name,
		Platforms:   m.Platforms,
		ReleaseDate: m.Released,
		CoverArt:    m.BackgroundImage,
		Description: m.Description,
		Rating:      m.Rating * 20.0, // RAWG's 0-5 -> canonical 0-100
		Genres:      m.Genres,
		Developers:  m.Developers,
		Publishers:  m.Publishers,
		Metacritic:  m.Metacritic,
		ESRB:        m.ESRB,
		Slug:        m.Slug,
	}
	if m.ID > 0 {
		g.addProviderID(providerName, itoaSafe(m.ID))
	}
	return g
}

// atoiSafe parses a provider ID string, returning 0 when it is not numeric.
func atoiSafe(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// itoaSafe formats a numeric provider ID as its string form.
func itoaSafe(n int) string {
	return strconv.Itoa(n)
}

// exactTitleMatch returns the index of the first title equal to query once
// normalized, or -1 when none matches.
//
// Both IGDB and Steam rank their own search results by internal relevance,
// which routinely places a spinoff or sequel above an exact match: IGDB
// returns "Portal 2: In Motion" ahead of "Portal 2" (verified live), and
// Steam does the same with popular sequels. Providers therefore prefer an
// exact title match over the upstream ordering and only fall back to the
// first result when nothing matches exactly.
func exactTitleMatch(query string, titles []string) int {
	wanted := normalizeTitle(query)
	if wanted == "" {
		return -1
	}
	for i, t := range titles {
		if normalizeTitle(t) == wanted {
			return i
		}
	}
	return -1
}

// normalizeTitle lowercases and strips punctuation/whitespace noise so titles
// from different providers can be compared for equality ("Chrono Trigger" vs
// "chrono trigger:" vs "Chrono  Trigger").
func normalizeTitle(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevSpace = false
		case r == ' ' || r == '\t' || r == '-' || r == '_' || r == ':':
			if !prevSpace && b.Len() > 0 {
				b.WriteRune(' ')
				prevSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}
