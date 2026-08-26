package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/map/service"
)

// MapHandler exposes the internal MapLibre style/tile/glyph/sprite endpoints.
type MapHandler struct {
	svc *service.MapService
}

func NewMapHandler(svc *service.MapService) *MapHandler {
	return &MapHandler{svc: svc}
}

// StyleJSON serves a rewritten style.json that points all resources at this
// proxy. It accepts an optional access_token query param for gateway auth.
func (h *MapHandler) StyleJSON(c *gin.Context) {
	baseURL := deriveBaseURL(c)

	body, err := h.svc.GetStyleJSON(c.Request.Context(), baseURL)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Type", "application/json")
	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, "application/json", body)
}

// ProxyTile forwards a tile request to the upstream provider.
func (h *MapHandler) ProxyTile(c *gin.Context) {
	source := c.Param("source")
	if source == "" {
		source = "tiles"
	}

	zStr := c.Param("z")
	xStr := c.Param("x")
	yStr := c.Param("y")

	// Strip file extension (.pbf, .png, etc.) from y coordinate if present
	if dotIdx := strings.Index(yStr, "."); dotIdx != -1 {
		yStr = yStr[:dotIdx]
	}

	z, err1 := strconv.Atoi(zStr)
	x, err2 := strconv.Atoi(xStr)
	y, err3 := strconv.Atoi(yStr)
	if err1 != nil || err2 != nil || err3 != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tile coordinates"})
		return
	}

	if err := h.svc.ProxyTile(c.Request.Context(), source, z, x, y, c.Writer); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
}

// ProxyGlyphs forwards glyph PBF requests.
func (h *MapHandler) ProxyGlyphs(c *gin.Context) {
	fontstack := c.Param("fontstack")
	start, err1 := strconv.Atoi(c.Param("start"))
	end, err2 := strconv.Atoi(c.Param("end"))
	if err1 != nil || err2 != nil || fontstack == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid glyph params"})
		return
	}

	if err := h.svc.ProxyGlyphs(c.Request.Context(), fontstack, start, end, c.Writer); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
}

// ProxySprite forwards sprite JSON or PNG requests.
func (h *MapHandler) ProxySprite(c *gin.Context) {
	id := c.Param("id")
	ext := c.Param("ext")
	file := c.Param("file")

	if file != "" {
		ext = parseExtension(file)
		id = strings.TrimSuffix(file, "."+ext)
	}

	if id == "" || (ext != "json" && ext != "png") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sprite request"})
		return
	}

	if err := h.svc.ProxySprite(c.Request.Context(), id, ext, c.Writer); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
}

// Route proxies OSRM turn-by-turn routing requests.
func (h *MapHandler) Route(c *gin.Context) {
	profile := c.DefaultQuery("profile", "driving")
	coords := c.Query("coordinates")
	if coords == "" {
		// Fallback to origin/destination params: ?origin=lat,lng&destination=lat,lng
		origin := c.Query("origin")
		dest := c.Query("destination")
		if origin != "" && dest != "" {
			coords = fmt.Sprintf("%s;%s", origin, dest)
		}
	}

	if coords == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "coordinates parameter required (e.g. lon1,lat1;lon2,lat2)"})
		return
	}

	payload, err := h.svc.GetRoute(c.Request.Context(), profile, coords)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Type", "application/json")
	c.Data(http.StatusOK, "application/json", payload)
}

// Search proxies geocoding search queries to Photon/Nominatim.
func (h *MapHandler) Search(c *gin.Context) {
	q := c.Query("q")
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query parameter 'q' required"})
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	payload, err := h.svc.SearchGeocode(c.Request.Context(), q, limit)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Type", "application/json")
	c.Data(http.StatusOK, "application/json", payload)
}

// Reverse proxies GPS reverse-geocoding queries to Photon/Nominatim.
func (h *MapHandler) Reverse(c *gin.Context) {
	latStr := c.Query("lat")
	lonStr := c.Query("lon")
	if latStr == "" || lonStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "both 'lat' and 'lon' parameters required"})
		return
	}

	lat, err1 := strconv.ParseFloat(latStr, 64)
	lon, err2 := strconv.ParseFloat(lonStr, 64)
	if err1 != nil || err2 != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid lat or lon coordinates"})
		return
	}

	payload, err := h.svc.ReverseGeocode(c.Request.Context(), lat, lon)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Type", "application/json")
	c.Data(http.StatusOK, "application/json", payload)
}

// Health returns runtime map proxy diagnostics.
func (h *MapHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, h.svc.HealthCheck(c.Request.Context()))
}

// RegisterRoutes attaches the map endpoints to the gin router.
func (h *MapHandler) RegisterRoutes(router *gin.Engine) {
	mapGroup := router.Group("/api/v1/map")
	{
		mapGroup.GET("/health", h.Health)
		mapGroup.GET("/style.json", h.StyleJSON)
		mapGroup.GET("/tiles/:source/:z/:x/:y", h.ProxyTile)
		mapGroup.GET("/glyphs/:fontstack/:start-:end.pbf", h.ProxyGlyphs)
		mapGroup.GET("/sprites/:id@2x.:ext", h.ProxySprite)
		mapGroup.GET("/sprites/:file", h.ProxySprite)
		mapGroup.GET("/route", h.Route)
		mapGroup.GET("/search", h.Search)
		mapGroup.GET("/reverse", h.Reverse)
	}
}

// deriveBaseURL builds the public base URL for this map-service so the
// style.json can rewrite all resources to come back through the gateway.
func deriveBaseURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := c.Request.Host
	if fwdHost := c.GetHeader("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}
	return fmt.Sprintf("%s://%s/api/v1/map", scheme, host)
}

func parseExtension(path string) string {
	parts := strings.Split(path, ".")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return ""
}
