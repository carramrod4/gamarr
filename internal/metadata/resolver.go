package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// DefaultCacheTTL is used when ResolverConfig.CacheTTL is left at zero.
const DefaultCacheTTL = 24 * time.Hour

// CacheStore is the subset of the SQLite layer the resolver needs.
// *db.JobStore satisfies it; tests use an in-memory stub.
type CacheStore interface {
	GetMetadataCache(key string, maxAge time.Duration) ([]byte, bool)
	PutMetadataCache(key string, payload []byte) error
}

// ResolverConfig configures which providers a Resolver will consult.
type ResolverConfig struct {
	// IGDB (primary). Both values are required for IGDB to be enabled.
	IGDBClientID     string
	IGDBClientSecret string

	// RAWG (fallback). Enabled when non-empty.
	RAWGAPIKey string

	// DisableSteam turns off the Steam provider, which is otherwise always
	// on because it needs no credentials.
	DisableSteam bool

	// CacheTTL bounds how long a cached provider response stays valid.
	// Zero means DefaultCacheTTL; negative disables caching entirely.
	CacheTTL time.Duration

	// Cache is optional. Without it the resolver still works, just with no
	// cross-run caching (each provider keeps its own in-process cache).
	Cache CacheStore

	// HTTPClient is shared by every provider. Optional.
	HTTPClient *http.Client
}

// Resolver queries metadata providers in priority order and merges what they
// return into a single canonical Game.
//
// Priority is fixed at construction: IGDB, then RAWG, then Steam. Because
// Game.Merge is first-non-empty-wins and the resolver merges in this order,
// the highest-priority provider that actually has a value for a field owns
// that field, and every field records which provider supplied it.
type Resolver struct {
	providers []Provider
	cache     CacheStore
	ttl       time.Duration
}

// NewResolver builds a Resolver with the providers the config enables.
func NewResolver(cfg ResolverConfig) *Resolver {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}

	ttl := cfg.CacheTTL
	if ttl == 0 {
		ttl = DefaultCacheTTL
	}

	r := &Resolver{cache: cfg.Cache, ttl: ttl}

	// Priority order. Disabled providers are kept in the slice and skipped at
	// query time, so Providers() can report the full configured picture.
	r.providers = append(r.providers, newIGDBProvider(cfg.IGDBClientID, cfg.IGDBClientSecret, hc))
	r.providers = append(r.providers, newRAWGProvider(cfg.RAWGAPIKey, hc))
	if !cfg.DisableSteam {
		r.providers = append(r.providers, newSteamProvider(hc))
	}
	return r
}

// Enabled reports whether at least one provider can serve requests.
func (r *Resolver) Enabled() bool {
	for _, p := range r.providers {
		if p.Enabled() {
			return true
		}
	}
	return false
}

// Providers returns the names of currently-enabled providers, in priority order.
func (r *Resolver) Providers() []string {
	var names []string
	for _, p := range r.providers {
		if p.Enabled() {
			names = append(names, p.Name())
		}
	}
	return names
}

// Search resolves a title across every enabled provider and returns the merged
// record, or nil when no provider recognized it.
//
// Lower-priority results are only merged when their title matches the record
// established by a higher-priority provider. Providers disagree about which
// game a loose query means ("Doom" matches four different releases), and
// blindly merging a different game's fields would produce a record that is
// individually sourced but collectively wrong.
func (r *Resolver) Search(ctx context.Context, query, platformSlug string) (*Game, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty search query")
	}

	merged := &Game{}
	var firstErr error
	found := false

	for _, p := range r.providers {
		if !p.Enabled() {
			continue
		}
		if merged.isComplete() {
			// Every attributable field is already filled by a
			// higher-priority provider; further calls would be discarded.
			break
		}

		key := cacheKeyFor(p.Name(), "search", query, platformSlug)
		g, err := r.fetch(ctx, key, func() (*Game, error) {
			return p.Search(ctx, query, platformSlug)
		})
		if err != nil {
			// A provider being down must not fail the whole resolve - record
			// it and let the remaining providers answer.
			slog.Warn("metadata provider search failed",
				"provider", p.Name(), "query", query, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if g == nil {
			continue
		}
		if found && !titlesMatch(merged.Title, g.Title) {
			slog.Debug("metadata provider returned a different game; not merging",
				"provider", p.Name(), "have", merged.Title, "got", g.Title)
			continue
		}

		merged.Merge(g, p.Name())
		found = true
	}

	if !found {
		// Only surface an upstream error when nothing at all was resolved;
		// a partial success is more useful to the caller than an error.
		return nil, firstErr
	}
	return merged, nil
}

// GetByID fetches one provider's record by that provider's own ID. IDs are not
// portable between providers, so the provider must be named explicitly.
func (r *Resolver) GetByID(ctx context.Context, providerName, id string) (*Game, error) {
	for _, p := range r.providers {
		if p.Name() != providerName {
			continue
		}
		if !p.Enabled() {
			return nil, fmt.Errorf("metadata provider %q is not configured", providerName)
		}
		key := cacheKeyFor(p.Name(), "id", id, "")
		g, err := r.fetch(ctx, key, func() (*Game, error) {
			return p.GetByID(ctx, id)
		})
		if err != nil {
			return nil, err
		}
		if g != nil {
			// Attribute every field this single provider supplied, so a
			// by-ID lookup carries the same attribution as a merged search.
			out := &Game{}
			out.Merge(g, p.Name())
			return out, nil
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unknown metadata provider %q", providerName)
}

// CoverArt resolves cover art for an already-resolved game, asking providers in
// priority order until one supplies a URL.
func (r *Resolver) CoverArt(ctx context.Context, g *Game) (string, error) {
	if g == nil {
		return "", nil
	}
	if g.CoverArt != "" {
		return g.CoverArt, nil
	}
	var firstErr error
	for _, p := range r.providers {
		if !p.Enabled() {
			continue
		}
		url, err := p.GetCoverArt(ctx, g)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if url != "" {
			return url, nil
		}
	}
	return "", firstErr
}

// fetch runs call, transparently serving and populating the SQLite cache.
//
// A cache entry is written even for a miss (a nil game encoded as JSON null),
// so a title no provider knows about doesn't re-query every provider on every
// subsequent enrich attempt.
func (r *Resolver) fetch(_ context.Context, key string, call func() (*Game, error)) (*Game, error) {
	if r.cache != nil && r.ttl > 0 {
		if payload, ok := r.cache.GetMetadataCache(key, r.ttl); ok {
			var cached *Game
			if err := json.Unmarshal(payload, &cached); err == nil {
				return cached, nil
			}
			// A corrupt/outdated cache row is not fatal: fall through and
			// re-fetch, overwriting it below.
			slog.Debug("discarding unreadable metadata cache entry", "key", key)
		}
	}

	g, err := call()
	if err != nil {
		return nil, err
	}

	if r.cache != nil && r.ttl > 0 {
		if payload, mErr := json.Marshal(g); mErr == nil {
			if pErr := r.cache.PutMetadataCache(key, payload); pErr != nil {
				slog.Debug("failed to write metadata cache", "key", key, "error", pErr)
			}
		}
	}
	return g, nil
}

// isComplete reports whether every attributable field already has a value, in
// which case no lower-priority provider can contribute anything.
func (g *Game) isComplete() bool {
	return g.Title != "" &&
		len(g.Platforms) > 0 &&
		g.ReleaseDate != "" &&
		g.CoverArt != "" &&
		g.Description != "" &&
		g.Rating != 0
}

// titlesMatch reports whether two provider titles refer to the same game,
// comparing normalized forms and tolerating one being a prefix of the other
// ("Doom" vs "Doom (2016)", "Half-Life 2" vs "Half-Life 2: Episode One" is
// deliberately NOT a match because the suffix follows a separator that
// normalizeTitle preserves as a space-delimited word).
func titlesMatch(a, b string) bool {
	na, nb := normalizeTitle(a), normalizeTitle(b)
	if na == "" || nb == "" {
		return true // nothing to contradict
	}
	return na == nb
}

// cacheKeyFor builds a stable cache key. Keys are lowercased so the same query
// in different casing shares one entry.
func cacheKeyFor(provider, op, arg, platformSlug string) string {
	return strings.ToLower(strings.Join([]string{provider, op, arg, platformSlug}, "|"))
}
