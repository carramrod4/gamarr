package metadata

import (
	"context"
	"fmt"
	"net/http"
)

// rawgProvider adapts the pre-existing RAWG Client to the Provider interface.
//
// RAWG is the fallback rather than the primary source: it still has the
// broadest retro/console coverage of the three, but its ratings are on a 0-5
// scale and its descriptions are frequently truncated marketing copy, so IGDB
// wins any field both can supply.
type rawgProvider struct {
	client *Client
}

func newRAWGProvider(apiKey string, hc *http.Client) *rawgProvider {
	c := NewClient(apiKey)
	if hc != nil {
		c.httpClient = hc
	}
	return &rawgProvider{client: c}
}

func (p *rawgProvider) Name() string { return providerRAWG }

func (p *rawgProvider) Enabled() bool { return p.client.Enabled() }

func (p *rawgProvider) Search(ctx context.Context, query, platformSlug string) (*Game, error) {
	meta, err := p.client.searchGameCtx(ctx, query, platformSlug)
	if err != nil || meta == nil {
		return nil, err
	}
	return fromGameMetadata(meta, providerRAWG), nil
}

func (p *rawgProvider) GetByID(ctx context.Context, id string) (*Game, error) {
	numeric := atoiSafe(id)
	if numeric <= 0 {
		return nil, fmt.Errorf("invalid RAWG id %q", id)
	}
	meta, err := p.client.getGameCtx(ctx, numeric)
	if err != nil || meta == nil {
		return nil, err
	}
	return fromGameMetadata(meta, providerRAWG), nil
}

// GetCoverArt returns the background image RAWG already supplied. RAWG has no
// separate cover-art endpoint, so a search/detail result is the only source.
func (p *rawgProvider) GetCoverArt(_ context.Context, g *Game) (string, error) {
	if g == nil {
		return "", nil
	}
	return g.CoverArt, nil
}
