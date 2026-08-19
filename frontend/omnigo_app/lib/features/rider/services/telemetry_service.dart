import 'dart:async';
import 'dart:convert';
import 'dart:math' as math;
import 'dart:ui';
import 'package:flutter/foundation.dart';
import 'package:flutter_background_service/flutter_background_service.dart';
import 'package:geolocator/geolocator.dart';
import 'package:web_socket_channel/web_socket_channel.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:battery_plus/battery_plus.dart';
import 'package:http/http.dart' as http;
import '../../../core/network/ws_config.dart';
import '../../../core/network/api_endpoints.dart';

String get _resolvedHost => resolveWsHost();

// Decodes a JWT token locally to evaluate expiration.
bool _isTokenExpired(String jwtToken) {
  try {
    final parts = jwtToken.split('.');
    if (parts.length != 3) return true;
    final payload = utf8.decode(base64Url.decode(base64Url.normalize(parts[1])));
    final Map<String, dynamic> claims = jsonDecode(payload) as Map<String, dynamic>;
    final exp = claims['exp'] as int?;
    if (exp == null) return true;
    
    // Refresh 5 minutes before the actual expiry to prevent edge-case race conditions
    final expiryTime = DateTime.fromMillisecondsSinceEpoch(exp * 1000);
    return DateTime.now().isAfter(expiryTime.subtract(const Duration(minutes: 5)));
  } catch (_) {
    return true; // Treat parsing failures as expired to force a safe refresh
  }
}

// Executes an HTTP POST request to refresh the access token using Refresh Token Rotation (RTR).
Future<String?> _refreshAccessToken(String refreshToken) async {
  try {
    final url = Uri.parse(ApiEndpoints.authRefresh());
    final response = await http.post(
      url,
      headers: {'Content-Type': 'application/json'},
      body: jsonEncode({'refresh_token': refreshToken}),
    );
    if (response.statusCode == 200) {
      final data = jsonDecode(response.body);
      final newToken = data['token']?.toString();
      final newRefreshToken = data['refresh_token']?.toString();
      if (newToken != null && newRefreshToken != null) {
        final prefs = await SharedPreferences.getInstance();
        await prefs.setString('jwt_token', newToken);
        await prefs.setString('refresh_token', newRefreshToken);
        return newToken;
      }
    }
  } catch (e) {
    debugPrint('Background Isolate: Access token refresh failed: $e');
  }
  return null;
}

// Buffers coordinates locally in SharedPreferences during offline status
Future<void> _saveToOfflineBuffer(Map<String, dynamic> payload) async {
  try {
    final prefs = await SharedPreferences.getInstance();
    final String rawQueue = prefs.getString('telemetry_offline_queue') ?? '[]';
    final List<dynamic> queue = jsonDecode(rawQueue) as List<dynamic>;
    queue.add(payload);
    await prefs.setString('telemetry_offline_queue', jsonEncode(queue));
    debugPrint('Background Isolate: Telemetry buffered locally. Size: ${queue.length}');
  } catch (e) {
    debugPrint('Background Isolate: Failed to append to offline buffer: $e');
  }
}

@pragma('vm:entry-point')
void onStart(ServiceInstance service) async {
  DartPluginRegistrant.ensureInitialized();

  WebSocketChannel? channel;
  bool isConnected = false;
  bool isConnecting = false;
  int reconnectAttempts = 0;
  Timer? reconnectTimer;

  final Battery battery = Battery();

  // Flushes locally buffered telemetry points to the WebSocket Gateway
  Future<void> flushOfflineBuffer() async {
    if (channel == null || !isConnected) return;
    try {
      final prefs = await SharedPreferences.getInstance();
      final String rawQueue = prefs.getString('telemetry_offline_queue') ?? '[]';
      final List<dynamic> queue = jsonDecode(rawQueue) as List<dynamic>;
      if (queue.isEmpty) return;

      debugPrint('Background Isolate: Flushing ${queue.length} offline coordinates...');
      for (final point in queue) {
        channel!.sink.add(jsonEncode(point));
        await Future<void>.delayed(const Duration(milliseconds: 50)); // Prevent socket congestion
      }
      await prefs.remove('telemetry_offline_queue');
      debugPrint('Background Isolate: Offline buffer successfully flushed and cleared.');
    } catch (e) {
      debugPrint('Background Isolate: Error flushing telemetry buffer: $e');
    }
  }

  // Connects WebSocket with dynamic refresh-token logic
  Future<void> connectWebSocket() async {
    if (isConnecting || isConnected) return;
    isConnecting = true;

    final prefs = await SharedPreferences.getInstance();
    String token = prefs.getString('jwt_token') ?? '';
    final refreshToken = prefs.getString('refresh_token') ?? '';

    // Proactive auto-refresh check
    if (_isTokenExpired(token) && refreshToken.isNotEmpty) {
      debugPrint('Background Isolate: Access token expired. Triggering refresh...');
      final newToken = await _refreshAccessToken(refreshToken);
      if (newToken != null) {
        token = newToken;
      }
    }

    final wsUrl = Uri.parse('${wsBaseUrl(_resolvedHost)}?token=$token');
    try {
      channel = WebSocketChannel.connect(wsUrl);
      channel!.stream.listen(
        (message) {
          if (!isConnected) {
            isConnected = true;
            isConnecting = false;
            reconnectAttempts = 0;
            debugPrint('Background Isolate: Connected to WebSocket Gateway.');
            flushOfflineBuffer();
          }
        },
        onError: (Object err) {
          debugPrint('Background Isolate: WebSocket stream error: $err');
          isConnected = false;
          isConnecting = false;
        },
        onDone: () {
          debugPrint('Background Isolate: WebSocket connection closed.');
          isConnected = false;
          isConnecting = false;
        },
        cancelOnError: true,
      );
    } catch (e) {
      debugPrint('Background Isolate: WebSocket handshaking error: $e');
      isConnected = false;
      isConnecting = false;
    }
  }

  // Set up periodic connection verification and exponential backoff reconnects
  reconnectTimer = Timer.periodic(const Duration(seconds: 10), (timer) async {
    if (!isConnected && !isConnecting) {
      reconnectAttempts++;
      final delaySeconds = reconnectAttempts > 6 ? 60 : reconnectAttempts * 10;
      debugPrint('Background Isolate: Attempting reconnect in $delaySeconds seconds...');
      await Future<void>.delayed(Duration(seconds: delaySeconds));
      await connectWebSocket();
    }
  });

  await connectWebSocket();

  if (service is AndroidServiceInstance) {
    service.on('setAsForeground').listen((event) {
      service.setAsForegroundService();
    });
    service.on('setAsBackground').listen((event) {
      service.setAsBackgroundService();
    });
  }

  service.on('stopService').listen((event) async {
    reconnectTimer?.cancel();
    if (channel != null) {
      try {
        await channel!.sink.close();
      } catch (_) {}
    }
    await service.stopSelf();
  });

  final LocationPermission permission = await Geolocator.checkPermission();
  if (permission == LocationPermission.denied) {
     return;
  }

  // Seeding local Kalman filters for coordinate smoothing
  final latFilter = LocalKalmanFilter(q: 0.00001, r: 0.0001);
  final lngFilter = LocalKalmanFilter(q: 0.00001, r: 0.0001);

  double? lastSentLat;
  double? lastSentLng;
  double lastSentSpeed = 0.0;
  double lastSentBearing = 0.0;
  DateTime lastSendTime = DateTime.fromMillisecondsSinceEpoch(0);

  // Pure Dart Haversine calculator for high-performance distance mapping
  double calculateDistance(double lat1, double lon1, double lat2, double lon2) {
    const p = 0.017453292519943295; // Pi / 180
    final a = 0.5 - math.cos((lat2 - lat1) * p)/2 + 
          math.cos(lat1 * p) * math.cos(lat2 * p) * 
          (1 - math.cos((lon2 - lon1) * p))/2;
    return 12742000.0 * math.asin(math.sqrt(a)); // 2 * R; R = 6371000 meters
  }

  Geolocator.getPositionStream(
    locationSettings: const LocationSettings(
      accuracy: LocationAccuracy.high,
      distanceFilter: 2, // Low filter to feed raw stream to local Kalman filter
    ),
  ).listen((Position position) async {
    int batteryLevel = 100;
    bool isCharging = false;
    try {
      batteryLevel = await battery.batteryLevel;
      final state = await battery.batteryState;
      isCharging = state == BatteryState.charging;
    } catch (_) {}

    // 1. Locally smooth coordinates via Kalman Filters
    final double smoothedLat = latFilter.update(position.latitude);
    final double smoothedLng = lngFilter.update(position.longitude);

    final now = DateTime.now();
    bool forceSend = false;

    // 2. Heartbeat check: force send if threshold elapsed to preserve WS alive state
    final double timeDelta = now.difference(lastSendTime).inMilliseconds / 1000.0;
    final bool isMoving = position.speed >= 0.833;
    final int heartbeatThreshold = isMoving ? 15 : 45;

    if (timeDelta >= heartbeatThreshold || lastSentLat == null || lastSentLng == null) {
      forceSend = true;
    }

    if (!forceSend) {
      // 3. Dead Reckoning check
      // Predict current position based on last sent position & velocity vector
      final double bearingRad = lastSentBearing * math.pi / 180.0;
      final double vx = lastSentSpeed * math.sin(bearingRad);
      final double vy = lastSentSpeed * math.cos(bearingRad);

      // Displacement in meters
      final double dx = vx * timeDelta;
      final double dy = vy * timeDelta;

      // Local flat-Earth Mercator projection conversion
      final double latChange = dy / 111111.0;
      final double lngChange = dx / (111111.0 * math.cos(lastSentLat! * math.pi / 180.0));

      final double predictedLat = lastSentLat! + latChange;
      final double predictedLng = lastSentLng! + lngChange;

      // Calculate spatial deviation from prediction
      final double deviation = calculateDistance(smoothedLat, smoothedLng, predictedLat, predictedLng);

      // Trigger telemetry write if actual coordinates drift by >= 5.0 meters
      if (deviation >= 5.0) {
        forceSend = true;
      }
    }

    if (forceSend) {
      lastSentLat = smoothedLat;
      lastSentLng = smoothedLng;
      lastSentSpeed = position.speed;
      // Geolocator heading can be negative, clamp to [0, 360)
      lastSentBearing = position.heading < 0 
          ? (position.heading % 360.0 + 360.0) % 360.0 
          : position.heading % 360.0;
      lastSendTime = now;

      final prefs = await SharedPreferences.getInstance();
      final trackingId = prefs.getString('tracking_id') ?? '';
      if (trackingId.isEmpty) {
        debugPrint('Background Isolate: No tracking_id found; skipping telemetry send.');
        return;
      }
      final customerId = prefs.getString('active_customer_id') ?? '';
      final orderId = prefs.getString('active_order_id') ?? '';

      // Variable batteryLevel and isCharging are already captured at the start of the stream listener callback.

      final payload = {
        'tracking_id': trackingId,
        'customer_id': customerId,
        'order_id': orderId,
        'timestamp_ms': now.millisecondsSinceEpoch,
        'latitude': smoothedLat,
        'longitude': smoothedLng,
        'speed_mps': lastSentSpeed,
        'bearing_degrees': lastSentBearing,
        'battery_pct': batteryLevel,
        'is_charging': isCharging,
        'status': 'online',
      };

      if (isConnected && channel != null) {
        try {
          channel!.sink.add(jsonEncode(payload));
        } catch (e) {
          isConnected = false;
          await _saveToOfflineBuffer(payload);
        }
      } else {
        await _saveToOfflineBuffer(payload);
      }
    }

    if (service is AndroidServiceInstance) {
      await service.setForegroundNotificationInfo(
        title: "OMNIGO Rider Active",
        content: "Broadcasting location (Battery: $batteryLevel%)",
      );
    }
  });
}

class TelemetryService {
  Future<void> initializeService() async {
    final service = FlutterBackgroundService();

    await service.configure(
      androidConfiguration: AndroidConfiguration(
        onStart: onStart,
        autoStart: false,
        isForegroundMode: true,
        notificationChannelId: 'rider_telemetry',
        initialNotificationTitle: 'Rider Online',
        initialNotificationContent: 'Connecting to OMNIGO Network...',
        foregroundServiceNotificationId: 888,
      ),
      iosConfiguration: IosConfiguration(
        autoStart: false,
        onForeground: onStart,
        onBackground: (ServiceInstance service) {
          return true;
        },
      ),
    );
  }

  bool _started = false;

  Future<void> goOnline() async {
    if (_started) return;
    _started = true;
    final service = FlutterBackgroundService();
    await service.startService();
  }

  Future<void> goOffline() async {
    _started = false;
    final service = FlutterBackgroundService();
    service.invoke('stopService');
  }
}

/// A 1D Kalman Filter implementation to locally smooth GPS coordinate tracking streams.
class LocalKalmanFilter {

  LocalKalmanFilter({required this.q, required this.r});
  final double q; // Process covariance noise
  final double r; // Measurement covariance noise
  double _x = 0;  // State estimate
  double _p = 1.0; // Covariance estimate
  bool _initialized = false;

  void init(double initialValue) {
    _x = initialValue;
    _p = 1.0;
    _initialized = true;
  }

  bool get isInitialized => _initialized;

  double update(double measurement) {
    if (!_initialized) {
      init(measurement);
      return measurement;
    }
    // Time Update (Prediction)
    _p = _p + q;

    // Measurement Update (Correction)
    final double k = _p / (_p + r);
    _x = _x + k * (measurement - _x);
    _p = (1.0 - k) * _p;

    return _x;
  }
}
