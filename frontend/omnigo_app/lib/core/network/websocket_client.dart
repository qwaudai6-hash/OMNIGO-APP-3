import 'dart:async';
import 'dart:convert';
import 'dart:math' as math;
import 'package:flutter/foundation.dart';
import 'package:web_socket_channel/web_socket_channel.dart';
import 'ws_config.dart';

/// Connection state of the WebSocket gateway link.
enum WSConnectionState { disconnected, connecting, connected, reconnecting }

/// Internal lifecycle events surfaced to UI (rider live map, dashboard).
@immutable
class WSHealthSnapshot {
  const WSHealthSnapshot({
    required this.state,
    required this.connectAttempts,
    required this.lastMessageAt,
    required this.heartbeatsSent,
    required this.heartbeatsMissed,
  });
  final WSConnectionState state;
  final int connectAttempts;
  final DateTime? lastMessageAt;
  final int heartbeatsSent;
  final int heartbeatsMissed;
}

/// WebSocketClient is a resilient, broadcast-safe wrapper around the
/// Rust WebSocket gateway (port 8087, proxied through the gateway on /ws).
///
/// Production-grade additions (Session 59):
///   - Heartbeat ping every wsHeartbeatInterval; stale socket (no traffic
///     for wsStaleThreshold) → force-reconnect.
///   - Reconnect attempts + heartbeat stats are exposed via healthStream so
///     ops/UI can show a "Reconnecting (3)" badge or telemetry counter.
///   - pause()/resume() to gracefully handle app background → foreground on
///     mobile (doze mode drops idle sockets).
///   - Bounded reconnect attempts with full-jitter backoff, cap from
///     ws_config.wsMaxBackoff.
///
/// Production-grade additions (Session 66):
///   - **Message Queue (Offline Buffer)**: Outbound messages are buffered
///     while the socket is disconnected and flushed automatically on
///     reconnect. Bounded to wsMaxOutboxSize; oldest messages dropped first.
///   - **Topic Multiplexing**: Logical channels over the single physical
///     connection. Call `topicStream('orders')` to get a stream filtered
///     to messages with `{"topic": "orders", ...}`. Messages without a
///     `topic` field are broadcast to all listeners (backward compatible).
///   - **Backpressure (Throttle)**: Per-topic emission throttle prevents
///     UI flooding when the server pushes bursts of telemetry frames.
class WebSocketClient {
  WebSocketClient() {
    _host = resolveWsHost();
    _baseWsUrl = wsBaseUrl(_host);
  }
  late final String _host;
  late final String _baseWsUrl;

  WebSocketChannel? _channel;
  final StreamController<dynamic> _broadcastController =
      StreamController<dynamic>.broadcast();
  Stream<dynamic> get stream => _broadcastController.stream;
  StreamSubscription<dynamic>? _internalSub;
  Timer? _reconnectTimer;
  Timer? _heartbeatTimer;
  int _backoffSeconds = 1;
  bool _manuallyClosed = false;
  bool _isPaused = false;
  int _connectAttempts = 0;
  int _heartbeatsSent = 0;
  int _heartbeatsMissed = 0;
  DateTime? _lastMessageAt;
  String? _activeToken;

  // ── Message Queue (Offline Buffer) ────────────────────────────────
  final List<String> _outbox = [];
  int get outboxLength => _outbox.length;
  bool get isConnected => _state == WSConnectionState.connected;

  // ── Topic Multiplexing ────────────────────────────────────────────
  final Map<String, StreamController<dynamic>> _topicControllers = {};
  final Map<String, DateTime> _topicLastEmit = {};

  /// Returns a stream filtered to messages whose JSON payload contains
  /// `"topic": [topic]`. Messages without a `topic` field are included
  /// (backward compatible with existing gateway frames). The stream is
  /// throttled per-topic to prevent UI flooding.
  Stream<dynamic> topicStream(String topic) {
    return _getOrCreateTopicController(topic).stream;
  }

  StreamController<dynamic> _getOrCreateTopicController(String topic) {
    return _topicControllers.putIfAbsent(topic, () {
      final ctrl = StreamController<dynamic>.broadcast();
      return ctrl;
    });
  }

  WSConnectionState _state = WSConnectionState.disconnected;
  final StreamController<WSConnectionState> _stateController =
      StreamController<WSConnectionState>.broadcast();
  Stream<WSConnectionState> get stateStream => _stateController.stream;

  final StreamController<WSHealthSnapshot> _healthController =
      StreamController<WSHealthSnapshot>.broadcast();
  Stream<WSHealthSnapshot> get healthStream => _healthController.stream;

  WSConnectionState get state => _state;
  WSHealthSnapshot get health => WSHealthSnapshot(
        state: _state,
        connectAttempts: _connectAttempts,
        lastMessageAt: _lastMessageAt,
        heartbeatsSent: _heartbeatsSent,
        heartbeatsMissed: _heartbeatsMissed,
      );

  void _setState(WSConnectionState next) {
    if (_state == next) return;
    _state = next;
    if (!_stateController.isClosed) _stateController.add(_state);
    _emitHealth();
  }

  void _emitHealth() {
    if (_healthController.isClosed) return;
    _healthController.add(health);
  }

  void _markMessageReceived() {
    _lastMessageAt = DateTime.now();
    _heartbeatsMissed = 0;
    _emitHealth();
  }

  /// Opens a connection to the WS gateway using the user's JWT token.
  ///
  /// Default assumes a rider client (legacy semantics). For customers or
  /// vendors pass [clientType] and [trackingId] so the gateway can route
  /// telemetry frames to the right socket.
  void connect(String token, {String clientType = 'rider', String? trackingId}) {
    _activeToken = token;
    _clientType = clientType;
    _trackingId = trackingId;
    _manuallyClosed = false;
    _isPaused = false;
    _connectInternal();
  }

  String _clientType = 'rider';
  String? _trackingId;

  void _connectInternal() {
    if (_activeToken == null || _activeToken!.isEmpty) return;
    if (_isPaused) return;

    _teardownChannel();
    _connectAttempts++;
    _setState(_connectAttempts == 1
        ? WSConnectionState.connecting
        : WSConnectionState.reconnecting,);

    final uri = Uri.parse('$_baseWsUrl?token=$_activeToken');
    try {
      _channel = WebSocketChannel.connect(uri);
    } catch (e) {
      debugLog('connect() threw: $e');
      _scheduleReconnect();
      return;
    }

    _internalSub = _channel!.stream.listen(
      (message) {
        _markMessageReceived();
        if (!_broadcastController.isClosed) {
          _broadcastController.add(message);
        }
        // Server-driven "hello" frame (sent by the proxy on connect) or any
        // pong/pong echo counts as a heartbeat round-trip success.
        if (message is String &&
            (message.contains('"type":"ws.hello"') ||
                message.contains('"type":"pong"'))) {
          _heartbeatsMissed = 0;
        }

        // ── Topic Routing ──────────────────────────────────────────
        _routeToTopicController(message);
      },
      onError: (Object error) {
        debugLog('WebSocket Error: $error');
        if (!_broadcastController.isClosed) {
          _broadcastController.addError(error);
        }
        _stopHeartbeat();
        _scheduleReconnect();
      },
      onDone: () {
        debugLog('WebSocket connection closed.');
        _setState(WSConnectionState.disconnected);
        _stopHeartbeat();
        if (!_manuallyClosed && !_isPaused) {
          _scheduleReconnect();
        }
      },
    );

    _setState(WSConnectionState.connected);
    _backoffSeconds = 1; // reset backoff after a successful connect

    // ── Flush Outbox ───────────────────────────────────────────────
    _flushOutbox();

    // Send the register handshake so the gateway knows what kind of client
    // we are. For riders we don't need this (legacy path auto-detects via
    // the first telemetry frame), but customers and vendors MUST send it
    // or the gateway will silently never send them telemetry frames.
    if (_clientType != 'rider' && (_trackingId ?? '').isNotEmpty) {
      _sendRegister();
    }

    _startHeartbeat();
  }

  void _sendRegister() {
    final sink = _channel?.sink;
    if (sink == null) return;
    try {
      sink.add(jsonEncode({
        'type': 'register',
        'client_type': _clientType,
        'tracking_id': _trackingId,
      }),);
    } catch (_) {
      // Sink probably already closed; onDone will fire.
    }
  }

  void _startHeartbeat() {
    _heartbeatTimer?.cancel();
    _heartbeatTimer = Timer.periodic(wsHeartbeatInterval, (_) {
      // If we haven't seen ANY traffic in `wsStaleThreshold`, the socket is
      // probably dead (mobile doze, NAT timeout, server restart). Tear down
      // and let the backoff loop take over.
      if (_lastMessageAt != null &&
          DateTime.now().difference(_lastMessageAt!) > wsStaleThreshold) {
        _heartbeatsMissed++;
        debugLog(
            'heartbeat stale for >${wsStaleThreshold.inSeconds}s — forcing reconnect (missed=$_heartbeatsMissed)',);
        if (_heartbeatsMissed >= 2) {
          _teardownChannel();
          _setState(WSConnectionState.reconnecting);
          _scheduleReconnect();
          return;
        }
      }
      _sendHeartbeat();
    });
  }

  void _stopHeartbeat() {
    _heartbeatTimer?.cancel();
    _heartbeatTimer = null;
  }

  void _sendHeartbeat() {
    final sink = _channel?.sink;
    if (sink == null) return;
    try {
      sink.add(jsonEncode({
        'type': 'ping',
        'ts': DateTime.now().toUtc().toIso8601String(),
      }),);
      _heartbeatsSent++;
      _emitHealth();
    } catch (_) {
      // Sink probably already closed; onDone will fire.
    }
  }

  /// Schedules an automatic reconnect with full-jitter exponential backoff.
  void _scheduleReconnect() {
    if (_manuallyClosed || _isPaused) return;
    _setState(WSConnectionState.reconnecting);

    // Full jitter: random delay in [0, baseMs]. AWS-recommended pattern.
    final int cap = wsMaxBackoff.inSeconds;
    final int baseMs = (_backoffSeconds * 1000).clamp(500, cap * 1000);
    final int jitterMs = math.Random().nextInt(baseMs + 1);
    final Duration delay = Duration(milliseconds: jitterMs);

    _backoffSeconds = (_backoffSeconds * 2).clamp(1, cap);

    _reconnectTimer?.cancel();
    _reconnectTimer = Timer(delay, () {
      debugLog('reconnect attempt #$_connectAttempts after ${delay.inMilliseconds}ms');
      _connectInternal();
    });
  }

  /// Pause the client. Used when the app goes to background on mobile — we
  /// don't want the reconnect loop hammering the server while the OS has
  /// frozen our socket anyway. Call [resume] when the app is foregrounded.
  void pause() {
    if (_isPaused) return;
    _isPaused = true;
    _reconnectTimer?.cancel();
    _reconnectTimer = null;
    _stopHeartbeat();
    _teardownChannel();
    _setState(WSConnectionState.disconnected);
  }

  /// Resume the client. If a token is still active, reconnect immediately.
  void resume() {
    if (!_isPaused) return;
    _isPaused = false;
    if (!_manuallyClosed && (_activeToken ?? '').isNotEmpty) {
      _connectInternal();
    }
  }

  /// Sends a message to the gateway. If disconnected, the message is
  /// buffered in the outbox and flushed on the next reconnect. Returns
  /// `true` if the message was sent immediately, `false` if buffered.
  bool sendMessage(String message) {
    final ch = _channel;
    if (ch == null || _state != WSConnectionState.connected) {
      _enqueueOutbox(message);
      return false;
    }
    try {
      ch.sink.add(message);
      return true;
    } catch (_) {
      _enqueueOutbox(message);
      return false;
    }
  }

  // ── Message Queue (Offline Buffer) ────────────────────────────────

  void _enqueueOutbox(String message) {
    if (_outbox.length >= wsMaxOutboxSize) {
      _outbox.removeAt(0); // drop oldest
    }
    _outbox.add(message);
  }

  void _flushOutbox() {
    if (_outbox.isEmpty) return;
    final ch = _channel;
    if (ch == null) return;
    final pending = List<String>.from(_outbox);
    _outbox.clear();
    for (final msg in pending) {
      try {
        ch.sink.add(msg);
      } catch (_) {
        // Re-queue remaining messages on failure.
        _outbox.addAll(pending.sublist(pending.indexOf(msg)));
        break;
      }
    }
  }

  // ── Topic Multiplexing + Backpressure ─────────────────────────────

  /// Routes an incoming message to the appropriate topic controller.
  /// Messages with a `"topic"` field go to that specific controller.
  /// Messages without a `"topic"` are broadcast to ALL topic controllers
  /// (backward compatible with existing gateway frames that lack topics).
  void _routeToTopicController(dynamic message) {
    if (message is! String) return;
    Map<String, dynamic>? frame;
    try {
      frame = jsonDecode(message) as Map<String, dynamic>;
    } catch (_) {
      return;
    }
    final topic = frame['topic'] as String?;

    if (topic != null && topic.isNotEmpty) {
      // Targeted delivery to the specific topic.
      _emitToTopic(topic, message);
    } else {
      // Untyped frame — broadcast to all registered topics (backward compat).
      for (final t in _topicControllers.keys) {
        _emitToTopic(t, message);
      }
    }
  }

  /// Emits a message to a topic controller, subject to per-topic throttle.
  void _emitToTopic(String topic, dynamic message) {
    final ctrl = _topicControllers[topic];
    if (ctrl == null || ctrl.isClosed) return;

    // Backpressure: skip if within throttle interval.
    final now = DateTime.now();
    final last = _topicLastEmit[topic];
    if (last != null && now.difference(last) < wsThrottleInterval) {
      return;
    }
    _topicLastEmit[topic] = now;
    ctrl.add(message);
  }

  /// Cleanly disconnects and stops any reconnection attempts.
  void disconnect() {
    _manuallyClosed = true;
    _reconnectTimer?.cancel();
    _reconnectTimer = null;
    _stopHeartbeat();
    _teardownChannel();
    _setState(WSConnectionState.disconnected);
  }

  /// Force a token refresh + reconnect (called by auth service on 401).
  void reconnectWithToken(String newToken) {
    _activeToken = newToken;
    _manuallyClosed = false;
    _reconnectTimer?.cancel();
    _teardownChannel();
    _connectInternal();
  }

  void _teardownChannel() {
    _internalSub?.cancel();
    _internalSub = null;
    try {
      _channel?.sink.close();
    } catch (_) {}
    _channel = null;
  }

  void debugLog(String msg) {
    if (kDebugMode) {
      // ignore: avoid_print
      print('[WebSocketClient] $msg');
    }
  }

  /// Release all resources. Call only on app teardown.
  Future<void> dispose() async {
    disconnect();
    if (!_broadcastController.isClosed) {
      await _broadcastController.close();
    }
    await _stateController.close();
    await _healthController.close();
    for (final ctrl in _topicControllers.values) {
      if (!ctrl.isClosed) await ctrl.close();
    }
    _topicControllers.clear();
  }
}
