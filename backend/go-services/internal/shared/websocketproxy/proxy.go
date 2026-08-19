package websocketproxy

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ── Configurable origin allowlist ───────────────────────────────────────────
//
// Production deployments should set WS_ALLOWED_ORIGINS to a comma-separated
// list of origins, e.g. "https://omnigo-app-production.up.railway.app,
// https://omnigo.app". The previous hardcoded list made staging / custom
// domains silently fail. Empty env → "reflect Origin" (dev only), so
// local Flutter or curl still works.

var (
	allowedOriginsOnce sync.Once
	allowedOriginsList []string
	allowedOriginsMode atomic.Int32 // 0=reflect, 1=deny, 2=allowlist
	allowedOriginsRaw  atomic.Value // string
)

const (
	modeReflect = 0
	modeDeny    = 1
	modeAllow   = 2
)

func loadAllowedOrigins() {
	raw := os.Getenv("WS_ALLOWED_ORIGINS")
	if raw == "" {
		// Backwards-compat: also accept the older variable name.
		raw = os.Getenv("CORS_ALLOWED_ORIGINS")
	}
	allowedOriginsRaw.Store(raw)
	switch {
	case raw == "":
		allowedOriginsMode.Store(modeReflect)
	case raw == "*":
		allowedOriginsMode.Store(modeReflect)
		allowedOriginsRaw.Store("*")
	default:
		allowedOriginsMode.Store(modeAllow)
		parts := strings.Split(raw, ",")
		allowedOriginsList = make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				allowedOriginsList = append(allowedOriginsList, s)
			}
		}
	}
	log.Printf("websocketproxy: origin policy: env=%q mode=%d count=%d", raw, allowedOriginsMode.Load(), len(allowedOriginsList))
}

func originAllowed(origin string) bool {
	allowedOriginsOnce.Do(loadAllowedOrigins)
	if origin == "" {
		// Native mobile clients (Flutter Android/iOS) typically send no
		// Origin header. Permit them — they're already authenticated by
		// the bearer token in the URL query.
		return true
	}
	switch allowedOriginsMode.Load() {
	case modeReflect:
		return true
	case modeDeny:
		return false
	case modeAllow:
		for _, o := range allowedOriginsList {
			if strings.EqualFold(o, origin) {
				return true
			}
		}
		return false
	}
	return false
}

// upgrader is intentionally permissive about WebSocket headers but strict
// about Origin (handled in originAllowed above).
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// We do our own Origin check in Proxy before calling Upgrade. The
	// default CheckOrigin is the gorilla "permit all" — DO NOT trust it
	// alone.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// IsWebSocketRequest reports whether r is a WebSocket upgrade request.
func IsWebSocketRequest(r *http.Request) bool {
	return websocket.IsWebSocketUpgrade(r)
}

// Proxy upgrades the incoming HTTP connection to a WebSocket and bridges it
// to a backend WebSocket server at targetBaseURL. The original path and
// query are preserved. On Origin rejection, returns 403 with a JSON body.
func Proxy(w http.ResponseWriter, r *http.Request, targetBaseURL string) {
	origin := r.Header.Get("Origin")
	if !originAllowed(origin) {
		log.Printf("websocketproxy: rejected Origin=%q for %s", origin, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":  "origin_not_allowed",
			"origin": origin,
			"hint":   "set WS_ALLOWED_ORIGINS env to a comma-separated list including this origin",
		})
		return
	}

	backendURL, err := url.Parse(targetBaseURL)
	if err != nil {
		http.Error(w, "invalid websocket upstream", http.StatusInternalServerError)
		return
	}
	backendURL.Path = r.URL.Path
	backendURL.RawQuery = r.URL.RawQuery

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	// Copy headers, but skip hop-by-hop + connection-upgrade ones.
	backendHeader := http.Header{}
	for k, vv := range r.Header {
		switch strings.ToLower(k) {
		case "upgrade", "connection", "origin":
			// We strip Origin on the way out so the upstream sees a clean
			// internal hop.
			continue
		}
		for _, v := range vv {
			backendHeader.Add(k, v)
		}
	}

	backendConn, resp, err := dialer.Dial(backendURL.String(), backendHeader)
	if err != nil {
		log.Printf("websocketproxy: dial %s failed: %v", backendURL.String(), err)
		status := http.StatusBadGateway
		if resp != nil && resp.StatusCode != 0 {
			status = resp.StatusCode
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":    "upstream_dial_failed",
			"upstream": backendURL.String(),
		})
		return
	}
	defer backendConn.Close()

	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocketproxy: upgrade failed: %v", err)
		return
	}
	defer clientConn.Close()

	// Send a hello so the client sees a usable connection even before its
	// first message. Avoids a "stuck connecting" perception on slow links.
	_ = clientConn.WriteJSON(map[string]any{
		"type":    "ws.hello",
		"server":  "omnigo-monolith",
		"version": "2.0",
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
	})

	errChan := make(chan error, 2)

	// upstream → client
	go func() {
		for {
			mt, msg, err := backendConn.ReadMessage()
			if err != nil {
				errChan <- err
				return
			}
			if err := clientConn.WriteMessage(mt, msg); err != nil {
				errChan <- err
				return
			}
		}
	}()

	// client → upstream
	go func() {
		for {
			mt, msg, err := clientConn.ReadMessage()
			if err != nil {
				errChan <- err
				return
			}
			if err := backendConn.WriteMessage(mt, msg); err != nil {
				errChan <- err
				return
			}
		}
	}()

	// Block until one direction closes, then close both sides.
	<-errChan
	go func() { <-errChan }()
}
