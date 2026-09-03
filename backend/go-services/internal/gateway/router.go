// Package gateway implements the single public entry point for all OMNIGO
// clients (Flutter, web storefront). It is a stateless Layer-7 reverse
// proxy + router that fans requests out to internal microservices over
// Railway private networking (or localhost in dev).
//
// Design goals:
//   - Stateless: safe to run N replicas behind Railway/K8s load balancer.
//   - Single public URL: Flutter only knows https://omnigo-app-production.up.railway.app
//   - Internal services stay private; gateway is the only exposed port.
//   - Health, readiness and liveness endpoints for auto-scaling.
//   - Circuit breaker + retry per upstream to tolerate partial failures.
//   - Shared middleware: CORS, rate limit, request ID, panic recovery, slog access log.
package gateway

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/shared/logging"
	"github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/shared/websocketproxy"
	"github.com/redis/go-redis/v9"
)

// Upstream describes one internal service the gateway can route to.
type Upstream struct {
	Name    string // human label for logs/metrics
	BaseURL string // e.g. http://auth-service.railway.internal:9000
}

// circuitState is a tiny per-upstream circuit breaker.
// ponytail: in-memory breaker per gateway instance. Good enough for
// stateless horizontally-scaled gateways; if you want a cluster-wide
// breaker, back it with Redis. Upgrade path: redisBreaker struct.
type circuitState struct {
	mu               sync.Mutex
	failures         int
	openedAt         time.Time
	halfOpenProbe    bool
	failureThreshold int
	openTimeout      time.Duration
}

func newCircuit(threshold int, openTimeout time.Duration) *circuitState {
	return &circuitState{failureThreshold: threshold, openTimeout: openTimeout}
}

func (c *circuitState) allow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.openedAt.IsZero() {
		return true
	}
	if time.Since(c.openedAt) > c.openTimeout {
		if c.halfOpenProbe {
			return false // only one probe at a time
		}
		c.halfOpenProbe = true
		return true
	}
	return false
}

func (c *circuitState) recordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures = 0
	c.openedAt = time.Time{}
	c.halfOpenProbe = false
}

func (c *circuitState) recordFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures++
	if c.failures >= c.failureThreshold {
		c.openedAt = time.Now()
	}
}

// route binds a URL prefix to an upstream.
type route struct {
	prefix   string
	upstream *Upstream
	breaker  *circuitState
}

// Gateway is the public router. Construct once with New, then call Handler().
type Gateway struct {
	routes      []route
	websocketUp *Upstream
	aiUpstream  *Upstream
	startedAt   time.Time
	redisClient redis.UniversalClient
}

// Options configures the gateway from env vars.
type Options struct {
	// Env is used to decide CORS strictness ("development" allows *).
	Env string
	// Redis is used for distributed rate limiting. May be nil (in-memory fallback).
	Redis *redis.Client
}

// resolveUpstream reads a service URL from env, falling back to localhost dev port.
func resolveUpstream(envKey, devFallback string) *Upstream {
	u := os.Getenv(envKey)
	if u == "" {
		u = devFallback
	}
	return &Upstream{Name: envKey, BaseURL: u}
}

// New builds the gateway from environment variables.
func New(opts Options) *Gateway {
	g := &Gateway{
		startedAt:   time.Now(),
		redisClient: opts.Redis,
	}

	breaker := func() *circuitState { return newCircuit(5, 15*time.Second) }

	add := func(prefix, envKey, dev string) {
		g.routes = append(g.routes, route{
			prefix:   prefix,
			upstream: resolveUpstream(envKey, dev),
			breaker:  breaker(),
		})
	}

	// ── Public API routes (match Flutter ApiEndpoints) ──
	add("/api/v1/auth", "AUTH_SERVICE_URL", "http://127.0.0.1:9000")
	add("/api/v1/products", "PRODUCT_SERVICE_URL", "http://127.0.0.1:9001")
	add("/api/v1/wishlist", "PRODUCT_SERVICE_URL", "http://127.0.0.1:9001")
	add("/api/v1/reviews", "PRODUCT_SERVICE_URL", "http://127.0.0.1:9001")
	add("/api/v1/vendor/products", "PRODUCT_SERVICE_URL", "http://127.0.0.1:9001")
	add("/uploads", "PRODUCT_SERVICE_URL", "http://127.0.0.1:9001")
	// BUG-IMAGE-1 FIX: Delivery proof photos and KYC docs are stored in
	// separate services but were unreachable through the gateway because
	// /uploads only proxied to product-service. Now each sub-path routes
	// to the correct upstream.
	add("/uploads/proofs", "DELIVERY_SERVICE_URL", "http://127.0.0.1:9003")
	add("/uploads/kyc", "AUTH_SERVICE_URL", "http://127.0.0.1:9000")
	add("/api/v1/stores", "VENDOR_STORE_SERVICE_URL", "http://127.0.0.1:9002")
	add("/api/v1/vendor", "VENDOR_STORE_SERVICE_URL", "http://127.0.0.1:9002")
	add("/api/v1/geocoding", "VENDOR_STORE_SERVICE_URL", "http://127.0.0.1:9002")
	add("/api/v1/geo", "ADMIN_SERVICE_URL", "http://127.0.0.1:9007")
	add("/api/v1/admin", "ADMIN_SERVICE_URL", "http://127.0.0.1:9007")
	add("/api/v1/delivery", "DELIVERY_SERVICE_URL", "http://127.0.0.1:9003")
	add("/api/v1/ride", "RIDE_SERVICE_URL", "http://127.0.0.1:9004")
	add("/api/v1/rides", "RIDE_SERVICE_URL", "http://127.0.0.1:9004")
	add("/api/v1/orders", "ORDER_SERVICE_URL", "http://127.0.0.1:9005")
	add("/api/v1/cart", "ORDER_SERVICE_URL", "http://127.0.0.1:9005")
	add("/api/v1/chat", "ORDER_SERVICE_URL", "http://127.0.0.1:9005")
	add("/api/v1/ratings", "ORDER_SERVICE_URL", "http://127.0.0.1:9005")
	add("/api/v1/wallet", "ORDER_SERVICE_URL", "http://127.0.0.1:9005")
	// Singular `/api/v1/payment` is what Flutter's `stripeCheckout()` calls.
	// Without this route, Stripe PaymentSheet calls hit the gateway and
	// 404 because the longest-prefix matcher only finds `/api/v1/payments`
	// (plural) which routes to the payment-orchestrator (different service).
	add("/api/v1/payment", "ORDER_SERVICE_URL", "http://127.0.0.1:9005")
	add("/api/v1/payments", "PAYMENT_SERVICE_URL", "http://127.0.0.1:9006")
	// Finance split: refund/cancel live on order-service, while
	// reconcile / payouts-export live on payment-orchestrator. Longest-prefix
	// matching makes the two specific prefixes win over the generic one.
	add("/api/v1/finance/refund", "ORDER_SERVICE_URL", "http://127.0.0.1:9005")
	add("/api/v1/finance/cancel", "ORDER_SERVICE_URL", "http://127.0.0.1:9005")
	add("/api/v1/finance", "PAYMENT_SERVICE_URL", "http://127.0.0.1:9006")
	add("/api/v1/ai", "AI_ENGINE_URL", "http://127.0.0.1:8086")
	// Map stack proxy: forwards style.json, tiles, glyphs, sprites,
	// reverse geocode, and OSRM route calls to the in-house map-service.
	// Without this route, every Flutter `MapLibreMapWidget` would 404
	// on its first style.json load.
	add("/api/v1/map", "MAP_SERVICE_URL", "http://127.0.0.1:9010")

	// Websocket is handled separately (needs connection upgrade).
	g.websocketUp = resolveUpstream("WEBSOCKET_GATEWAY_URL", "http://127.0.0.1:9008")

	return g
}

// longestPrefix matches the route with the longest prefix (so /api/v1/vendor/products
// wins over /api/v1/vendor if both were registered — though here we route both to
// the same upstream anyway; the logic keeps the table order-safe).
func (g *Gateway) match(path string) *route {
	var best *route
	for i := range g.routes {
		r := &g.routes[i]
		if strings.HasPrefix(path, r.prefix) {
			if best == nil || len(r.prefix) > len(best.prefix) {
				best = r
			}
		}
	}
	return best
}

// Handler returns the public http.Handler. Mount on the single exposed port.
func (g *Gateway) Handler() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	// ── Shared middleware (stateless, safe for N replicas) ──
	r.Use(middleware.Recovery())
	r.Use(middleware.RequestID())
	r.Use(middleware.CORS())
	r.Use(middleware.BodyLimit()) // MEDIUM-26: 2 MiB cap at the edge
	if g.redisClient != nil {
		r.Use(middleware.RateLimit(g.redisClient, 300, time.Minute))
	} else if rdbAddr := os.Getenv("REDIS_ADDRS"); rdbAddr != "" {
		rdb := redis.NewClient(&redis.Options{Addr: rdbAddr})
		r.Use(middleware.RateLimit(rdb, 300, time.Minute))
	}
	r.Use(g.accessLog())

	// ── Health / readiness / liveness (for Railway + K8s autoscaling) ──
	r.GET("/health", g.health)
	r.GET("/healthz", g.health)   // K8s liveness
	r.GET("/readyz", g.readiness) // K8s readiness
	r.GET("/livez", g.health)     // alias
	r.GET("/", g.root)

	// ── Websocket upgrade proxy ──
	r.GET("/ws", g.handleWebSocket)

	// ── Everything under /api/* is reverse-proxied ──
	r.NoRoute(g.proxy)

	return r
}

// root is a friendly landing for browser hits to the bare domain.
func (g *Gateway) root(c *gin.Context) {
	// GW-06: do not advertise the internal API surface to anonymous callers.
	c.JSON(http.StatusOK, gin.H{
		"service": "omnigo-gateway",
		"status":  "ok",
	})
}

// health is the liveness probe — gateway process is alive.
func (g *Gateway) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"service":   "omnigo-gateway",
		"uptime_s":  int(time.Since(g.startedAt).Seconds()),
		"replica":   os.Getenv("RAILWAY_REPLICA_ID"),
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// readiness checks that at least the core upstreams are reachable.
// Railway/K8s only route traffic to this replica when this returns 200.
func (g *Gateway) readiness(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	type probe struct {
		name string
		ok   bool
		err  string
	}
	results := make([]probe, 0, len(g.routes))
	client := &http.Client{Timeout: 1500 * time.Millisecond}

	for i := range g.routes {
		r := &g.routes[i]
		// Hit the upstream's /health. If it doesn't have one, a 404 is still "alive".
		checkURL := strings.TrimRight(r.upstream.BaseURL, "/") + "/health"
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
		resp, err := client.Do(req)
		if err != nil {
			results = append(results, probe{r.upstream.Name, false, err.Error()})
			continue
		}
		resp.Body.Close()
		// 200 or 404 both mean "service is up and answering".
		alive := resp.StatusCode < 500
		results = append(results, probe{r.upstream.Name, alive, ""})
	}

	allOK := true
	summary := make([]gin.H, 0, len(results))
	for _, p := range results {
		if !p.ok {
			allOK = false
		}
		summary = append(summary, gin.H{"service": p.name, "ok": p.ok, "error": p.err})
	}
	status := http.StatusOK
	if !allOK {
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, gin.H{"ready": allOK, "upstreams": summary})
}

// proxy is the core reverse-proxy handler.
func (g *Gateway) proxy(c *gin.Context) {
	r := g.match(c.Request.URL.Path)
	if r == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "route_not_found",
			"path":  c.Request.URL.Path,
			"hint":  "No upstream registered for this prefix. Check gateway route table.",
		})
		return
	}

	if !r.breaker.allow() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "upstream_circuit_open",
			"service": r.upstream.Name,
			"retry":   "after 15s",
		})
		return
	}

	target, err := url.Parse(r.upstream.BaseURL)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "bad_upstream_url", "service": r.upstream.Name})
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// Customize the director: preserve original path, set Host to upstream,
	// forward X-Forwarded-* headers so downstream services see the real client.
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
		// GW-08/GW-22: tracing IDs are generated SERVER-SIDE (requestid
		// middleware sanitizes them). Never forward raw client-supplied trace
		// identifiers upstream — they enable log injection / cache poisoning.
		if rid := c.GetString("request_id"); rid != "" {
			req.Header.Set("X-Request-ID", rid)
		}
		// Preserve the original client IP for downstream rate limiting / audit.
		if prior := req.Header.Get("X-Forwarded-For"); prior != "" {
			req.Header.Set("X-Forwarded-For", prior+", "+c.ClientIP())
		} else {
			req.Header.Set("X-Forwarded-For", c.ClientIP())
		}
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", c.Request.Host)
	}

	// GW-11: a single failed request must count ONCE against the breaker.
	// Previously the ErrorHandler recorded a failure AND the 502 status
	// observer recorded it again → premature circuit opening.
	errorHandled := false

	// Track success/failure for the circuit breaker + retry once on transport error.
	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		r.breaker.recordFailure()
		errorHandled = true
		logging.Error(req.Context(), "gateway proxy error",
			"upstream", r.upstream.Name,
			"path", req.URL.Path,
			"error", err.Error())
		// GW-12: brief jittered backoff before the single retry so N gateway
		// goroutines don't hammer a recovering upstream in lockstep.
		// GW-13: retry only bodyless idempotent requests (GET/HEAD/OPTIONS),
		// so a consumed request body can never be replayed empty.
		time.Sleep(time.Duration(100+rand.Intn(100)) * time.Millisecond)
		if retryOnce(target, w, req) {
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintln(w, `{"error":"bad_gateway","service":"`+r.upstream.Name+`"}`)
	}

	// Wrap the response writer to observe status code for breaker accounting.
	observed := &statusRecorder{ResponseWriter: c.Writer, status: http.StatusOK}
	proxy.ServeHTTP(observed, c.Request)
	if errorHandled {
		return // already accounted by ErrorHandler (incl. its retry outcome)
	}
	if observed.status >= 500 {
		r.breaker.recordFailure()
	} else {
		r.breaker.recordSuccess()
	}
}

// handleWebSocket proxies a websocket upgrade to the websocket-gateway upstream.
func (g *Gateway) handleWebSocket(c *gin.Context) {
	if g.websocketUp == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "websocket_upstream_not_configured"})
		return
	}
	if !websocketproxy.IsWebSocketRequest(c.Request) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not_a_websocket_upgrade"})
		return
	}
	websocketproxy.Proxy(c.Writer, c.Request, g.websocketUp.BaseURL)
}

// statusRecorder wraps gin.ResponseWriter to capture the final status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// retryOnce attempts a single re-send of the original request to the same upstream.
// Returns true if the retry succeeded (2xx/3xx/4xx — anything transport-level OK).
func retryOnce(target *url.URL, w http.ResponseWriter, origReq *http.Request) bool {
	// Only retry idempotent methods to avoid double-charging payments.
	switch origReq.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		return false
	}
	// Preserve the full target URL including query string — dropping RawQuery
	// here made retried filtered GETs return unfiltered results.
	retryURL := target.String() + origReq.URL.Path
	if origReq.URL.RawQuery != "" {
		retryURL += "?" + origReq.URL.RawQuery
	}
	retryReq, err := http.NewRequestWithContext(origReq.Context(), origReq.Method, retryURL, nil)
	if err != nil {
		return false
	}
	retryReq.Header = origReq.Header.Clone()
	resp, err := http.DefaultClient.Do(retryReq)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return true
}

// accessLog is a slog-based access log middleware (replaces gin.Logger output).
func (g *Gateway) accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		latency := time.Since(start)
		logging.Info(c.Request.Context(), "gateway request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", latency.Milliseconds(),
			"client_ip", c.ClientIP(),
			"upstream", upstreamForPath(c.Request.URL.Path, g),
		)
	}
}

// upstreamForPath returns the upstream name for a matched path (for logs).
func upstreamForPath(path string, g *Gateway) string {
	if r := g.match(path); r != nil {
		return r.upstream.Name
	}
	return "-"
}
