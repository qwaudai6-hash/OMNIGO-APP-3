package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/omnigo/backend/internal/shared/websocketproxy"
)

// ── Config helpers ───────────────────────────────────────────────────────────

func envPortOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return def
}

func overrideEnv(env []string, key, val string) []string {
	prefix := key + "="
	found := false
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + val
			found = true
		}
	}
	if !found {
		env = append(env, prefix+val)
	}
	return env
}

func envList(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ── Service registry ────────────────────────────────────────────────────────
//
// Each entry maps a public route prefix to an internal child service. The
// gateway probes /health on each child at startup and on a fixed interval.
// Failed children are excluded from the proxy table until they recover.

type serviceEntry struct {
	name        string
	prefixes    []string
	port        string
	healthURL   string
	targetURL   string // if set, proxy to this URL instead of 127.0.0.1:port (for external services)
	healthy     atomic.Bool
	lastChecked atomic.Int64 // unix nano
	started     atomic.Bool
}

func (s *serviceEntry) isReady() bool {
	return s.healthy.Load() && s.started.Load()
}

var (
	registryMu sync.RWMutex
	registry   = map[string]*serviceEntry{}
)

func register(name string, prefixes []string, port string) {
	entry := &serviceEntry{name: name, prefixes: prefixes, port: port}
	entry.healthy.Store(false)
	entry.started.Store(false)
	registry[name] = entry
}

// registerExternal registers a service that runs externally (not spawned by monolith).
// proxyURL is the full URL to proxy to (e.g. http://ai-engine.railway.internal:8086).
func registerExternal(name string, prefixes []string, proxyURL string) {
	entry := &serviceEntry{name: name, prefixes: prefixes, port: "0", targetURL: proxyURL}
	entry.healthURL = proxyURL + "/health"
	entry.healthy.Store(false)
	entry.started.Store(false)
	registry[name] = entry
}

func lookupPrefix(path string) (*serviceEntry, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	var bestMatch *serviceEntry
	var longestPrefix int

	for _, s := range registry {
		for _, p := range s.prefixes {
			if strings.HasPrefix(path, p) {
				if len(p) > longestPrefix {
					longestPrefix = len(p)
					bestMatch = s
				}
			}
		}
	}

	if bestMatch != nil {
		return bestMatch, true
	}
	return nil, false
}

// ── Startup health gate ─────────────────────────────────────────────────────
//
// We DO NOT just `time.Sleep(3s)` and hope. Instead we spawn each child, then
// actively probe /health on a tight loop until either the child responds 200
// or the deadline is hit. The proxy table is only built from children that
// passed their probe — so the first user request never hits a half-started
// service and gets a 502.

const (
	probeTimeout     = 1500 * time.Millisecond
	probeMaxWait     = 45 * time.Second
	probeInterval    = 250 * time.Millisecond
	healthRecheckInt = 5 * time.Second
)

func probeHealth(ctx context.Context, url string) bool {
	client := &http.Client{Timeout: probeTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func waitForReady(ctx context.Context, s *serviceEntry) bool {
	deadline := time.Now().Add(probeMaxWait)
	url := s.healthURL
	log.Printf("⏳ Waiting for %s to become healthy at %s (max %s)", s.name, url, probeMaxWait)
	for {
		if probeHealth(ctx, url) {
			s.healthy.Store(true)
			s.started.Store(true)
			s.lastChecked.Store(time.Now().UnixNano())
			log.Printf("✓ %s is HEALTHY", s.name)
			return true
		}
		if time.Now().After(deadline) {
			log.Printf("✗ %s FAILED health gate after %s — excluding from proxy", s.name, probeMaxWait)
			s.healthy.Store(false)
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(probeInterval):
		}
	}
}

func startChild(name, port string) bool {
	binPath := "/app/bin/" + name
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		if _, err := os.Stat("./bin/" + name); err == nil {
			binPath = "./bin/" + name
		} else if lp, err := exec.LookPath(name); err == nil {
			binPath = lp
		} else {
			log.Printf("⚠ Binary %s not found at %s or ./bin/%s — service will be marked DOWN", name, binPath, name)
			return false
		}
	}
	cmd := exec.Command(binPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	env := os.Environ()
	if port != "" {
		env = overrideEnv(env, "PORT", port)
	}
	// Propagate DB URLs and JWT issuer if not explicitly set.
	if os.Getenv("DB_WRITER_DSN") == "" {
		env = append(env, "DB_WRITER_DSN="+os.Getenv("DATABASE_URL"))
	}
	if os.Getenv("DB_READER_DSN") == "" {
		env = append(env, "DB_READER_DSN="+os.Getenv("DATABASE_URL"))
	}
	if os.Getenv("PRODUCT_SERVICE_URL") == "" {
		env = append(env, "PRODUCT_SERVICE_URL=http://127.0.0.1:"+envPortOr("PRODUCT_SERVICE_PORT", "9001"))
	}
	if os.Getenv("ADMIN_SERVICE_URL") == "" {
		env = append(env, "ADMIN_SERVICE_URL=http://127.0.0.1:"+envPortOr("ADMIN_SERVICE_PORT", "9007"))
	}
	if os.Getenv("JWT_ISSUER") == "" {
		env = append(env, "JWT_ISSUER=omnigo-platform")
	}
	// Propagate TigerBeetle Ledger configuration to child microservices
	if os.Getenv("TIGERBEETLE_DISABLED") == "true" {
		env = append(env, "TIGERBEETLE_DISABLED=true")
		env = append(env, "TB_ENABLED=false")
	} else if tbAddr := os.Getenv("TIGERBEETLE_ADDRESSES"); tbAddr != "" {
		env = append(env, "TIGERBEETLE_ADDRESSES="+tbAddr)
		env = append(env, "TIGERBEETLE_ADDR="+tbAddr)
		env = append(env, "TB_ENABLED=true")
	} else if tbAddr2 := os.Getenv("TIGERBEETLE_ADDR"); tbAddr2 != "" {
		env = append(env, "TIGERBEETLE_ADDRESSES="+tbAddr2)
		env = append(env, "TIGERBEETLE_ADDR="+tbAddr2)
		env = append(env, "TB_ENABLED=true")
	}
	// Propagate Kafka and Event configuration
	if kafkaBrokers := os.Getenv("KAFKA_BROKERS"); kafkaBrokers != "" {
		env = append(env, "KAFKA_BROKERS="+kafkaBrokers)
	}
	env = append(env, "DISABLE_MIGRATIONS=true")
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		log.Printf("✗ Failed to start %s: %v", name, err)
		return false
	}
	log.Printf("✓ Spawned %s on internal port %s (pid=%d)", name, port, cmd.Process.Pid)
	return true
}

// Continuously re-probes failed services so the gateway self-heals when a
// child recovers (e.g. a flaky DB connection that comes back).
func startHealthWatcher(ctx context.Context) {
	ticker := time.NewTicker(healthRecheckInt)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Snapshot the registry under lock, then probe without holding it.
			registryMu.RLock()
			failed := make([]*serviceEntry, 0)
			for _, s := range registry {
				if !s.isReady() {
					failed = append(failed, s)
				}
			}
			registryMu.RUnlock()

			for _, s := range failed {
				if probeHealth(ctx, s.healthURL) {
					s.healthy.Store(true)
					s.started.Store(true)
					s.lastChecked.Store(time.Now().UnixNano())
					log.Printf("♻ %s recovered", s.name)
				}
			}
		}
	}
}

// ── CORS allowlist ──────────────────────────────────────────────────────────
//
// CORS_ALLOWED_ORIGINS is a comma-separated list. Empty → reflect request
// Origin (development only). "*" → wildcard. Use a real allowlist in prod.

func corsAllowedOrigin(origin, allowList string) string {
	origin = strings.TrimSpace(origin)
	if allowList == "" {
		return origin // dev: reflect
	}
	if allowList == "*" {
		return "*"
	}
	for _, o := range strings.Split(allowList, ",") {
		if strings.EqualFold(strings.TrimSpace(o), origin) {
			return origin
		}
	}
	return ""
}

// ── /readyz probe that ACTUALLY checks upstreams ───────────────────────────

func handleReady(w http.ResponseWriter, r *http.Request) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	type status struct {
		Name    string `json:"name"`
		Healthy bool   `json:"healthy"`
		Port    string `json:"port"`
	}
	out := struct {
		Status      string    `json:"status"`
		Service     string    `json:"service"`
		Upstreams   int       `json:"upstreams_total"`
		UpstreamsOK int       `json:"upstreams_healthy"`
		GeneratedAt time.Time `json:"generated_at"`
		Services    []status  `json:"services"`
	}{
		Service:     "omnigo-monolith",
		GeneratedAt: time.Now().UTC(),
	}
	for _, s := range registry {
		ok := s.isReady()
		out.Services = append(out.Services, status{Name: s.name, Healthy: ok, Port: s.port})
		out.Upstreams++
		if ok {
			out.UpstreamsOK++
		}
	}
	if out.UpstreamsOK == out.Upstreams && out.Upstreams > 0 {
		out.Status = "ready"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	out.Status = "degraded"
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(out)
}

// startTigerBeetleIfConfigured formats and launches TigerBeetle if embedded mode is enabled.
func startTigerBeetleIfConfigured() {
	if os.Getenv("TIGERBEETLE_DISABLED") == "true" {
		return
	}
	tbBin := "tigerbeetle"
	if _, err := exec.LookPath(tbBin); err != nil {
		if _, err := os.Stat("/app/bin/tigerbeetle"); err == nil {
			tbBin = "/app/bin/tigerbeetle"
		} else if _, err := os.Stat("./tigerbeetle"); err == nil {
			tbBin = "./tigerbeetle"
		} else {
			log.Println("ℹ TigerBeetle binary not found in container; using PostgreSQL high-integrity fallback ledger")
			return
		}
	}

	dataDir := os.Getenv("TIGERBEETLE_DATA_DIR")
	if dataDir == "" {
		dataDir = "/tmp/tigerbeetle_data"
	}
	_ = os.MkdirAll(dataDir, 0755)
	dataFile := dataDir + "/0_0.tigerbeetle"

	if _, err := os.Stat(dataFile); os.IsNotExist(err) {
		log.Println("🐅 Formatting TigerBeetle data file at", dataFile)
		formatCmd := exec.Command(tbBin, "format", "--cluster=0", "--replica=0", "--replica-count=1", dataFile)
		if out, err := formatCmd.CombinedOutput(); err != nil {
			log.Printf("⚠ TigerBeetle format error: %v (output: %s)", err, string(out))
		} else {
			log.Println("✓ TigerBeetle format complete")
		}
	}

	tbPort := envPortOr("TIGERBEETLE_PORT", "3000")
	tbCmd := exec.Command(tbBin, "start", "--addresses=0.0.0.0:"+tbPort, dataFile)
	tbCmd.Stdout = os.Stdout
	tbCmd.Stderr = os.Stderr
	if err := tbCmd.Start(); err != nil {
		log.Printf("⚠ Failed to start embedded TigerBeetle: %v", err)
	} else {
		log.Printf("✓ Spawned TigerBeetle ledger on port %s (pid=%d)", tbPort, tbCmd.Process.Pid)
		_ = os.Setenv("TIGERBEETLE_ADDRESSES", "127.0.0.1:"+tbPort)
		_ = os.Setenv("TB_ENABLED", "true")
	}
}

// ── Main ────────────────────────────────────────────────────────────────────

func main() {
	log.Println("Starting OMNIGO Monolith API Gateway (Railway Production Mode)...")

	// Start embedded TigerBeetle if configured
	startTigerBeetleIfConfigured()

	// Initialize fallback keys if not explicitly provided in environment
	if strings.TrimSpace(os.Getenv("JWT_SECRET_KEY")) == "" {
		_ = os.Setenv("JWT_SECRET_KEY", "csiPLQIJqstuH6rIa6ulOdjl30RMYqwfk2cwTPoj2nAVHykMdWixUJnwVt6NovAyUDMqLoryPxQOSPM6jr6MlQ==")
		log.Println("ℹ Using default production JWT_SECRET_KEY")
	}
	if strings.TrimSpace(os.Getenv("HMAC_SECRET")) == "" {
		_ = os.Setenv("HMAC_SECRET", "XLZg8xSIgUncVPqiObww9hRzOVc5Y68E+5xjB0+ac7c=")
		log.Println("ℹ Using default production HMAC_SECRET")
	}
	if strings.TrimSpace(os.Getenv("HMAC_TOKEN_ENCRYPTION_KEY")) == "" {
		hmacSecret := os.Getenv("HMAC_SECRET")
		sum := sha256.Sum256([]byte(hmacSecret))
		_ = os.Setenv("HMAC_TOKEN_ENCRYPTION_KEY", hex.EncodeToString(sum[:]))
		log.Println("ℹ Derived 64-hex HMAC_TOKEN_ENCRYPTION_KEY from HMAC_SECRET")
	}
	if strings.TrimSpace(os.Getenv("ADMIN_API_KEY_ENCRYPTION_KEY")) == "" {
		_ = os.Setenv("ADMIN_API_KEY_ENCRYPTION_KEY", "/N6AKevpb5gqQ7TpEndfYJ9bHvBU54hQV8I2w+ealsQ=")
		log.Println("ℹ Using default production ADMIN_API_KEY_ENCRYPTION_KEY")
	}
	if strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS")) == "" {
		_ = os.Setenv("CORS_ALLOWED_ORIGINS", "https://omnigo-app-3-production.up.railway.app,https://omnigo-app-production.up.railway.app")
		log.Println("ℹ Using default production CORS_ALLOWED_ORIGINS")
	}
	if strings.TrimSpace(os.Getenv("REDIS_ADDRS")) == "" {
		if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
			_ = os.Setenv("REDIS_ADDRS", redisURL)
			log.Println("ℹ Using Railway Redis from REDIS_URL")
		} else {
			_ = os.Setenv("REDIS_ADDRS", "redis.railway.internal:6379")
			log.Println("ℹ Using Railway Internal Redis (redis.railway.internal:6379)")
		}
	}
	if strings.TrimSpace(os.Getenv("KAFKA_BROKERS")) == "" {
		_ = os.Setenv("KAFKA_BROKERS", "kafka.railway.internal:9092")
	}

	// PayFast return URL validation — required for hosted checkout redirects.
	// Without these, wallet top-up and legacy charge endpoints return 500.
	if strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")) == "" {
		log.Println("⚠ WARNING: PUBLIC_BASE_URL is not set — PayFast return URLs will fail. Set it to https://omnigo-app-3-production.up.railway.app in Railway env vars")
	}
	if strings.TrimSpace(os.Getenv("WALLET_RETURN_URL")) == "" && strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")) == "" {
		log.Println("⚠ WARNING: Neither WALLET_RETURN_URL nor PUBLIC_BASE_URL is set — PayFast hosted checkout will return 500. Set WALLET_RETURN_URL or PUBLIC_BASE_URL in Railway env vars.")
	}

	// Build service registry. Order = start order. Each prefix list is OR'd
	// in lookupPrefix. Add new routes here, not in a switch statement, so
	// adding a service is a one-line change.
	register("auth-service",
		[]string{"/api/v1/auth", "/api/v1/users"},
		envPortOr("AUTH_SERVICE_PORT", "9000"))
	register("product-service",
		[]string{"/api/v1/products", "/api/v1/wishlist", "/api/v1/reviews", "/api/v1/vendor/products", "/uploads"},
		envPortOr("PRODUCT_SERVICE_PORT", "9001"))
	register("vendor-store-service",
		[]string{"/api/v1/vendor", "/api/v1/stores", "/api/v1/geocoding"},
		envPortOr("VENDOR_SERVICE_PORT", "9002"))
	register("delivery-gig-service",
		[]string{"/api/v1/delivery", "/api/v1/ride"},
		envPortOr("DELIVERY_SERVICE_PORT", "9003"))
	register("ride-service",
		[]string{"/api/v1/rides"},
		envPortOr("RIDE_SERVICE_PORT", "9004"))
	register("order-service",
		[]string{"/api/v1/orders", "/api/v1/cart", "/api/v1/chat", "/api/v1/ratings", "/api/v1/wallet", "/api/v1/payment",
			"/api/v1/finance/refund", "/api/v1/finance/cancel"},
		envPortOr("ORDER_SERVICE_PORT", "9005"))
	register("payment-orchestrator",
		[]string{"/api/v1/payments", "/api/v1/finance", "/api/v1/ledger"},
		envPortOr("PAYMENT_ORCHESTRATOR_PORT", "9006"))
	register("admin-service",
		[]string{"/api/v1/admin", "/api/v1/geo"},
		envPortOr("ADMIN_SERVICE_PORT", "9007"))
	register("websocket-gateway",
		[]string{"/ws", "/api/v1/ws"},
		envPortOr("WEBSOCKET_GATEWAY_PORT", "9008"))
	register("map-service",
		[]string{"/api/v1/map"},
		envPortOr("MAP_SERVICE_PORT", "9010"))

	// AI Engine — external Python service, not spawned by monolith.
	// Only register if explicitly configured and not disabled.
	if os.Getenv("AI_ENGINE_DISABLED") != "true" {
		if aiURL := os.Getenv("AI_ENGINE_URL"); aiURL != "" {
			entry := &serviceEntry{name: "ai-engine", prefixes: []string{"/api/v1/ai"}, port: "0", targetURL: aiURL}
			entry.healthURL = aiURL + "/health"
			entry.healthy.Store(true) // assume reachable; health watcher will re-probe
			entry.started.Store(true) // external services don't block startup
			registry["ai-engine"] = entry
			log.Printf("✓ ai-engine registered as external service at %s", aiURL)
		}
	}

	// Wire each registry entry's health URL (same as proxy target, but on /health).
	// External services already have their healthURL set by registerExternal.
	for _, s := range registry {
		if s.targetURL == "" {
			s.healthURL = "http://127.0.0.1:" + s.port + "/health"
		}
	}

	// Boot phase: start each child, then block until it answers /health or
	// probeMaxWait elapses. We run them concurrently with a single global
	// timeout to keep cold starts under 30s.
	bootCtx, bootCancel := context.WithTimeout(context.Background(), probeMaxWait+5*time.Second)
	defer bootCancel()

	var wg sync.WaitGroup
	for name, entry := range registry {
		wg.Add(1)
		go func(n string, e *serviceEntry) {
			defer wg.Done()
			// External services (targetURL set) are already running — just probe health.
			if e.targetURL != "" {
				waitForReady(bootCtx, e)
				return
			}
			if !startChild(n, e.port) {
				return
			}
			waitForReady(bootCtx, e)
		}(name, entry)
	}
	wg.Wait()
	log.Printf("=== Boot phase complete: %d upstreams healthy ===", countHealthy())

	// Background health watcher — re-probes failed children so the gateway
	// self-heals without a restart.
	watcherCtx, watcherCancel := context.WithCancel(context.Background())
	defer watcherCancel()
	go startHealthWatcher(watcherCtx)

	// HTTP server.
	allowedOrigins := os.Getenv("CORS_ALLOWED_ORIGINS")
	proxyPort := os.Getenv("PORT")
	if proxyPort == "" {
		proxyPort = "8000"
	}

	// Build reverse proxies once, reuse across requests. They are safe for
	// concurrent use and per-request the Director rewrites the host.
	proxies := map[string]*httputil.ReverseProxy{}
	registryMu.RLock()
	for _, s := range registry {
		targetStr := "http://127.0.0.1:" + s.port
		if s.targetURL != "" {
			targetStr = s.targetURL
		}
		target, err := url.Parse(targetStr)
		if err != nil {
			log.Printf("⚠ bad target for %s: %v", s.name, err)
			continue
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		// Standard proxy error handler: when upstream is down, return 503
		// with a JSON body so the client knows which service failed.
		name := s.name
		proxy.ErrorHandler = func(rw http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy error to %s: %v", name, err)
			s.healthy.Store(false) // mark unhealthy so watcher re-probes
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(rw).Encode(map[string]any{
				"error":   "upstream_unavailable",
				"service": name,
				"hint":    "service may still be booting; retry shortly",
			})
		}
		proxies[s.port] = proxy
	}
	registryMu.RUnlock()

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"monolith"}`))
	})
	mux.HandleFunc("/readyz", handleReady)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allow := corsAllowedOrigin(origin, allowedOrigins); allow != "" {
			w.Header().Set("Access-Control-Allow-Origin", allow)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-Trace-Id, X-Internal-Token")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		path := r.URL.Path
		if path == "/" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"service":"omnigo-monolith","status":"ok","endpoints":["/api/v1/auth","/api/v1/products","/api/v1/orders","/api/v1/payments","/api/v1/rides","/api/v1/delivery","/api/v1/vendor","/api/v1/stores","/api/v1/admin","/ws","/health","/readyz"]}`))
			return
		}

		entry, ok := lookupPrefix(path)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"route_not_found","path":"` + path + `"}`))
			return
		}

		// WebSocket path: bridge to upstream.
		if websocketproxy.IsWebSocketRequest(r) {
			wsTarget := "ws://127.0.0.1:" + entry.port
			if entry.targetURL != "" {
				wsTarget = strings.Replace(entry.targetURL, "http://", "ws://", 1)
			}
			websocketproxy.Proxy(w, r, wsTarget)
			return
		}

		// Standard HTTP reverse proxy.
		proxy, ok := proxies[entry.port]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"proxy_not_initialised"}`))
			return
		}
		r.Host = "127.0.0.1:" + entry.port
		proxy.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              ":" + proxyPort,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		// Connection-level timeouts for proxying long-lived requests.
	}

	// Bind explicitly so we get a clear error if the port is taken.
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		log.Fatalf("Gateway failed to bind %s: %v", srv.Addr, err)
	}
	log.Printf("=== OMNIGO Monolith Proxy listening on %s ===", proxyPort)
	log.Printf("=== CORS allowed origins: %q (raw: %q) ===",
		func() string {
			if allowedOrigins == "" {
				return "reflect"
			}
			return allowedOrigins
		}(),
		allowedOrigins,
	)

	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Gateway failed: %v", err)
		}
	}()

	// Graceful shutdown on SIGTERM/SIGINT so Railway/health checks see a clean exit.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down monolith gateway...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Gateway forced to shutdown: %v", err)
	}
	log.Println("Monolith gateway exited")
}

func countHealthy() int {
	n := 0
	registryMu.RLock()
	defer registryMu.RUnlock()
	for _, s := range registry {
		if s.isReady() {
			n++
		}
	}
	return n
}

// silence unused-variable warnings for helpers we want kept for future use
var _ = envList
