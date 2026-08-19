package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MapService proxies MapLibre-related requests (style, tiles, glyphs, sprites)
// so the mobile app never needs to embed an API key directly. It also gives us
// a migration path from MapTiler to a self-hosted OpenMapTiles stack.
type MapService struct {
	apiKey      string
	styleURL    string
	httpClient  *http.Client
	tileSources map[string]string
}

// NewMapService creates the proxy with a MapTiler key now. In production the
// style URL can be overridden with MAPLIBRE_STYLE_URL to point at self-hosted
// OpenMapTiles / tileserver-gl.
func NewMapService(apiKey, styleURL string) *MapService {
	if styleURL == "" && apiKey != "" {
		styleURL = fmt.Sprintf("https://api.maptiler.com/maps/streets/style.json?key=%s", apiKey)
	}

	return &MapService{
		apiKey:   apiKey,
		styleURL: styleURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		tileSources: map[string]string{
			"tiles":     "https://api.maptiler.com/maps/streets/%s?key=" + apiKey,
			"satellite": "https://api.maptiler.com/maps/hybrid/%s?key=" + apiKey,
			"terrain":   "https://api.maptiler.com/maps/terrain/%s?key=" + apiKey,
		},
	}
}

// GetStyleJSON fetches the upstream style.json and rewrites URLs to point at
// this internal proxy so the API key stays server-side.
func (s *MapService) GetStyleJSON(ctx context.Context, baseURL string) ([]byte, error) {
	if s.styleURL == "" {
		return nil, fmt.Errorf("MAPLIBRE_STYLE_URL or MAPLIBRE_API_KEY is not configured")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.styleURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "OMNIGO-MapService/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream style request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream style returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var style map[string]interface{}
	if err := json.Unmarshal(body, &style); err != nil {
		// If we cannot parse it, just return the raw body — some custom styles may
		// not be JSON (e.g. future yaml). For now we require JSON.
		return nil, fmt.Errorf("failed to decode style.json: %w", err)
	}

	rewritten := s.rewriteStyle(style, baseURL)

	out, err := json.Marshal(rewritten)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// rewriteStyle walks the style JSON and replaces any upstream MapTiler tile/
// glyph/sprite URLs with internal proxy URLs.
func (s *MapService) rewriteStyle(style map[string]interface{}, baseURL string) map[string]interface{} {
	// Rewrite top-level glyphs and sprites.
	if glyphs, ok := style["glyphs"].(string); ok {
		style["glyphs"] = s.rewriteGlyphsURL(glyphs, baseURL)
	}
	if sprites, ok := style["sprite"].(string); ok {
		style["sprite"] = s.rewriteSpriteURL(sprites, baseURL)
	}

	// Rewrite sources.
	if sources, ok := style["sources"].(map[string]interface{}); ok {
		for name, src := range sources {
			if srcMap, ok := src.(map[string]interface{}); ok {
				if tiles, ok := srcMap["tiles"].([]interface{}); ok {
					var rewrittenTiles []string
					for _, t := range tiles {
						if tileStr, ok := t.(string); ok {
							rewrittenTiles = append(rewrittenTiles, s.rewriteTileURL(tileStr, name, baseURL))
						} else {
							rewrittenTiles = append(rewrittenTiles, fmt.Sprintf("%s/tiles/%s/{z}/{x}/{y}", baseURL, name))
						}
					}
					srcMap["tiles"] = rewrittenTiles
				}
				if urlStr, ok := srcMap["url"].(string); ok {
					srcMap["url"] = s.rewriteTileURL(urlStr, name, baseURL)
				}
			}
		}
	}

	return style
}

func (s *MapService) rewriteTileURL(upstream, sourceName, baseURL string) string {
	// If the upstream is already our proxy, skip.
	if strings.Contains(upstream, baseURL) {
		return upstream
	}
	return fmt.Sprintf("%s/tiles/%s/{z}/{x}/{y}", baseURL, sourceName)
}

func (s *MapService) rewriteGlyphsURL(upstream, baseURL string) string {
	if strings.Contains(upstream, baseURL) {
		return upstream
	}
	return fmt.Sprintf("%s/glyphs/{fontstack}/{start}-{end}.pbf", baseURL)
}

func (s *MapService) rewriteSpriteURL(upstream, baseURL string) string {
	if strings.Contains(upstream, baseURL) {
		return upstream
	}
	return fmt.Sprintf("%s/sprites/default", baseURL)
}

// ProxyTile forwards a raster/vector tile request to the configured upstream.
// It preserves caching headers and content type.
func (s *MapService) ProxyTile(ctx context.Context, source string, z, x, y int, w http.ResponseWriter) error {
	pattern, ok := s.tileSources[source]
	if !ok {
		return fmt.Errorf("unknown tile source: %s", source)
	}

	// MapTiler tile URLs look like: https://api.maptiler.com/maps/streets/{z}/{x}/{y}.png?key=...
	upstream := fmt.Sprintf(pattern, fmt.Sprintf("%d/%d/%d.png", z, x, y))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "OMNIGO-MapService/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upstream tile request failed: %w", err)
	}
	defer resp.Body.Close()

	copyHeader(w.Header(), resp.Header, "Content-Type", "Cache-Control", "Expires", "ETag", "Last-Modified")
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
}

// ProxyGlyphs forwards a font glyph PBF request.
func (s *MapService) ProxyGlyphs(ctx context.Context, fontstack string, start, end int, w http.ResponseWriter) error {
	if s.styleURL == "" {
		return fmt.Errorf("map service not configured")
	}
	// Derive glyph base from style URL host. For MapTiler it is usually:
	// https://api.maptiler.com/fonts/{fontstack}/{start}-{end}.pbf?key=...
	upstream := fmt.Sprintf("https://api.maptiler.com/fonts/%s/%d-%d.pbf?key=%s", url.PathEscape(fontstack), start, end, s.apiKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "OMNIGO-MapService/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upstream glyph request failed: %w", err)
	}
	defer resp.Body.Close()

	copyHeader(w.Header(), resp.Header, "Content-Type", "Cache-Control", "Expires", "ETag")
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
}

// ProxySprite forwards sprite JSON / PNG requests.
func (s *MapService) ProxySprite(ctx context.Context, id string, ext string, w http.ResponseWriter) error {
	if s.styleURL == "" {
		return fmt.Errorf("map service not configured")
	}
	// MapTiler default sprite endpoint.
	upstream := fmt.Sprintf("https://api.maptiler.com/maps/stiles/sprite%s.%s?key=%s", id, ext, s.apiKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "OMNIGO-MapService/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upstream sprite request failed: %w", err)
	}
	defer resp.Body.Close()

	copyHeader(w.Header(), resp.Header, "Content-Type", "Cache-Control", "Expires", "ETag")
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
}

func copyHeader(dst, src http.Header, keys ...string) {
	for _, k := range keys {
		if v := src.Get(k); v != "" {
			dst.Set(k, v)
		}
	}
}

// IsConfigured reports whether the map service has the minimum config to run.
func (s *MapService) IsConfigured() bool {
	return s.styleURL != ""
}
