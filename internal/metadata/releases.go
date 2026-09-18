package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Page-size ceilings imposed by each upstream API: IGDB's Apicalypse `limit`
// caps at 500, and RAWG rejects a page_size above 40.
const (
	igdbMaxPageSize = 500
	rawgMaxPageSize = 40
)

// defaultWindowLimit caps how many games one window query will collect across
// all pages.
//
// Pagination is not optional here, and the number is not arbitrary: IGDB holds
// roughly 5,000 main-game releases in a 90-day span (measured live, against the
// real API - 889 in the next 90 days, 5,018 in the previous 90). A single
// 500-row page sorted by date therefore covers only the first few days of the
// window and silently drops the rest, which is exactly how the first version of
// this code returned an empty calendar for every request. 10,000 leaves real
// headroom over a 90-day window while still bounding a runaway query.
const defaultWindowLimit = 10000

// ReleaseBrowser is an optional Provider capability: listing games by release
// window rather than by title.
//
// It is a separate interface rather than three more methods on Provider because
// not every source can answer it - Steam's storefront API is lookup-by-appid
// only, with no date-range query at all - and widening Provider would force a
// stub implementation that can only ever return nothing.
type ReleaseBrowser interface {
	// ReleasesBetween lists games whose first release date falls in
	// [from, to], oldest first, capped at limit.
	ReleasesBetween(ctx context.Context, from, to time.Time, limit int) ([]*Game, error)
}

// CanBrowseReleases reports whether any enabled provider can answer a release
// window query.
//
// This is deliberately narrower than Enabled(): Steam needs no credentials, so
// Enabled() is true even with nothing else configured, yet Steam's storefront
// API has no date-range query at all. Callers that treat Enabled() as "the
// calendar will work" get an empty calendar with no explanation, which is the
// exact failure this method exists to let them report.
func (r *Resolver) CanBrowseReleases() bool {
	for _, p := range r.providers {
		if !p.Enabled() {
			continue
		}
		if _, ok := p.(ReleaseBrowser); ok {
			return true
		}
	}
	return false
}

// ReleasesBetween lists releases in a date window, asking providers in priority
// order and returning the first non-empty answer.
//
// Unlike Search, results are NOT merged across providers. A window query
// returns a *set* of games, and two providers' sets overlap only partially -
// merging them would mean pairing rows by fuzzy title match, where a wrong pair
// silently fabricates a release. Taking one provider's complete list keeps the
// calendar internally consistent, and priority order means IGDB answers
// whenever it is configured.
func (r *Resolver) ReleasesBetween(ctx context.Context, from, to time.Time, limit int) ([]*Game, error) {
	var firstErr error
	for _, p := range r.providers {
		if !p.Enabled() {
			continue
		}
		browser, ok := p.(ReleaseBrowser)
		if !ok {
			continue
		}
		games, err := browser.ReleasesBetween(ctx, from, to, limit)
		if err != nil {
			slog.Warn("metadata provider release window failed",
				"provider", p.Name(), "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(games) == 0 {
			continue
		}
		// Attribute every field to the provider that supplied it, so calendar
		// entries carry the same provenance as resolved records.
		out := make([]*Game, 0, len(games))
		for _, g := range games {
			merged := &Game{}
			merged.Merge(g, p.Name())
			out = append(out, merged)
		}
		return out, nil
	}
	return nil, firstErr
}

// ── IGDB ───────────────────────────────────────────────────────────────────────

// ReleasesBetween queries IGDB by first_release_date, paging until the whole
// window is collected.
//
// The window is filtered on first_release_date rather than on the release_dates
// child table deliberately: release_dates holds one row per platform per
// region, so a game released on five platforms would appear five times in the
// window and each row would have to be collapsed back by game id.
func (p *igdbProvider) ReleasesBetween(ctx context.Context, from, to time.Time, limit int) ([]*Game, error) {
	if !p.Enabled() {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultWindowLimit
	}

	var out []*Game
	for offset := 0; offset < limit; offset += igdbMaxPageSize {
		page := igdbMaxPageSize
		if remaining := limit - offset; remaining < page {
			page = remaining
		}

		// game_type 0 keeps mods, ports, bundles and DLC out of a release
		// calendar. rating is deliberately not filtered on: an unreleased game
		// has no rating yet, so requiring one would empty the upcoming half of
		// the calendar entirely.
		body := fmt.Sprintf(
			"%s where first_release_date >= %d & first_release_date <= %d & game_type = %d;"+
				" sort first_release_date asc; limit %d; offset %d;",
			igdbCalendarFields, from.Unix(), to.Unix(), igdbTypeMainGame, page, offset,
		)

		var games []igdbGame
		if err := p.query(ctx, "games", body, &games); err != nil {
			// Pages already collected are still usable, so a failure partway
			// through returns them rather than discarding the whole window.
			if len(out) > 0 {
				slog.Warn("IGDB release window truncated by an error",
					"collected", len(out), "error", err)
				return out, nil
			}
			return nil, err
		}

		for i := range games {
			if games[i].Name == "" {
				continue
			}
			out = append(out, p.toGame(&games[i]))
		}

		// A short page is the last page.
		if len(games) < page {
			return out, nil
		}
	}

	slog.Info("IGDB release window hit its row cap; the far end of the window is incomplete",
		"limit", limit)
	return out, nil
}

// igdbCalendarFields is a deliberately leaner field set than igdbFields: a
// calendar row shows a name, date, platforms, cover and rating, and nothing
// renders the summary. Dropping it matters at this scale - thousands of rows
// per refresh - because summary is by far the largest field in the response.
const igdbCalendarFields = "fields name,slug,game_type,first_release_date,rating," +
	"cover.image_id,platforms.name;"

// ── RAWG ───────────────────────────────────────────────────────────────────────

// ReleasesBetween queries RAWG's dates filter, paging until the window is
// collected. Used only when IGDB is not configured, since the resolver takes
// the first provider that answers.
func (p *rawgProvider) ReleasesBetween(ctx context.Context, from, to time.Time, limit int) ([]*Game, error) {
	if !p.Enabled() {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultWindowLimit
	}

	var out []*Game
	// RAWG pages by page number, not offset, and caps page_size at 40 - so a
	// window of any useful size is necessarily many requests. rateLimit is
	// called per page for that reason.
	for page := 1; len(out) < limit; page++ {
		p.client.rateLimit()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawgBaseURL+"/games", nil)
		if err != nil {
			return out, err
		}
		q := req.URL.Query()
		q.Set("key", p.client.apiKey)
		q.Set("dates", from.Format("2006-01-02")+","+to.Format("2006-01-02"))
		q.Set("ordering", "released")
		q.Set("page_size", fmt.Sprint(rawgMaxPageSize))
		q.Set("page", fmt.Sprint(page))
		req.URL.RawQuery = q.Encode()
		req.Header.Set("User-Agent", userAgent)

		resp, err := p.client.httpClient.Do(req)
		if err != nil {
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		var data rawgSearchResponse
		status := resp.StatusCode
		decodeErr := json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()

		if status != http.StatusOK {
			// RAWG answers 404 for a page past the end, which is the normal
			// way this loop finishes rather than an error worth surfacing.
			if status == http.StatusNotFound && len(out) > 0 {
				return out, nil
			}
			if len(out) > 0 {
				return out, nil
			}
			return nil, fmt.Errorf("RAWG API HTTP %d", status)
		}
		if decodeErr != nil {
			if len(out) > 0 {
				return out, nil
			}
			return nil, decodeErr
		}

		for i := range data.Results {
			if data.Results[i].Name == "" {
				continue
			}
			out = append(out, fromGameMetadata(rawgGameToMetadata(&data.Results[i]), providerRAWG))
		}

		if len(data.Results) < rawgMaxPageSize {
			return out, nil
		}
	}
	return out, nil
}

// SortByReleaseDate orders games oldest-first by release date.
//
// Dates are ISO "YYYY-MM-DD" strings, so a lexical compare is also a
// chronological one. Entries with no date sort last rather than first, where a
// plain string sort would put them, since an undated row at the top of a
// calendar is the least useful thing the list could lead with.
func SortByReleaseDate(games []*Game) {
	sort.SliceStable(games, func(i, j int) bool {
		a, b := games[i].ReleaseDate, games[j].ReleaseDate
		if (a == "") != (b == "") {
			return b == ""
		}
		return a < b
	})
}

// NormalizeForMatch exposes the same title normalization the resolver uses, so
// callers cross-referencing games against their own records (a wishlist, a
// library) match titles the same way the resolver does instead of inventing a
// second, subtly different rule.
func NormalizeForMatch(title string) string {
	return normalizeTitle(strings.TrimSpace(title))
}
