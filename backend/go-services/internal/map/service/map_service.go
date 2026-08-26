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
	osrmURL     string
	photonURL   string
	httpClient  *http.Client
	tileSources map[string]string
}

// NewMapService creates the proxy. In production the style URL can be set
// via MAPLIBRE_STYLE_URL or TILESERVER_URL to point at self-hosted TileServer GL
// (e.g. http://omnigo-tileserver:8080/styles/streets/style.json), or fall back to MapTiler or DemoTiles.
func NewMapService(apiKey, styleURL string) *MapService {
	if styleURL == "" && apiKey != "" {
		styleURL = fmt.Sprintf("https://api.maptiler.com/maps/streets/style.json?key=%s", apiKey)
	}
	if styleURL == "" && apiKey == "" {
		// Resilient zero-config fallback to OpenMapTiles demo style
		styleURL = "https://demotiles.maplibre.org/style.json"
	}

	osrmURL := "http://omnigo-osrm:5000"
	photonURL := "http://omnigo-photon:2322"

	tileSources := map[string]string{
		"tiles":           "https://api.maptiler.com/maps/streets/%s?key=" + apiKey,
		"openmaptiles":    "https://api.maptiler.com/tiles/v3/%s?key=" + apiKey,
		"maptiler_planet": "https://api.maptiler.com/tiles/v3/%s?key=" + apiKey,
		"streets":         "https://api.maptiler.com/maps/streets/%s?key=" + apiKey,
		"basic":           "https://api.maptiler.com/maps/basic/%s?key=" + apiKey,
		"satellite":       "https://api.maptiler.com/maps/hybrid/%s?key=" + apiKey,
		"terrain":         "https://api.maptiler.com/maps/terrain/%s?key=" + apiKey,
	}

	// Auto-configure endpoints if using self-hosted TileServer GL / Martin container
	if styleURL != "" && (!strings.Contains(styleURL, "maptiler.com") || strings.Contains(styleURL, "tileserver")) {
		baseURL := styleURL
		if idx := strings.Index(styleURL, "/styles/"); idx != -1 {
			baseURL = styleURL[:idx]
		}
		tileSources["tiles"] = baseURL + "/data/v3/%s"
		tileSources["openmaptiles"] = baseURL + "/data/v3/%s"
		tileSources["maptiler_planet"] = baseURL + "/data/v3/%s"
		tileSources["streets"] = baseURL + "/data/v3/%s"
		tileSources["basic"] = baseURL + "/data/v3/%s"
	}

	return &MapService{
		apiKey:     apiKey,
		styleURL:   styleURL,
		osrmURL:    osrmURL,
		photonURL:  photonURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		tileSources: tileSources,
	}
}

// SetEndpoints allows overriding OSRM and Photon endpoints dynamically.
func (s *MapService) SetEndpoints(osrmURL, photonURL string) {
	if osrmURL != "" {
		s.osrmURL = osrmURL
	}
	if photonURL != "" {
		s.photonURL = photonURL
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

// rewriteStyle walks the style JSON and replaces any upstream tile/
// glyph/sprite URLs with internal proxy URLs, injecting explicit tiles: [...] arrays.
func (s *MapService) rewriteStyle(style map[string]interface{}, baseURL string) map[string]interface{} {
	// Rewrite top-level glyphs and sprites.
	if glyphs, ok := style["glyphs"].(string); ok {
		style["glyphs"] = s.rewriteGlyphsURL(glyphs, baseURL)
	}
	if sprites, ok := style["sprite"].(string); ok {
		style["sprite"] = s.rewriteSpriteURL(sprites, baseURL)
	}

	// Rewrite sources: ensure explicit "tiles" array is injected for vector and raster sources.
	if sources, ok := style["sources"].(map[string]interface{}); ok {
		for name, src := range sources {
			if srcMap, ok := src.(map[string]interface{}); ok {
				srcType, _ := srcMap["type"].(string)
				ext := "pbf"
				if srcType == "raster" {
					ext = "png"
				}

				// Always inject full tiles array pointing to internal proxy
				srcMap["tiles"] = []string{
					fmt.Sprintf("%s/tiles/%s/{z}/{x}/{y}.%s", baseURL, name, ext),
				}
				// Remove metadata URL so MapLibre native engine doesn't attempt TileJSON lookup
				delete(srcMap, "url")
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
	return fmt.Sprintf("%s/tiles/%s/{z}/{x}/{y}.pbf", baseURL, sourceName)
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
		// Fallback to default vector tile pattern
		pattern = s.tileSources["tiles"]
		if pattern == "" {
			pattern = s.tileSources["openmaptiles"]
		}
		if pattern == "" {
			return fmt.Errorf("unknown tile source: %s", source)
		}
	}

	ext := "pbf"
	if source == "satellite" || source == "terrain" {
		ext = "png"
	}
	upstream := fmt.Sprintf(pattern, fmt.Sprintf("%d/%d/%d.%s", z, x, y, ext))

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

	var upstream string
	if !strings.Contains(s.styleURL, "maptiler.com") {
		// Self-hosted TileServer GL / Martin fonts endpoint
		baseURL := s.styleURL
		if idx := strings.Index(s.styleURL, "/styles/"); idx != -1 {
			baseURL = s.styleURL[:idx]
		}
		upstream = fmt.Sprintf("%s/fonts/%s/%d-%d.pbf", baseURL, url.PathEscape(fontstack), start, end)
	} else {
		// MapTiler glyphs endpoint
		upstream = fmt.Sprintf("https://api.maptiler.com/fonts/%s/%d-%d.pbf?key=%s", url.PathEscape(fontstack), start, end, s.apiKey)
	}

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
	spriteName := "sprite"
	if strings.Contains(id, "@2x") {
		spriteName = "sprite@2x"
	}

	var upstream string
	if !strings.Contains(s.styleURL, "maptiler.com") {
		// Self-hosted TileServer GL / Martin sprite endpoint
		baseURL := s.styleURL
		if idx := strings.Index(s.styleURL, "/styles/"); idx != -1 {
			baseURL = s.styleURL[:idx]
		}
		upstream = fmt.Sprintf("%s/styles/streets/%s.%s", baseURL, spriteName, ext)
	} else {
		// MapTiler default sprite endpoint
		upstream = fmt.Sprintf("https://api.maptiler.com/maps/streets/%s.%s?key=%s", spriteName, ext, s.apiKey)
	}

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

// GetRoute fetches a turn-by-turn routing payload from OSRM backend.
func (s *MapService) GetRoute(ctx context.Context, profile, coordinates string) ([]byte, error) {
	if profile == "" {
		profile = "driving"
	}
	// OSRM URL format: /route/v1/{profile}/{coordinates}?overview=full&geometries=geojson&steps=true
	targetURL := fmt.Sprintf("%s/route/v1/%s/%s?overview=full&geometries=geojson&steps=true", s.osrmURL, profile, coordinates)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "OMNIGO-MapService/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("osrm routing request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("osrm returned status %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

// SearchGeocode queries Photon/Nominatim for matching addresses and landmarks.
func (s *MapService) SearchGeocode(ctx context.Context, query string, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = 10
	}
	targetURL := fmt.Sprintf("%s/api?q=%s&limit=%d", s.photonURL, url.QueryEscape(query), limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "OMNIGO-MapService/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("photon geocode request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("photon returned status %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

// ReverseGeocode converts GPS lat/lon to address information via Photon/Nominatim.
func (s *MapService) ReverseGeocode(ctx context.Context, lat, lon float64) ([]byte, error) {
	targetURL := fmt.Sprintf("%s/reverse?lat=%f&lon=%f", s.photonURL, lat, lon)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "OMNIGO-MapService/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("photon reverse geocode request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("photon reverse returned status %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}

// HealthCheck verifies availability of upstream map components.
func (s *MapService) HealthCheck(ctx context.Context) map[string]interface{} {
	health := map[string]interface{}{
		"status":      "ok",
		"service":     "map-service",
		"style_url":   s.styleURL,
		"osrm_url":    s.osrmURL,
		"photon_url":  s.photonURL,
		"configured":  s.IsConfigured(),
	}
	return health
}
