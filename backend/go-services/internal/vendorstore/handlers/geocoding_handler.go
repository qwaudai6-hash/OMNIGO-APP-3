package handlers

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const geocodingCacheTTL = 24 * time.Hour

// rateLimitBucket is a tiny stdlib token-bucket per client IP.
type rateLimitBucket struct {
	tokens   int
	lastSeen time.Time
}

const (
	rateLimitMaxTokens = 10
	rateLimitRefillSec = 60
)

var (
	rateLimitMu      sync.Mutex
	rateLimitBuckets = make(map[string]*rateLimitBucket)
)

type GeocodingHandler struct {
	rdb        redis.UniversalClient
	photonURL  string
	httpClient *http.Client
}

func init() {
	// Cleanup stale rate-limit buckets every 10 minutes to prevent OOM
	go func() {
		for {
			time.Sleep(10 * time.Minute)
			rateLimitMu.Lock()
			cutoff := time.Now().Add(-5 * time.Minute)
			for ip, b := range rateLimitBuckets {
				if b.lastSeen.Before(cutoff) {
					delete(rateLimitBuckets, ip)
				}
			}
			rateLimitMu.Unlock()
		}
	}()
}

func NewGeocodingHandler() *GeocodingHandler {
	return &GeocodingHandler{
		photonURL: getEnv("PHOTON_URL", "http://omnigo-photon:2322"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func NewGeocodingHandlerWithCache(rdb redis.UniversalClient) *GeocodingHandler {
	return &GeocodingHandler{
		rdb:       rdb,
		photonURL: getEnv("PHOTON_URL", "http://omnigo-photon:2322"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func allowRequest(clientIP string) bool {
	rateLimitMu.Lock()
	defer rateLimitMu.Unlock()

	now := time.Now()
	b, ok := rateLimitBuckets[clientIP]
	if !ok {
		rateLimitBuckets[clientIP] = &rateLimitBucket{tokens: rateLimitMaxTokens - 1, lastSeen: now}
		return true
	}

	elapsed := now.Sub(b.lastSeen).Seconds()
	refill := int(elapsed / rateLimitRefillSec * rateLimitMaxTokens)
	if refill > 0 {
		b.tokens += refill
		if b.tokens > rateLimitMaxTokens {
			b.tokens = rateLimitMaxTokens
		}
	}
	b.lastSeen = now

	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

// fetchGeocoding tries Photon first, then falls back to public Nominatim
// if Photon is unreachable. Photon returns results in a different format
// (`features` array with `geometry.coordinates`) than Nominatim, so we
// normalise both to the Nominatim shape the Flutter app already expects.
func (h *GeocodingHandler) fetchGeocoding(ctx context.Context, query string) ([]map[string]interface{}, error) {
	// 1. Try Photon first (self-hosted, no rate limit).
	if h.photonURL != "" {
		photonURL := fmt.Sprintf("%s/api?q=%s&limit=1", h.photonURL, url.QueryEscape(query))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, photonURL, nil)
		if err == nil {
			req.Header.Set("User-Agent", "OmnigoSuperApp-BackendProxy/1.0")
			resp, err := h.httpClient.Do(req)
			if err == nil {
				defer resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					body, _ := io.ReadAll(resp.Body)
					var photonResp struct {
						Features []struct {
							Geometry struct {
								Coordinates []float64 `json:"coordinates"`
							} `json:"geometry"`
							Properties struct {
								Name        string `json:"name"`
								Street      string `json:"street"`
								City        string `json:"city"`
								State       string `json:"state"`
								Country     string `json:"country"`
								CountryCode string `json:"countrycode"`
								Postcode    string `json:"postcode"`
								OsmType     string `json:"osm_type"`
								OsmID       int64  `json:"osm_id"`
							} `json:"properties"`
						} `json:"features"`
					}
					if json.Unmarshal(body, &photonResp) == nil && len(photonResp.Features) > 0 {
						results := make([]map[string]interface{}, 0, len(photonResp.Features))
						for _, f := range photonResp.Features {
							lat := f.Geometry.Coordinates[1]
							lng := f.Geometry.Coordinates[0]
							parts := []string{}
							if f.Properties.Name != "" {
								parts = append(parts, f.Properties.Name)
							}
							if f.Properties.Street != "" {
								parts = append(parts, f.Properties.Street)
							}
							if f.Properties.City != "" {
								parts = append(parts, f.Properties.City)
							}
							if f.Properties.State != "" {
								parts = append(parts, f.Properties.State)
							}
							if f.Properties.Country != "" {
								parts = append(parts, f.Properties.Country)
							}
							results = append(results, map[string]interface{}{
								"lat":             fmt.Sprintf("%f", lat),
								"lon":             fmt.Sprintf("%f", lng),
								"display_name":    joinNonEmpty(parts, ", "),
								"display_place":   f.Properties.Name,
								"display_address": joinNonEmpty([]string{f.Properties.Street, f.Properties.City, f.Properties.State, f.Properties.Country}, ", "),
								"type":            f.Properties.OsmType,
								"osm_id":          f.Properties.OsmID,
								"source":          "photon",
							})
						}
						return results, nil
					}
				}
			}
		}
		// Photon errored or returned empty — fall through to public Nominatim.
	}

	// 2. Fallback to public Nominatim (no API key needed but rate-limited).
	targetURL := fmt.Sprintf("https://nominatim.openstreetmap.org/search?format=json&limit=1&q=%s", url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "OmnigoSuperApp-BackendProxy/1.0")
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var results []map[string]interface{}
	if err := json.Unmarshal(body, &results); err != nil {
		return nil, err
	}
	for i := range results {
		results[i]["source"] = "nominatim"
	}
	return results, nil
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i > 0 && out != "" {
			out += sep
		}
		out += p
	}
	return out
}

// Search proxies requests to Photon (self-hosted) with Redis caching.
// Falls back to public Nominatim if Photon is unreachable.
func (h *GeocodingHandler) Search(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing 'q' parameter"})
		return
	}

	// Redis cache check
	cacheKey := fmt.Sprintf("geocoding:search:%x", md5.Sum([]byte(query)))
	if h.rdb != nil {
		if cached, err := h.rdb.Get(c.Request.Context(), cacheKey).Bytes(); err == nil {
			var data interface{}
			if json.Unmarshal(cached, &data) == nil {
				c.JSON(http.StatusOK, data)
				return
			}
		}
	}

	clientIP := c.ClientIP()
	if !allowRequest(clientIP) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
		return
	}

	results, err := h.fetchGeocoding(c.Request.Context(), query)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to contact upstream geocoding service"})
		return
	}

	// Cache the JSON response in Redis
	if h.rdb != nil {
		if body, err := json.Marshal(results); err == nil {
			h.rdb.Set(context.Background(), cacheKey, body, geocodingCacheTTL)
		}
	}

	c.JSON(http.StatusOK, results)
}

func (h *GeocodingHandler) RegisterRoutes(router *gin.Engine) {
	geocoding := router.Group("/api/v1/geocoding")
	{
		geocoding.GET("/search", h.Search)
	}
}
