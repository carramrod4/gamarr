package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const steamStoreBaseURL = "https://store.steampowered.com/api"

// steamProvider reads Steam's public storefront endpoints. These need no API
// key at all, which is why Steam is always enabled.
//
// Steam only knows about PC titles, so this provider deliberately declines
// any request carrying a non-PC platform slug rather than returning a
// confidently wrong match for a console game that happens to share a name
// with a Steam release.
type steamProvider struct {
	httpClient *http.Client
}

func newSteamProvider(hc *http.Client) *steamProvider {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &steamProvider{httpClient: hc}
}

func (p *steamProvider) Name() string { return providerSteam }

// Enabled is always true: Steam's storefront API requires no credentials.
func (p *steamProvider) Enabled() bool { return true }

// handlesPlatform reports whether Steam can say anything useful about this
// platform. An empty slug is accepted because an unqualified search may still
// be a PC title, and the resolver merges Steam's answer at lowest priority.
func (p *steamProvider) handlesPlatform(slug string) bool {
	return slug == "" || slug == "pc"
}

func (p *steamProvider) Search(ctx context.Context, query, platformSlug string) (*Game, error) {
	if !p.handlesPlatform(platformSlug) {
		return nil, nil
	}

	q := url.Values{}
	q.Set("term", query)
	q.Set("cc", "us")
	q.Set("l", "en")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		steamStoreBaseURL+"/storesearch/?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Steam search HTTP %d", resp.StatusCode)
	}

	var data struct {
		Total int `json:"total"`
		Items []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if len(data.Items) == 0 {
		return nil, nil
	}

	// storesearch returns bundles/DLC alongside games, so consider only real
	// apps, then prefer an exact title match over Steam's own ranking.
	var apps []int // indices into data.Items
	var titles []string
	for i, item := range data.Items {
		if item.Type != "" && item.Type != "app" {
			continue
		}
		apps = append(apps, i)
		titles = append(titles, item.Name)
	}
	if len(apps) == 0 {
		return nil, nil
	}
	best := exactTitleMatch(query, titles)
	if best < 0 {
		best = 0
	}

	// storesearch carries almost no detail, so go straight to appdetails for
	// the chosen app rather than returning a nearly-empty record.
	return p.GetByID(ctx, itoaSafe(data.Items[apps[best]].ID))
}

func (p *steamProvider) GetByID(ctx context.Context, id string) (*Game, error) {
	appID := atoiSafe(id)
	if appID <= 0 {
		return nil, fmt.Errorf("invalid Steam appid %q", id)
	}

	q := url.Values{}
	q.Set("appids", itoaSafe(appID))
	q.Set("cc", "us")
	q.Set("l", "en")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		steamStoreBaseURL+"/appdetails?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Steam appdetails HTTP %d", resp.StatusCode)
	}

	// appdetails is keyed by the appid as a string, with a per-app success
	// flag: a missing/unreleased app returns {"<id>":{"success":false}}.
	var envelope map[string]struct {
		Success bool            `json:"success"`
		Data    steamAppDetails `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, err
	}

	entry, ok := envelope[itoaSafe(appID)]
	if !ok || !entry.Success {
		return nil, nil
	}
	return p.toGame(appID, &entry.Data), nil
}

// GetCoverArt returns the header image resolved during Search/GetByID.
func (p *steamProvider) GetCoverArt(_ context.Context, g *Game) (string, error) {
	if g == nil {
		return "", nil
	}
	return g.CoverArt, nil
}

type steamAppDetails struct {
	Name             string   `json:"name"`
	ShortDescription string   `json:"short_description"`
	HeaderImage      string   `json:"header_image"`
	Developers       []string `json:"developers"`
	Publishers       []string `json:"publishers"`
	Platforms        struct {
		Windows bool `json:"windows"`
		Mac     bool `json:"mac"`
		Linux   bool `json:"linux"`
	} `json:"platforms"`
	Metacritic struct {
		Score int `json:"score"`
	} `json:"metacritic"`
	Genres []struct {
		Description string `json:"description"`
	} `json:"genres"`
	ReleaseDate struct {
		ComingSoon bool   `json:"coming_soon"`
		Date       string `json:"date"`
	} `json:"release_date"`
}

func (p *steamProvider) toGame(appID int, d *steamAppDetails) *Game {
	g := &Game{
		Title: d.Name,
		// Steam returns short_description HTML-entity-encoded (verified live:
		// Portal 2's contains &quot;), which would otherwise render literally
		// in the UI, since no consumer of this field treats it as HTML.
		Description: truncate(html.UnescapeString(d.ShortDescription), 1000),
		CoverArt:    d.HeaderImage,
		Metacritic:  d.Metacritic.Score,
		Developers:  d.Developers,
		Publishers:  d.Publishers,
		ReleaseDate: normalizeSteamDate(d.ReleaseDate.Date),
	}
	// Steam's appdetails exposes no aggregate user score (only the separate,
	// undocumented appreviews endpoint does), so Rating is deliberately left
	// at 0 here and filled from IGDB/RAWG by the resolver. Metacritic is a
	// critic score, not a user rating, so it is not reused as one.
	for _, gn := range d.Genres {
		g.Genres = append(g.Genres, gn.Description)
	}
	if d.Platforms.Windows || d.Platforms.Mac || d.Platforms.Linux {
		g.Platforms = append(g.Platforms, "PC")
	}
	g.addProviderID(providerSteam, itoaSafe(appID))
	return g
}

// normalizeSteamDate converts Steam's human-readable release date
// ("Nov 10, 2020", "10 Nov, 2020") into the ISO form the canonical record
// uses. Anything unparseable (notably vague values like "Q4 2026" or
// "Coming Soon") is passed through unchanged rather than dropped, so the
// information still reaches the UI even when it isn't a real date.
func normalizeSteamDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, layout := range []string{"Jan 2, 2006", "2 Jan, 2006", "January 2, 2006", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return s
}
