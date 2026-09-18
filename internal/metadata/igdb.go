package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	igdbTokenURL = "https://id.twitch.tv/oauth2/token"
	igdbBaseURL  = "https://api.igdb.com/v4"
	igdbImageURL = "https://images.igdb.com/igdb/image/upload/t_cover_big"

	// IGDB documents a hard limit of 4 requests per second per client.
	igdbRateLimit  = 4
	igdbRateWindow = time.Second

	// Refresh the app token this far before its stated expiry so a long
	// request started near the boundary can't race the expiry.
	igdbTokenSkew = 5 * time.Minute
)

// igdbProvider talks to IGDB v4, authenticating with a Twitch app access
// token obtained via the client-credentials grant.
//
// IGDB is the primary provider: its platform/release data is the most
// complete of the three, and its rating is already on a 0-100 scale, so no
// conversion is needed to reach the canonical form.
type igdbProvider struct {
	clientID     string
	clientSecret string
	httpClient   *http.Client

	tokenMu   sync.Mutex
	token     string
	tokenExp  time.Time
	rateMu    sync.Mutex
	rateStamp []time.Time
}

func newIGDBProvider(clientID, clientSecret string, hc *http.Client) *igdbProvider {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &igdbProvider{
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   hc,
		rateStamp:    make([]time.Time, 0, igdbRateLimit),
	}
}

func (p *igdbProvider) Name() string { return providerIGDB }

func (p *igdbProvider) Enabled() bool {
	return p.clientID != "" && p.clientSecret != ""
}

// rateLimit blocks until issuing another request keeps the client within
// IGDB's 4-requests-per-second budget.
//
// This is a sliding window rather than a fixed 250ms spacing: it permits a
// burst of up to 4 immediate requests and only blocks once the window is
// genuinely full, which matches what IGDB actually enforces and keeps a
// 3-provider resolver fan-out from serializing unnecessarily.
func (p *igdbProvider) rateLimit(ctx context.Context) error {
	p.rateMu.Lock()
	defer p.rateMu.Unlock()

	for {
		now := time.Now()
		cutoff := now.Add(-igdbRateWindow)

		// Drop timestamps that have aged out of the window.
		kept := p.rateStamp[:0]
		for _, ts := range p.rateStamp {
			if ts.After(cutoff) {
				kept = append(kept, ts)
			}
		}
		p.rateStamp = kept

		if len(p.rateStamp) < igdbRateLimit {
			p.rateStamp = append(p.rateStamp, now)
			return nil
		}

		// Window is full - wait until the oldest request leaves it.
		wait := p.rateStamp[0].Add(igdbRateWindow).Sub(now)
		if wait <= 0 {
			continue
		}
		timer := time.NewTimer(wait)
		p.rateMu.Unlock()
		select {
		case <-ctx.Done():
			timer.Stop()
			p.rateMu.Lock()
			return ctx.Err()
		case <-timer.C:
		}
		p.rateMu.Lock()
	}
}

// accessToken returns a cached Twitch app token, fetching a new one when the
// current one is missing or close to expiry.
func (p *igdbProvider) accessToken(ctx context.Context) (string, error) {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()

	if p.token != "" && time.Now().Before(p.tokenExp.Add(-igdbTokenSkew)) {
		return p.token, nil
	}

	form := url.Values{}
	form.Set("client_id", p.clientID)
	form.Set("client_secret", p.clientSecret)
	form.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		igdbTokenURL+"?"+form.Encode(), nil)
	if err != nil {
		return "", err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("IGDB token HTTP %d", resp.StatusCode)
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("IGDB token response had no access_token")
	}

	p.token = tok.AccessToken
	p.tokenExp = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return p.token, nil
}

// query runs an Apicalypse query against an IGDB endpoint.
func (p *igdbProvider) query(ctx context.Context, endpoint, body string, out interface{}) error {
	token, err := p.accessToken(ctx)
	if err != nil {
		return err
	}
	if err := p.rateLimit(ctx); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		igdbBaseURL+"/"+endpoint, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Client-ID", p.clientID)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("IGDB API HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// IGDB game_type values. The field was previously called "category"; that
// name is gone in v4 and IGDB silently returns nothing for it rather than
// erroring, so requesting the wrong one fails invisibly.
const (
	igdbTypeMainGame = 0
)

// igdbGame is IGDB's game shape for the fields this provider requests.
type igdbGame struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	// GameType distinguishes a real release from a mod/port/bundle/DLC.
	// 0 is main_game, 5 is mod. Absent decodes to 0, which is the value we
	// would want to assume anyway.
	GameType         int     `json:"game_type"`
	Summary          string  `json:"summary"`
	FirstReleaseDate int64   `json:"first_release_date"` // Unix seconds
	Rating           float64 `json:"rating"`             // already 0-100
	Cover            struct {
		ImageID string `json:"image_id"`
	} `json:"cover"`
	Platforms []struct {
		Name string `json:"name"`
	} `json:"platforms"`
	Genres []struct {
		Name string `json:"name"`
	} `json:"genres"`
	InvolvedCompanies []struct {
		Developer bool `json:"developer"`
		Publisher bool `json:"publisher"`
		Company   struct {
			Name string `json:"name"`
		} `json:"company"`
	} `json:"involved_companies"`
}

const igdbFields = "fields name,slug,game_type,summary,first_release_date,rating,cover.image_id," +
	"platforms.name,genres.name,involved_companies.developer," +
	"involved_companies.publisher,involved_companies.company.name;"

func (p *igdbProvider) Search(ctx context.Context, query, platformSlug string) (*Game, error) {
	if !p.Enabled() {
		return nil, nil
	}

	// Apicalypse has no parameter binding, so the query string is embedded
	// directly - escape any quote in the user's search term so it cannot
	// terminate the string and inject clauses.
	body := fmt.Sprintf("search %q; %s", query, igdbFields)
	if id := mapPlatformSlugToIGDB(platformSlug); id != "" {
		body += fmt.Sprintf(" where platforms = (%s);", id)
	}
	body += " limit 5;"

	var games []igdbGame
	if err := p.query(ctx, "games", body, &games); err != nil {
		return nil, err
	}
	if len(games) == 0 {
		return nil, nil
	}

	return p.toGame(&games[pickIGDBBest(query, games)]), nil
}

// pickIGDBBest chooses which search result actually answers the query.
//
// IGDB's own relevance ordering cannot be trusted for this: verified live,
// searching "Portal 2" returns the PS3 spinoff "Portal 2: In Motion" first and
// the real Portal 2 second, and searching "Chrono Trigger" returns the ROM
// hack "Chrono Trigger+" (game_type 5, parent_game 1802) ahead of the actual
// 1995 release. So main games are preferred over mods/ports/bundles first, and
// an exact title match is preferred within that pool.
//
// Titles are compared after normalization, which strips punctuation - that
// alone would treat "Chrono Trigger+" as an exact match for "Chrono Trigger",
// which is exactly why the game_type filter has to come first.
func pickIGDBBest(query string, games []igdbGame) int {
	pool := make([]int, 0, len(games))
	for i := range games {
		if games[i].GameType == igdbTypeMainGame {
			pool = append(pool, i)
		}
	}
	if len(pool) == 0 {
		// Nothing is a main game (e.g. searching for a mod by name) - fall
		// back to considering everything rather than returning no result.
		for i := range games {
			pool = append(pool, i)
		}
	}

	titles := make([]string, len(pool))
	for i, idx := range pool {
		titles[i] = games[idx].Name
	}
	if m := exactTitleMatch(query, titles); m >= 0 {
		return pool[m]
	}
	return pool[0]
}

func (p *igdbProvider) GetByID(ctx context.Context, id string) (*Game, error) {
	if !p.Enabled() {
		return nil, nil
	}
	numeric := atoiSafe(id)
	if numeric <= 0 {
		return nil, fmt.Errorf("invalid IGDB id %q", id)
	}

	body := fmt.Sprintf("%s where id = %d; limit 1;", igdbFields, numeric)
	var games []igdbGame
	if err := p.query(ctx, "games", body, &games); err != nil {
		return nil, err
	}
	if len(games) == 0 {
		return nil, nil
	}
	return p.toGame(&games[0]), nil
}

// GetCoverArt returns the cover URL already resolved during Search/GetByID.
// IGDB cover URLs are deterministic from the image_id, so no extra call is
// needed once the game has been fetched with cover.image_id in its fields.
func (p *igdbProvider) GetCoverArt(_ context.Context, g *Game) (string, error) {
	if g == nil {
		return "", nil
	}
	return g.CoverArt, nil
}

func (p *igdbProvider) toGame(ig *igdbGame) *Game {
	g := &Game{
		Title:       ig.Name,
		Slug:        ig.Slug,
		Description: truncate(ig.Summary, 1000),
		Rating:      ig.Rating,
	}
	if ig.FirstReleaseDate > 0 {
		g.ReleaseDate = time.Unix(ig.FirstReleaseDate, 0).UTC().Format("2006-01-02")
	}
	if ig.Cover.ImageID != "" {
		g.CoverArt = fmt.Sprintf("%s/%s.jpg", igdbImageURL, ig.Cover.ImageID)
	}
	for _, pl := range ig.Platforms {
		g.Platforms = append(g.Platforms, pl.Name)
	}
	for _, gn := range ig.Genres {
		g.Genres = append(g.Genres, gn.Name)
	}
	for _, ic := range ig.InvolvedCompanies {
		if ic.Company.Name == "" {
			continue
		}
		if ic.Developer {
			g.Developers = append(g.Developers, ic.Company.Name)
		}
		if ic.Publisher {
			g.Publishers = append(g.Publishers, ic.Company.Name)
		}
	}
	g.addProviderID(providerIGDB, itoaSafe(ig.ID))
	return g
}

// mapPlatformSlugToIGDB maps Gamarr platform slugs to IGDB platform IDs.
// IGDB platform IDs: https://api.igdb.com/v4/platforms
func mapPlatformSlugToIGDB(slug string) string {
	m := map[string]string{
		"pc":       "6",
		"psx":      "7",
		"ps2":      "8",
		"ps3":      "9",
		"ps4":      "48",
		"ps5":      "167",
		"psp":      "38",
		"vita":     "46",
		"xbox":     "11",
		"xbox360":  "12",
		"xboxone":  "49",
		"switch":   "130",
		"wii":      "5",
		"wiiu":     "41",
		"n64":      "4",
		"ngc":      "21",
		"nes":      "18",
		"snes":     "19",
		"gb":       "33",
		"gbc":      "22",
		"gba":      "24",
		"nds":      "20",
		"3ds":      "37",
		"genesis":  "29",
		"dc":       "23",
		"saturn":   "32",
		"sms":      "64",
		"gamegear": "35",
	}
	return m[slug]
}
