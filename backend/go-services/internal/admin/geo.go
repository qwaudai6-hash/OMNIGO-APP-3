package admin

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// readAll reads the full body of an HTTP response and returns it as bytes.
func readAll(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}

// GeoService proxies OpenStreetMap geocoding so the upstream URL and
// rate limits stay server-side instead of shipped in the Flutter app.
//
// Primary backend: Photon (self-hosted, see `docker-compose.yml`).
// Fallback: public Nominatim if Photon is unreachable.
//
// ponytail: simple proxy, no caching layer yet — add in-memory/redis cache if QPS bites.
type GeoService struct {
	photonURL    string
	nominatimURL string
	userAgent    string
	httpClient   *http.Client
}

func NewGeoService() *GeoService {
	photon := os.Getenv("PHOTON_URL")
	if photon == "" {
		photon = "http://omnigo-photon:2322"
	}
	nominatim := os.Getenv("NOMINATIM_BASE_URL")
	if nominatim == "" {
		nominatim = "https://nominatim.openstreetmap.org"
	}
	ua := os.Getenv("NOMINATIM_USER_AGENT")
	if ua == "" {
		ua = "OMNIGOApp/1.0 (geocode proxy)"
	}
	return &GeoService{
		photonURL:    photon,
		nominatimURL: nominatim,
		userAgent:    ua,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

// ReverseGeocode turns lat/lng into a human-readable address. Tries
// Photon first, falls back to Nominatim if Photon returns no result or
// is unreachable.
func (g *GeoService) ReverseGeocode(c *gin.Context) {
	lat := c.Query("lat")
	lng := c.Query("lng")
	if lat == "" || lng == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "lat and lng are required"})
		return
	}
	if _, err := strconv.ParseFloat(lat, 64); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "lat must be numeric"})
		return
	}
	if _, err := strconv.ParseFloat(lng, 64); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "lng must be numeric"})
		return
	}

	// 1. Try Photon first. Photon's reverse endpoint is /api/reverse
	//    with `?lon=` and `?lat=` params. It returns the same JSON
	//    shape as Nominatim so the Flutter app can consume it directly.
	if g.photonURL != "" {
		if body, status, ok := g.callPhotonReverse(lat, lng); ok {
			c.Data(status, "application/json", body)
			return
		} else {
			log.Printf("[GEO] photon reverse failed (status=%d), falling back to nominatim", status)
		}
	}

	// 2. Fallback to public Nominatim.
	u, err := url.Parse(g.nominatimURL + "/reverse")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid nominatim base url"})
		return
	}
	q := u.Query()
	q.Set("format", "json")
	q.Set("lat", lat)
	q.Set("lon", lng)
	q.Set("zoom", "18")
	q.Set("addressdetails", "1")
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "request build failed"})
		return
	}
	req.Header.Set("User-Agent", g.userAgent)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		log.Printf("[GEO] nominatim request failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "geocoding service unavailable"})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.JSON(resp.StatusCode, gin.H{"error": "geocoding upstream error", "upstream_status": resp.StatusCode})
		return
	}

	var payload json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to parse geocoding response"})
		return
	}
	c.Data(http.StatusOK, "application/json", payload)
}

// callPhotonReverse tries Photon's `/api/reverse` and returns the raw
// upstream body + status. Photon returns the standard `features` array
// shape (same as the search endpoint) — we normalise to a single object
// the Flutter app already understands.
func (g *GeoService) callPhotonReverse(lat, lng string) (body []byte, status int, ok bool) {
	u := g.photonURL + "/api/reverse?lon=" + url.QueryEscape(lng) + "&lat=" + url.QueryEscape(lat)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, false
	}
	req.Header.Set("User-Agent", g.userAgent)
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, false
	}
	body, err = readAll(resp.Body)
	if err != nil {
		return nil, 0, false
	}
	// Photon returns `{"features": [{"properties": {...}, "geometry": {...}}]}`.
	// Normalise to a single Nominatim-shaped object so the Flutter app
	// doesn't need to know which backend served the response.
	var photonResp struct {
		Features []struct {
			Geometry struct {
				Coordinates []float64 `json:"coordinates"`
			} `json:"geometry"`
			Properties map[string]interface{} `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(body, &photonResp); err != nil {
		return nil, 0, false
	}
	if len(photonResp.Features) == 0 {
		return nil, 200, false
	}
	f := photonResp.Features[0]
	latF := f.Geometry.Coordinates[1]
	lngF := f.Geometry.Coordinates[0]
	name, _ := f.Properties["name"].(string)
	street, _ := f.Properties["street"].(string)
	city, _ := f.Properties["city"].(string)
	state, _ := f.Properties["state"].(string)
	country, _ := f.Properties["country"].(string)
	norm := map[string]interface{}{
		"lat":          latF,
		"lon":          lngF,
		"display_name": joinAddr([]string{name, street, city, state, country}),
		"address": map[string]interface{}{
			"road":         street,
			"city":         city,
			"state":        state,
			"country":      country,
			"country_code": f.Properties["countrycode"],
			"postcode":     f.Properties["postcode"],
		},
		"source": "photon",
	}
	body, err = json.Marshal(norm)
	if err != nil {
		return nil, 0, false
	}
	return body, http.StatusOK, true
}

func joinAddr(parts []string) string {
	out := ""
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i > 0 && out != "" {
			out += ", "
		}
		out += p
	}
	return out
}
