package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gamarr/internal/metadata"
)

// The calendar previously called RAWG directly, which meant it went silently
// empty whenever RAWG_API_KEY was unset - the state this deployment was
// actually in, so the feature had never worked here at all. It now goes through
// the metadata resolver, so it uses whichever provider is configured (IGDB
// first, RAWG as fallback) and reports honestly when none is.

// calendarCacheTTL bounds how long a fetched window stays usable. Release dates
// move on the order of days, so hours of staleness costs nothing and keeps the
// IGDB rate limit well clear.
const calendarCacheTTL = 6 * time.Hour

// calendarWindowDays is how far either side of today gets fetched and cached.
// A request for fewer days is served by filtering this window rather than by a
// second upstream call.
const calendarWindowDays = 90

// calendarCache caches the fetched release windows. It is a package-level value
// rather than a Server field to preserve the original behavior (one cache for
// the process); the mutex guards every field.
var calendarCache struct {
	mu sync.RWMutex
	// refresh serializes upstream fetches. Without it, every request arriving
	// while the cache is cold starts its own multi-page provider walk - and a
	// full window is now a dozen upstream calls, so that is a real cost, not a
	// theoretical one. Held across the fetch, which is why it is separate from
	// mu (held only for the field writes).
	refresh     sync.Mutex
	upcoming    []calendarEntry
	recent      []calendarEntry
	lastFetched time.Time
	// lastErr records why the most recent refresh produced nothing, so the
	// handler can distinguish "no provider configured" and "provider down"
	// from a genuinely empty window.
	lastErr string
}

// calendarEntry represents a game release.
//
// The response keys are kept as the RAWG-backed version had them so any
// existing client keeps working (the bundled React UI does not consume this
// route at all yet - verified, not assumed). Source and InLibrary are additive;
// the old RAWG-specific numeric id is gone, since it identified a row in one
// provider's catalogue and means nothing once several providers can answer.
type calendarEntry struct {
	Name            string   `json:"name"`
	ReleaseDate     string   `json:"release_date"`
	Platforms       []string `json:"platforms"`
	BackgroundImage string   `json:"background_image,omitempty"`
	Rating          float64  `json:"rating"`
	OnWishlist      bool     `json:"on_wishlist"`
	InLibrary       bool     `json:"in_library"`
	Source          string   `json:"source,omitempty"`
}

// handleCalendar handles GET /api/calendar - upcoming game releases.
func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	s.writeCalendar(w, r, true)
}

// handleCalendarRecent handles GET /api/calendar/recent - recent releases.
func (s *Server) handleCalendarRecent(w http.ResponseWriter, r *http.Request) {
	s.writeCalendar(w, r, false)
}

func (s *Server) writeCalendar(w http.ResponseWriter, r *http.Request, upcoming bool) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}
	if days > calendarWindowDays {
		days = calendarWindowDays
	}

	entries, refreshErr := s.releaseWindow(r.Context(), days, upcoming)
	entries = filterByPlatform(entries, r.URL.Query().Get("platform"))
	s.annotateOwnership(entries)

	if entries == nil {
		entries = []calendarEntry{}
	}

	resp := map[string]interface{}{
		"success": true,
		"entries": entries,
		"total":   len(entries),
		"days":    days,
	}
	// An empty calendar has two very different causes. Saying which one it is
	// here is the difference between "nothing is coming out" and "metadata is
	// not configured", which looked identical in the RAWG-only version.
	if len(entries) == 0 && refreshErr != "" {
		resp["warning"] = refreshErr
	}
	writeJSON(w, http.StatusOK, resp)
}

// releaseWindow returns the cached window trimmed to the requested day count,
// refreshing the cache when it has expired.
func (s *Server) releaseWindow(ctx context.Context, days int, upcoming bool) ([]calendarEntry, string) {
	calendarCache.mu.RLock()
	fresh := time.Since(calendarCache.lastFetched) < calendarCacheTTL
	calendarCache.mu.RUnlock()

	if !fresh {
		s.refreshCalendarCache(ctx)
	}

	calendarCache.mu.RLock()
	defer calendarCache.mu.RUnlock()

	now := time.Now()
	var src []calendarEntry
	var lo, hi string
	if upcoming {
		src = calendarCache.upcoming
		lo, hi = now.Format(dateLayout), now.AddDate(0, 0, days).Format(dateLayout)
	} else {
		src = calendarCache.recent
		lo, hi = now.AddDate(0, 0, -days).Format(dateLayout), now.Format(dateLayout)
	}

	out := make([]calendarEntry, 0, len(src))
	for _, e := range src {
		if e.ReleaseDate >= lo && e.ReleaseDate <= hi {
			out = append(out, e)
		}
	}
	return out, calendarCache.lastErr
}

const dateLayout = "2006-01-02"

// refreshCalendarCache fetches one window spanning calendarWindowDays either
// side of today and splits it at today, so both handlers are served by a single
// upstream call instead of two.
func (s *Server) refreshCalendarCache(ctx context.Context) {
	calendarCache.refresh.Lock()
	defer calendarCache.refresh.Unlock()

	// Another request may have refreshed while this one waited for the lock.
	calendarCache.mu.RLock()
	fresh := time.Since(calendarCache.lastFetched) < calendarCacheTTL
	calendarCache.mu.RUnlock()
	if fresh {
		return
	}

	now := time.Now()
	from := now.AddDate(0, 0, -calendarWindowDays)
	to := now.AddDate(0, 0, calendarWindowDays)

	var entries []calendarEntry
	var errMsg string

	// CanBrowseReleases rather than Enabled: Steam is always enabled (it needs
	// no credentials) but cannot answer a date-range query, so Enabled() alone
	// would report a configured calendar that can only ever come back empty.
	if s.meta == nil || !s.meta.CanBrowseReleases() {
		errMsg = "No metadata provider that can list releases by date is configured. Set IGDB_CLIENT_ID and IGDB_CLIENT_SECRET (or RAWG_API_KEY) to populate the release calendar."
	} else {
		games, err := s.meta.ReleasesBetween(ctx, from, to, 0)
		if err != nil && len(games) == 0 {
			errMsg = "Could not reach the metadata provider: " + err.Error()
		}
		metadata.SortByReleaseDate(games)
		for _, g := range games {
			if g == nil || g.Title == "" || g.ReleaseDate == "" {
				continue
			}
			entries = append(entries, calendarEntry{
				Name:            g.Title,
				ReleaseDate:     g.ReleaseDate,
				Platforms:       g.Platforms,
				BackgroundImage: g.CoverArt,
				Rating:          g.Rating,
				Source:          g.Sources[metadata.FieldTitle],
			})
		}
	}

	today := now.Format(dateLayout)
	var upcoming, recent []calendarEntry
	for _, e := range entries {
		if e.ReleaseDate >= today {
			upcoming = append(upcoming, e)
		} else {
			recent = append(recent, e)
		}
	}
	// recent reads newest-first, which is the order a "recently released" list
	// wants; upcoming stays soonest-first.
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}

	calendarCache.mu.Lock()
	calendarCache.upcoming = upcoming
	calendarCache.recent = recent
	calendarCache.lastFetched = time.Now()
	calendarCache.lastErr = errMsg
	calendarCache.mu.Unlock()
}

// filterByPlatform keeps entries matching a platform name or slug. An empty
// filter or "all" keeps everything.
func filterByPlatform(entries []calendarEntry, platform string) []calendarEntry {
	if platform == "" || platform == "all" {
		return entries
	}
	out := make([]calendarEntry, 0, len(entries))
	for _, e := range entries {
		for _, p := range e.Platforms {
			if strings.EqualFold(p, platform) || strings.EqualFold(slugify(p), platform) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// annotateOwnership marks entries the user already wishlisted or owns.
//
// Both are matched with the resolver's own title normalization rather than a
// local rule, so "Hollow Knight: Silksong" from a provider and a hand-typed
// wishlist entry collapse to the same key the resolver would use.
func (s *Server) annotateOwnership(entries []calendarEntry) {
	if len(entries) == 0 {
		return
	}

	wishlisted := make(map[string]bool)
	for _, wl := range s.mgr.Jobs().GetWishlist() {
		wishlisted[metadata.NormalizeForMatch(wl.Title)] = true
	}

	owned := make(map[string]bool)
	for key := range s.mgr.Jobs().GetAllLibraryTitles() {
		// Keys are "lowercased title|platform_slug"; the calendar has no
		// platform to match against, so only the title half is used.
		if idx := strings.LastIndex(key, "|"); idx >= 0 {
			owned[metadata.NormalizeForMatch(key[:idx])] = true
		}
	}

	for i := range entries {
		k := metadata.NormalizeForMatch(entries[i].Name)
		entries[i].OnWishlist = wishlisted[k]
		entries[i].InLibrary = owned[k]
	}
}

func slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
