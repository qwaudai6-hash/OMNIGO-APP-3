/// WebSocket host resolution — aligned with the API Gateway architecture.
///
/// Flutter talks to ONE public URL only. WebSocket also goes through the
/// gateway (wss://omnigo-app-3-production.up.railway.app/ws).
///
/// For local dev, override with --dart-define=WS_HOST=10.0.2.2
library;

// Production-grade limits. Tuned for mobile networks where TCP keep-alive
// intervals are aggressive and the OS may suspend sockets.
const Duration wsMaxBackoff = Duration(seconds: 30);
// Heartbeat: how often we send a ping while connected. Server is expected
// to echo a pong; if we don't see any traffic for wsStaleThreshold we treat
// the socket as dead and reconnect.
const Duration wsHeartbeatInterval = Duration(seconds: 20);
const Duration wsStaleThreshold = Duration(seconds: 45);
// How long we wait for the WebSocket handshake before giving up.
const Duration wsConnectTimeout = Duration(seconds: 10);

// ── Message Queue (Offline Buffer) ──────────────────────────────────
// Max outbound messages buffered while disconnected. Oldest dropped first.
const int wsMaxOutboxSize = 50;

// ── Backpressure / Throttling ───────────────────────────────────────
// Minimum interval between consecutive UI-bound messages on the same
// topic. Prevents flooding the UI layer when the server pushes bursts
// of rider telemetry or order status frames.
const Duration wsThrottleInterval = Duration(milliseconds: 500);

String resolveWsHost() {
  // Check for dart-define override first
  const override = String.fromEnvironment('WS_HOST');
  if (override.isNotEmpty) return override;

  // Check API_HOST override (shared with ApiEndpoints)
  const apiHost = String.fromEnvironment('API_HOST');
  if (apiHost.isNotEmpty) return apiHost;

  // Production Railway Domain
  return 'omnigo-app-3-production.up.railway.app';
}

/// In production: wss://omnigo-app-3-production.up.railway.app/ws (TLS, port 443)
String wsBaseUrl(String host) {
  if (host.startsWith('wss://') || host.startsWith('ws://')) {
    return host;
  }
  return 'wss://$host/ws';
}
