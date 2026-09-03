import 'dart:convert';
import 'dart:async';
import 'dart:math' as math;
import 'dart:io';
import 'package:flutter/foundation.dart' show kIsWeb;
import 'package:flutter/material.dart';
import 'package:maplibre_gl/maplibre_gl.dart';
import 'package:http/http.dart' as http;
import 'package:geolocator/geolocator.dart';
import 'package:image_picker/image_picker.dart';
import 'package:url_launcher/url_launcher.dart';
import 'package:shared_preferences/shared_preferences.dart';
import '../../../../core/services/connectivity_service.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/network/websocket_client.dart';
import '../../../../core/network/api_client.dart';
import '../../../../shared/presentation/screens/chat_list_screen.dart';
import '../../../../shared/presentation/services/chat_service.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/di/service_locator.dart';
import '../../../../core/services/session_registry.dart';
import '../../../shared/presentation/widgets/map_libre_map_widget.dart';
import '../../services/telemetry_service.dart';
import '../../services/offline_gig_storage.dart';
import '../widgets/notification_alert_dialog.dart';

class RiderMapScreen extends StatefulWidget {
  const RiderMapScreen({super.key, required this.trackingId});
  final String trackingId;

  @override
  RiderMapScreenState createState() => RiderMapScreenState();
}

class RiderMapScreenState extends State<RiderMapScreen> with WidgetsBindingObserver {
  int _selectedTabIndex = 0;
  LatLng? _center; // Set by GPS only — no hardcoded default
  bool _isOnline = false;
  String _activeGigStatus = 'No Active Gig';
  final TelemetryService _telemetryService = TelemetryService();

  // Verification
  File? _cnicFile;
  File? _licenseFile;
  File? _vehicleRegFile;
  bool _isUploadingDocs = false;

  // Live data
  Map<String, dynamic>? _broadcastedGig;
  List<LatLng> _routePolyline = [];
  double? _routeEtaMinutes;
  double? _routeDistanceKm;
  StreamSubscription<dynamic>? _wsSubscription;
  StreamSubscription<dynamic>? _wsGigTopicSub;
  StreamSubscription<Position>? _positionStream;

  // Deviation & rerouting states
  int _deviationTicks = 0;
  DateTime? _lastRerouteTime;

  // Auto-follow GPS state
  final bool _followGps = true;
  DateTime? _lastUserPan;

  // Pick-and-drop ride mode
  bool _isPickAndDropActive = true;

  // Surge Heatmaps
  List<dynamic> _surgeHeatmaps = [];
  Timer? _heatmapTimer;

  // Mapbox Navigation removed — turn-by-turn handled via deep-link to
  // OSRM-backed polyline drawn on the MapLibre map. The flutter_mapbox_navigation
  // package was removed because it is no longer in pubspec.yaml; the Go
  // map-service can be wired in later for native turn-by-turn routing.
  final WebSocketClient _wsClient = sl<WebSocketClient>();
  final ApiClient _apiClient = sl<ApiClient>();
  MapLibreMapController? _mapController;
  List<dynamic> _codDebts = [];
  bool _isLoadingCodDebts = false;
  Map<String, dynamic>? _walletSummary;

  StreamSubscription<bool>? _connectivitySubscription;
  bool _isConnected = true;
  final ConnectivityService _connectivityService = ConnectivityService();

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _initConnectivity();
    _connectWebSocket();
    _telemetryService.initializeService();
    _initLocationTracking();
    _initWebSocketListener();
    _loadOfflineState();
    _startHeatmapPolling();
    _fetchCodDebts();
    _fetchWalletSummary();
  }

  /// Launch turn-by-turn navigation. MapBox flutter_mapbox_navigation has been
  /// replaced with our own MapLibre polyline overlay plus a deep-link to the
  /// OSRM routing engine via the Go map-service. We just request the route
  /// geometry (already stored in `_routePolyline` by `_loadRoute`) and animate
  /// the camera to the destination. Native turn-by-turn can be re-introduced
  /// later by wiring a new in-house Flutter plugin over OSRM/Valhalla.
  void _startNavigation() async {
    if (_center == null || _gigDropoffPoint() == null) return;
    final destination = _activeGigStatus == 'accepted'
        ? _gigPickupPoint()
        : _gigDropoffPoint();
    if (destination == null) return;

    // Animate the camera to fit the entire route on screen.
    final points = <LatLng>[
      _center!,
      if (_routePolyline.isNotEmpty) ..._routePolyline else destination,
    ];
    if (points.length >= 2) {
      final bounds = _latLngBoundsFromPoints(points);
      if ((bounds.northeast.latitude - bounds.southwest.latitude).abs() < 1e-6 &&
          (bounds.northeast.longitude - bounds.southwest.longitude).abs() < 1e-6) {
        _mapController?.animateCamera(
          CameraUpdate.newLatLngZoom(destination, 15.0),
        );
      } else {
        _mapController?.animateCamera(
          CameraUpdate.newLatLngBounds(
            bounds,
            left: 80,
            top: 80,
            right: 80,
            bottom: 80,
          ),
        );
      }
    } else {
      _mapController?.animateCamera(
        CameraUpdate.newLatLngZoom(destination, 15.0),
      );
    }
  }

  LatLngBounds _latLngBoundsFromPoints(List<LatLng> points) {
    double minLat = 90, maxLat = -90, minLng = 180, maxLng = -180;
    for (final p in points) {
      if (p.latitude < minLat) minLat = p.latitude;
      if (p.latitude > maxLat) maxLat = p.latitude;
      if (p.longitude < minLng) minLng = p.longitude;
      if (p.longitude > maxLng) maxLng = p.longitude;
    }
    return LatLngBounds(
      southwest: LatLng(minLat, minLng),
      northeast: LatLng(maxLat, maxLng),
    );
  }

  void _startHeatmapPolling() {
    _fetchSurgeHeatmaps();
    _heatmapTimer = Timer.periodic(const Duration(minutes: 1), (_) {
      _fetchSurgeHeatmaps();
    });
  }

  Future<void> _fetchSurgeHeatmaps() async {
    try {
      final response = await _apiClient.get(ApiEndpoints.deliverySurgeHeatmap());
      Map<String, dynamic>? data;
      if (response is String) {
        data = jsonDecode(response) as Map<String, dynamic>;
      } else if (response is Map<String, dynamic>) {
        data = response;
      }
      if (data != null && data['heatmaps'] != null && mounted) {
        setState(() {
          _surgeHeatmaps = data!['heatmaps'] as List<dynamic>;
        });
      }
    } catch (e) {
      debugPrint('Failed to fetch heatmaps: $e');
    }
  }

  Future<void> _connectWebSocket() async {
    await SessionRegistry.instance.hydrate();
    final token = SessionRegistry.instance.token;
    if (token != null && token.isNotEmpty) {
      _wsClient.connect(
        token,
        clientType: 'rider',
        trackingId: SessionRegistry.instance.trackingId ?? '',
      );
      // Bind the chat service to the WS so incoming chat messages are
      // surfaced to the chat screen.
      await ChatService.instance.bindToWebSocket(_wsClient);
      await ChatService.instance
          .setUserId(SessionRegistry.instance.trackingId ?? '');
    }
  }

  Future<void> _loadOfflineState() async {
    await OfflineGigStorage.init();
    final cachedGig = OfflineGigStorage.getActiveGig();
    if (cachedGig != null && mounted) {
      setState(() {
        _broadcastedGig = cachedGig;
        _activeGigStatus = (cachedGig['status'] as String?) ?? 'accepted';
      });
      unawaited(_loadRoute());
      
      // Retry any pending syncs
      final pending = await OfflineGigStorage.consumePendingStatusSync();
      if (pending != null) {
        final status = pending['status'] as String;
        final otp = pending['otp_code'] as String?;
        final localPhotoPath = pending['local_photo_path'] as String?;
        unawaited(_syncPendingOfflineStatus(status, otp, localPhotoPath));
      }
    }
  }

  Future<void> _syncPendingOfflineStatus(String status, String? otp, String? localPhotoPath) async {
    String? photoUrl;
    if (localPhotoPath != null) {
      final file = File(localPhotoPath);
      if (await file.exists()) {
        photoUrl = await _uploadPhoto(file);
      }
    }
    await _sendGigStatusUpdate(status, otp: otp, photoUrl: photoUrl);
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _connectivitySubscription?.cancel();
    _wsSubscription?.cancel();
    _wsGigTopicSub?.cancel();
    _positionStream?.cancel();
    _heatmapTimer?.cancel();
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) {
      if (_isConnected) {
        _initLocationTracking();
      }
    }
  }

  void _initConnectivity() {
    _connectivityService.isOnline().then((isConnected) {
      if (mounted) {
        setState(() {
          _isConnected = isConnected;
        });
      }
    });

    _connectivitySubscription = _connectivityService.onConnectivityChanged.listen((isConnected) {
      if (_isConnected != isConnected) {
        if (!mounted) return;
        setState(() {
          _isConnected = isConnected;
        });
        if (!isConnected) {
          ScaffoldMessenger.of(context).showMaterialBanner(
            MaterialBanner(
              content: const Text('No Internet Connection', style: TextStyle(color: Colors.white)),
              backgroundColor: Colors.red,
              actions: [
                TextButton(
                  onPressed: () => ScaffoldMessenger.of(context).hideCurrentMaterialBanner(),
                  child: const Text('DISMISS', style: TextStyle(color: Colors.white)),
                ),
              ],
            ),
          );
        } else {
          ScaffoldMessenger.of(context).hideCurrentMaterialBanner();
          ScaffoldMessenger.of(context).showSnackBar(
            const SnackBar(content: Text('Internet Restored'), backgroundColor: Colors.green),
          );
        }
      }
    });
  }

  double _distanceToPolyline(LatLng point, List<LatLng> polyline) {
    if (polyline.isEmpty) return double.maxFinite;
    double minDistance = double.maxFinite;

    for (int i = 0; i < polyline.length - 1; i++) {
      final p1 = polyline[i];
      final p2 = polyline[i + 1];

      final double latP = point.latitude;
      final double lngP = point.longitude;
      final double lat1 = p1.latitude;
      final double lng1 = p1.longitude;
      final double lat2 = p2.latitude;
      final double lng2 = p2.longitude;

      // Project spherical coordinates using cosine latitude adjustment for Lahore scale
      final double latRad = ((lat1 + lat2) / 2.0) * math.pi / 180.0;
      final double cosLat = math.cos(latRad);

      final double dx = (lng2 - lng1) * cosLat;
      final double dy = lat2 - lat1;
      final double dxP = (lngP - lng1) * cosLat;
      final double dyP = latP - lat1;

      final double segmentLenSq = dx * dx + dy * dy;
      if (segmentLenSq < 1e-12) {
        final dist = Geolocator.distanceBetween(latP, lngP, lat1, lng1);
        if (dist < minDistance) minDistance = dist;
        continue;
      }

      // Linear interpolation projection ratio
      double t = (dxP * dx + dyP * dy) / segmentLenSq;
      t = t.clamp(0.0, 1.0);

      final double closestLat = lat1 + t * (lat2 - lat1);
      final double closestLng = lng1 + t * (lng2 - lng1);

      final dist = Geolocator.distanceBetween(latP, lngP, closestLat, closestLng);
      if (dist < minDistance) minDistance = dist;
    }

    return minDistance;
  }

  Future<void> _showLocationServiceOffDialog() async {
    return showDialog(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Location Services Disabled'),
        content: const Text('Please enable location services to use the rider map.'),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('OK'),
          ),
        ],
      ),
    );
  }

  Future<void> _showPermissionDeniedDialog() async {
    return showDialog(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Location Permission Denied'),
        content: const Text('Location permission is required to track your rides.'),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('OK'),
          ),
        ],
      ),
    );
  }

  Future<void> _showPermissionPermanentlyDeniedDialog() async {
    return showDialog(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Location Permission Permanently Denied'),
        content: const Text('Please enable location permissions in app settings to use the rider map.'),
        actions: [
          TextButton(
            onPressed: () {
              Navigator.pop(context);
              Geolocator.openAppSettings();
            },
            child: const Text('Open Settings'),
          ),
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('Cancel'),
          ),
        ],
      ),
    );
  }

  Future<void> _initLocationTracking() async {
    try {
      final bool serviceEnabled = await Geolocator.isLocationServiceEnabled();
      if (!serviceEnabled) {
        if (mounted) await _showLocationServiceOffDialog();
        return;
      }

      LocationPermission permission = await Geolocator.checkPermission();
      if (permission == LocationPermission.denied) {
        permission = await Geolocator.requestPermission();
        if (permission == LocationPermission.denied) {
          if (mounted) await _showPermissionDeniedDialog();
          return;
        }
      }
      if (permission == LocationPermission.deniedForever) {
        if (mounted) await _showPermissionPermanentlyDeniedDialog();
        return;
      }

      await _positionStream?.cancel();
      _positionStream = Geolocator.getPositionStream(
        locationSettings: const LocationSettings(
          accuracy: LocationAccuracy.high,
          distanceFilter: 10,
        ),
      ).listen((Position position) {
        final latLng = LatLng(position.latitude, position.longitude);
        if (mounted) {
          setState(() {
            _center = latLng;
          });
        }
        if (_followGps &&
          (_lastUserPan == null ||
              DateTime.now().difference(_lastUserPan!) > const Duration(seconds: 5))) {
        _mapController?.animateCamera(
          CameraUpdate.newLatLngZoom(latLng, 15.0),
        );
      }

      // Perform route deviation check locally
      if (_routePolyline.isNotEmpty) {
        final currentLatLng = LatLng(position.latitude, position.longitude);
        final distanceToRoute = _distanceToPolyline(currentLatLng, _routePolyline);
        if (distanceToRoute > 80.0) {
          _deviationTicks++;
          if (_deviationTicks >= 3) {
            final now = DateTime.now();
            if (_lastRerouteTime == null || now.difference(_lastRerouteTime!) > const Duration(seconds: 30)) {
              _lastRerouteTime = now;
              if (mounted) {
                ScaffoldMessenger.of(context).showSnackBar(
                  const SnackBar(content: Text('Rerouting...'), duration: Duration(seconds: 2)),
                );
              }
              _loadRoute();
            }
          }
        } else {
          _deviationTicks = 0;
        }
      }

      // Send location via WS for telemetry. The Rust gateway requires a
      // TelemetryEvent shape; customer_id/order_id are optional and
      // the gateway forwards the packet to the linked customer session.
      if (_wsClient.state == WSConnectionState.connected) {
        _wsClient.sendMessage(jsonEncode({
          "customer_id": "",
          "order_id": _broadcastedGig != null ? _broadcastedGig!['order_tracking_id'] : "",
          "vector_clock": DateTime.now().millisecondsSinceEpoch,
          "lat": position.latitude,
          "lng": position.longitude,
        }),);
      }
    });
    } catch (e) {
      debugPrint('Location tracking disabled on this platform: $e');
    }
  }

  LatLng? _gigPickupPoint() {
    if (_broadcastedGig == null) return _center;
    final lat = _broadcastedGig!['pickup_lat'];
    final lng = _broadcastedGig!['pickup_lng'];
    if (lat == null || lng == null) return _center;
    return LatLng((lat as num).toDouble(), (lng as num).toDouble());
  }

  LatLng? _gigDropoffPoint() {
    if (_broadcastedGig == null) return _center;
    final lat = _broadcastedGig!['dropoff_lat'];
    final lng = _broadcastedGig!['dropoff_lng'];
    if (lat == null || lng == null) return _center;
    return LatLng((lat as num).toDouble(), (lng as num).toDouble());
  }

  Future<void> _loadRoute() async {
    if (_broadcastedGig == null) return;
    final gigId = _broadcastedGig!['tracking_id'] as String?;
    if (gigId == null) return;
    try {
      final response = await _apiClient.get(ApiEndpoints.deliveryGigRoute(gigId));
      final data = jsonDecode(response as String) as Map<String, dynamic>;
      final coordsList = data['coordinates'] as List<dynamic>?;
      final distance = data['distance_meters'] as num?;
      final duration = data['duration_seconds'] as num?;
      if (coordsList != null && coordsList.isNotEmpty) {
        final List<LatLng> points = coordsList
            .map((c) => LatLng((c[1] as num).toDouble(), (c[0] as num).toDouble()))
            .toList();
        if (mounted) {
          setState(() {
            _routePolyline = points;
            _routeDistanceKm = distance != null ? distance.toDouble() / 1000.0 : null;
            _routeEtaMinutes = duration != null ? duration.toDouble() / 60.0 : null;
          });
        }
      }
    } catch (e) {
      debugPrint('Failed to load route: $e');
    }
  }

  void _initWebSocketListener() {
    _wsSubscription = _wsClient.stream.listen((message) {
      try {
        final data = jsonDecode(message as String) as Map<String, dynamic>;
        final eventType = (data['action'] as String? ?? data['event'] as String? ?? '').toUpperCase();
        if (eventType == 'GIG_BROADCAST' || eventType == 'DELIVERY_BROADCAST' || eventType == 'RIDE_BROADCAST') {
          if (_activeGigStatus == 'No Active Gig' && mounted) {
            showDialog<void>(
              context: context,
              barrierDismissible: false,
              builder: (context) => NotificationAlertDialog(
                gigData: data,
                onAccept: () {
                  Navigator.pop(context);
                  setState(() {
                    _broadcastedGig = data;
                  });
                  if (eventType == 'RIDE_BROADCAST') {
                    _acceptRide();
                  } else {
                    _acceptGig();
                  }
                },
                onDecline: () {
                  Navigator.pop(context);
                },
              ),
            );
          }
        }
        // ORDER_CANCELLED / GIG_CANCELLED handled by topic stream below.
      } catch (e) {
        debugPrint('WS error: $e');
      }
    });

    // ── Topic Stream: Gig Updates ───────────────────────────────────
    // Throttled topic stream for gig broadcasts. Backward compatible:
    // untagged gateway frames are broadcast to all topic controllers.
    _wsGigTopicSub = _wsClient.topicStream('gigs').listen((message) {
      try {
        final data = jsonDecode(message as String) as Map<String, dynamic>;
        final eventType = (data['action'] as String? ?? data['event'] as String? ?? '').toUpperCase();
        if (eventType == 'ORDER_CANCELLED' || eventType == 'GIG_CANCELLED') {
          if (mounted && _activeGigStatus != 'No Active Gig') {
            final orderId = data['order_id']?.toString() ?? '';
            setState(() {
              _activeGigStatus = 'No Active Gig';
              _broadcastedGig = null;
            });
            ScaffoldMessenger.of(context).showSnackBar(
              SnackBar(
                content: Text('Order ${orderId.isNotEmpty ? orderId : ""} has been cancelled. Looking for new deliveries...'),
                backgroundColor: Colors.orange,
                duration: const Duration(seconds: 4),
              ),
            );
          }
        }
      } catch (e) {
        debugPrint('WS gig topic parse error: $e');
      }
    });
  }

  Future<void> _acceptGig() async {
    if (_broadcastedGig == null) return;
    final gigId = _broadcastedGig!['tracking_id'] as String?;
    final storeId = _broadcastedGig!['vendor_store_tracking_id'] as String?;

    setState(() {
      _activeGigStatus = 'Accepting...';
    });

    try {
      await _apiClient.post(ApiEndpoints.deliveryGigAccept(), {
        "tracking_id": gigId,
        "rider_tracking_id": widget.trackingId,
      });

      _broadcastedGig!['status'] = 'accepted';
      await OfflineGigStorage.saveActiveGig(_broadcastedGig!);
      
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('active_customer_id', (_broadcastedGig!['customer_tracking_id'] as String?) ?? '');
      await prefs.setString('active_order_id', (_broadcastedGig!['order_tracking_id'] as String?) ?? '');

      if (mounted) {
        setState(() {
          _activeGigStatus = 'accepted';
        });
        unawaited(_loadRoute());
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Gig Accepted! $gigId ➔ $storeId')),
        );
      }
    } on Exception catch (e) {
      final errStr = e.toString().toLowerCase();
      String message = 'Unable to accept gig. Please try again.';
      bool showVerificationDialog = false;

      if (errStr.contains('order limit reached') || errStr.contains('unverified rider')) {
        message = '10-Order Limit reached for unverified account. Please submit KYC verification.';
        showVerificationDialog = true;
      } else if (errStr.contains('already taken') || errStr.contains('conflict') || errStr.contains('409')) {
        message = 'Gig was taken by another rider.';
      } else if (errStr.contains('not in eligible') || errStr.contains('location')) {
        message = 'You are outside the eligible delivery zone. Move closer to the pickup area.';
      } else if (errStr.contains('suspended')) {
        message = 'Account suspended. Contact support.';
      }

      if (mounted) {
        setState(() {
          _broadcastedGig = null;
          _activeGigStatus = 'No Active Gig';
        });
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text(message)),
        );
        if (showVerificationDialog) {
          _showVerificationPromptDialog();
        }
      }
    }
  }

  Future<File?> _capturePhoto(String title) async {
    final picker = ImagePicker();
    final pickedFile = await picker.pickImage(
      source: ImageSource.camera,
      maxWidth: 1080,
      maxHeight: 1080,
      imageQuality: 85,
    );
    if (pickedFile != null) {
      return File(pickedFile.path);
    }
    return null;
  }

  Future<String?> _uploadPhoto(File imageFile) async {
    const maxRetries = 2;
    for (int attempt = 0; attempt <= maxRetries; attempt++) {
      try {
        final uri = Uri.parse(ApiEndpoints.deliveryGigUploadProof());
        final request = http.MultipartRequest('POST', uri);
        final token = SessionRegistry.instance.token ?? '';
        if (token.isNotEmpty) {
          request.headers['Authorization'] = 'Bearer $token';
        }
        request.files.add(await http.MultipartFile.fromPath('photo', imageFile.path));
        final streamedResponse = await request.send().timeout(const Duration(seconds: 30));
        final response = await http.Response.fromStream(streamedResponse);
        if (response.statusCode == 200) {
          final data = jsonDecode(response.body) as Map<String, dynamic>;
          return data['photo_url'] as String?;
        }
        if (response.statusCode >= 500 && attempt < maxRetries) {
          await Future<void>.delayed(Duration(seconds: attempt + 1));
          continue;
        }
        debugPrint('Upload failed (${response.statusCode}): ${response.body}');
      } catch (e) {
        debugPrint('Upload error (attempt ${attempt + 1}): $e');
        if (attempt < maxRetries) {
          await Future<void>.delayed(Duration(seconds: attempt + 1));
        }
      }
    }
    return null;
  }

  Future<String?> _promptOTPCode() async {
    String? code;
    await showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      backgroundColor: Colors.white,
      shape: const RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(24)),
      ),
      builder: (context) {
        final controller = TextEditingController();
        return Padding(
          padding: EdgeInsets.only(
            bottom: MediaQuery.of(context).viewInsets.bottom,
            left: 24,
            right: 24,
            top: 24,
          ),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const Text(
                'Customer Security OTP',
                style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold, color: AppTheme.blackAccent),
                textAlign: TextAlign.center,
              ),
              const SizedBox(height: 8),
              const Text(
                'Ask the customer for the 4-digit verification code displayed on their screen.',
                style: TextStyle(color: Colors.grey, fontSize: 13),
                textAlign: TextAlign.center,
              ),
              const SizedBox(height: 20),
              TextField(
                controller: controller,
                keyboardType: TextInputType.number,
                maxLength: 4,
                textAlign: TextAlign.center,
                style: const TextStyle(fontSize: 24, fontWeight: FontWeight.bold, letterSpacing: 8),
                decoration: InputDecoration(
                  counterText: '',
                  hintText: '0000',
                  hintStyle: const TextStyle(color: Colors.grey),
                  filled: true,
                  fillColor: Colors.grey.shade50,
                  border: OutlineInputBorder(
                    borderRadius: BorderRadius.circular(16),
                  ),
                ),
              ),
              const SizedBox(height: 24),
              ElevatedButton(
                onPressed: () {
                  code = controller.text.trim();
                  Navigator.pop(context);
                },
                style: ElevatedButton.styleFrom(
                  backgroundColor: AppTheme.blackAccent,
                  padding: const EdgeInsets.symmetric(vertical: 16),
                  shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                ),
                child: const Text('Verify & Proceed', style: TextStyle(color: AppTheme.limeAccent, fontWeight: FontWeight.bold)),
              ),
              const SizedBox(height: 24),
            ],
          ),
        );
      },
    );
    return code;
  }

  Future<void> _queueOfflineTransition(String nextStatus, {String? otp, String? localPhotoPath}) async {
    final payload = {
      "status": nextStatus,
      "otp_code": otp,
      "local_photo_path": localPhotoPath,
    };
    await OfflineGigStorage.queuePendingStatusSync(payload);
    await OfflineGigStorage.updateGigStatusLocally(nextStatus);
    if (mounted) {
      if (nextStatus == 'completed' || nextStatus == 'failed') {
        setState(() {
          _activeGigStatus = 'No Active Gig';
          _broadcastedGig = null;
        });
      } else {
        setState(() {
          _activeGigStatus = nextStatus;
        });
      }
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Offline mode: Saved locally. Will sync when online.')),
      );
    }
  }

  Future<void> _handleNextStatusTransition() async {
    final nextStatus = _nextStatusValue();
    if (nextStatus.isEmpty) return;

    String? photoUrl;
    String? otpCode;

    // Step 2: Pickup Verification (Require Photo)
    if (nextStatus == 'picked_up') {
      final File? image = await _capturePhoto('Take Pickup Photo Proof');
      if (image == null) {
        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Pickup photo is mandatory to proceed.')),
        );
        return;
      }
      if (_isOnline) {
        photoUrl = await _uploadPhoto(image);
        if (photoUrl == null) {
          await _queueOfflineTransition(nextStatus, localPhotoPath: image.path);
          return;
        }
      } else {
        await _queueOfflineTransition(nextStatus, localPhotoPath: image.path);
        return;
      }
    }

    // Step 4: Secure Delivery (Require OTP and Photo)
    if (nextStatus == 'completed') {
      otpCode = await _promptOTPCode();
      if (otpCode == null || otpCode.length != 4) {
        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Valid 4-digit Customer OTP is required.')),
        );
        return;
      }

      final File? image = await _capturePhoto('Take Delivery Photo Proof');
      if (image == null) {
        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Delivery proof photo is mandatory.')),
        );
        return;
      }

      if (_isOnline) {
        photoUrl = await _uploadPhoto(image);
        if (photoUrl == null) {
          await _queueOfflineTransition(nextStatus, otp: otpCode, localPhotoPath: image.path);
          return;
        }
      } else {
        await _queueOfflineTransition(nextStatus, otp: otpCode, localPhotoPath: image.path);
        return;
      }
    }

    if (nextStatus.startsWith('ride_')) {
      await _sendRideStatusUpdate(nextStatus);
      return;
    }

    await _sendGigStatusUpdate(nextStatus, otp: otpCode, photoUrl: photoUrl);
  }

  Future<void> _sendRideStatusUpdate(String nextStatus) async {
    if (_broadcastedGig == null) return;
    final rideId = _broadcastedGig!['tracking_id'] as String?;
    if (rideId == null) return;

    try {
      if (nextStatus == 'ride_completed') {
        final backendDistanceMeters = (_broadcastedGig!['actual_distance_meters'] as num?)?.toDouble();
        final backendDurationSecs = (_broadcastedGig!['actual_duration_seconds'] as num?)?.toDouble();
        final distanceMeters = backendDistanceMeters ?? (_routeDistanceKm ?? 0) * 1000;
        final durationSecs = backendDurationSecs ?? (_routeEtaMinutes ?? 0) * 60;

        await _apiClient.post(ApiEndpoints.rideComplete(rideId), {
          "rider_tracking_id": widget.trackingId,
          "final_fare": ((_broadcastedGig!['fare_amount'] as num?) ?? 0).toDouble(),
          "distance_meters": distanceMeters,
          "duration_seconds": durationSecs,
          "payment_method": "cash",
        });
        if (mounted) {
          setState(() {
            _activeGigStatus = 'No Active Gig';
            _broadcastedGig = null;
          });
          ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Ride completed successfully!')));
        }
      } else {
        final stateForApi = nextStatus == 'ride_in_progress' ? 'in_progress' : nextStatus;
        await _apiClient.patch(ApiEndpoints.rideUpdateStatus(rideId), {
          "rider_tracking_id": widget.trackingId,
          "status": stateForApi,
        });
        if (mounted) {
          setState(() {
            _activeGigStatus = nextStatus;
          });
        }
      }
    } catch (e) {
      if (mounted) {
        final errStr = e.toString().toLowerCase();
        String message = 'Failed to update ride status.';
        if (errStr.contains('timeout') || errStr.contains('network')) {
          message = 'Network timeout. Check your connection and try again.';
        } else if (errStr.contains('409') || errStr.contains('conflict')) {
          message = 'Status already updated by another device.';
        } else if (errStr.contains('401') || errStr.contains('unauthorized')) {
          message = 'Session expired. Please login again.';
        }
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text(message), backgroundColor: Colors.red),
        );
      }
    }
  }

  Future<void> _sendGigStatusUpdate(String nextStatus, {String? otp, String? photoUrl}) async {
    final gigId = (_broadcastedGig?['tracking_id'] as String?) ?? (_broadcastedGig?['order_tracking_id'] as String?);
    if (gigId == null) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
            content: Text('Cannot update: missing gig id'),
            backgroundColor: Colors.red,
          ),
        );
      }
      return;
    }

    if (nextStatus == 'completed' || nextStatus == 'failed') {
      await OfflineGigStorage.clearActiveGig();
    } else {
      await OfflineGigStorage.updateGigStatusLocally(nextStatus);
    }

    if (mounted) {
      if (nextStatus == 'completed' || nextStatus == 'failed') {
        setState(() {
          _activeGigStatus = 'No Active Gig';
          _broadcastedGig = null;
        });
      } else {
        setState(() {
          _activeGigStatus = nextStatus;
        });
      }
    }

    try {
      final Map<String, dynamic> body = {
        "status": nextStatus,
      };
      if (otp != null) body["otp_code"] = otp;
      if (photoUrl != null) body["photo_url"] = photoUrl;

      await _apiClient.patch(ApiEndpoints.deliveryGigStatusUpdate(gigId), body);

      if (mounted && nextStatus == 'completed') {
        unawaited(
          showDialog<void>(
            context: context,
            builder: (context) => AlertDialog(
              backgroundColor: Colors.white,
              shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(24)),
              title: const Text('Gig Completed', style: TextStyle(fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
              content: const Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text('Success: Delivery completed.'),
                  SizedBox(height: 10),
                  Text('Chain complete: CUST-xxxx ➔ STOR-xxxx ➔ RIDR-xxxx', style: TextStyle(color: Colors.green, fontWeight: FontWeight.bold)),
                ],
              ),
              actions: [
                TextButton(
                  onPressed: () => Navigator.pop(context),
                  child: const Text('OK', style: TextStyle(color: AppTheme.blackAccent, fontWeight: FontWeight.bold)),
                ),
              ],
            ),
          ),
        );
      }
    } catch (e) {
      final errStr = e.toString().toLowerCase();
      final payload = {
        "status": nextStatus,
        "otp_code": otp,
        "photo_url": photoUrl,
      };
      await OfflineGigStorage.queuePendingStatusSync(payload);
      if (mounted) {
        String msg = 'Offline mode: Status saved locally and will sync when online.';
        if (errStr.contains('otp') || errStr.contains('invalid')) {
          msg = 'Invalid OTP. Please check the code and try again.';
        } else if (errStr.contains('413') || errStr.contains('payload')) {
          msg = 'Photo too large. Please try a smaller image.';
        }
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text(msg)),
        );
      }
    }
  }

  Future<void> _acceptRide() async {
    if (_broadcastedGig == null) return;
    final rideId = _broadcastedGig!['tracking_id'] as String?;
    if (rideId == null) return;

    setState(() {
      _activeGigStatus = 'Accepting...';
    });

    try {
      await _apiClient.post(ApiEndpoints.rideAccept(rideId), {
        "rider_tracking_id": widget.trackingId,
      });

      _broadcastedGig!['status'] = 'ride_accepted';
      
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('active_customer_id', (_broadcastedGig!['customer_tracking_id'] as String?) ?? '');
      await prefs.setString('active_order_id', rideId);

      if (mounted) {
        setState(() {
          _activeGigStatus = 'ride_accepted';
        });
        unawaited(_loadRoute());
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Ride Accepted! $rideId')),
        );
      }
    } on Exception catch (e) {
      if (mounted) {
        setState(() {
          _activeGigStatus = 'No Active Gig';
        });
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('Failed to accept ride: $e')));
      }
    }
  }

  Future<void> _cancelGig() async {
    if (_broadcastedGig == null) return;
    try {
      final gigId = _broadcastedGig!['tracking_id'] as String?;
      await _apiClient.post(ApiEndpoints.deliveryGigCancel(), {
        "tracking_id": gigId,
        "rider_tracking_id": widget.trackingId,
        "reason": "Rider requested cancellation",
      });
      await OfflineGigStorage.clearActiveGig();
      if (mounted) {
        setState(() {
          _activeGigStatus = 'No Active Gig';
          _broadcastedGig = null;
        });
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Gig cancelled. Re-assigning to others.')),
        );
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Cancel failed: $e')),
        );
      }
    }
  }

  String _statusDisplayName(String status) {
    switch (status) {
      case 'accepted':
        return 'Heading to Pick-Up';
      case 'picked_up':
        return 'Parcel Picked Up';
      case 'in_transit':
        return 'In Transit to Customer';
      case 'ride_accepted':
        return 'Heading to Passenger';
      case 'ride_in_progress':
        return 'Ride In Progress';
      case 'completed':
        return 'Delivered';
      case 'failed':
        return 'Delivery Failed';
      default:
        return 'Active Job';
    }
  }

  String? _nextStatusButtonLabel() {
    switch (_activeGigStatus) {
      case 'accepted':
        return 'Confirm Pick-Up';
      case 'picked_up':
        return 'Start Delivery';
      case 'in_transit':
        return 'Complete Delivery';
      case 'ride_accepted':
        return 'Start Ride';
      case 'ride_in_progress':
        return 'Complete Ride';
      default:
        return null;
    }
  }

  String _nextStatusValue() {
    switch (_activeGigStatus) {
      case 'accepted':
        return 'picked_up';
      case 'picked_up':
        return 'in_transit';
      case 'in_transit':
        return 'completed';
      case 'ride_accepted':
        return 'ride_in_progress';
      case 'ride_in_progress':
        return 'ride_completed';
      default:
        return '';
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: AppTheme.bgColor,
      body: SafeArea(
        child: Column(
          children: [
            if (!SessionRegistry.instance.isVerified && _selectedTabIndex == 0)
              Container(
                width: double.infinity,
                padding: const EdgeInsets.symmetric(vertical: 10, horizontal: 16),
                color: Colors.redAccent,
                child: Row(
                  children: [
                    const Icon(Icons.warning_amber_rounded, color: Colors.white, size: 20),
                    const SizedBox(width: 8),
                    const Expanded(
                      child: Text(
                        'Account Not Verified. Please go to Profile & KYC tab to submit required documents.',
                        style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontSize: 12),
                      ),
                    ),
                    GestureDetector(
                      onTap: () {
                        setState(() {
                          _selectedTabIndex = 3;
                        });
                      },
                      child: Container(
                        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
                        decoration: BoxDecoration(
                          color: Colors.white,
                          borderRadius: BorderRadius.circular(12),
                        ),
                        child: const Text('Verify Now', style: TextStyle(color: Colors.redAccent, fontWeight: FontWeight.bold, fontSize: 12)),
                      ),
                    ),
                  ],
                ),
              ),
            Expanded(
              child: IndexedStack(
                index: _selectedTabIndex,
                children: [
                  _buildMapAndGigsTab(),
                  _buildWalletTab(),
                  _buildCODLedgerTab(),
                  _buildProfileAndKycTab(),
                ],
              ),
            ),
          ],
        ),
      ),
      bottomNavigationBar: BottomNavigationBar(
        currentIndex: _selectedTabIndex,
        type: BottomNavigationBarType.fixed,
        backgroundColor: Colors.white,
        selectedItemColor: AppTheme.blackAccent,
        unselectedItemColor: Colors.grey,
        selectedLabelStyle: const TextStyle(fontWeight: FontWeight.bold, fontSize: 11),
        unselectedLabelStyle: const TextStyle(fontSize: 11),
        elevation: 12,
        onTap: (index) {
          setState(() {
            _selectedTabIndex = index;
          });
          if (index == 1) _fetchWalletSummary();
          if (index == 2) _fetchCodDebts();
        },
        items: [
          const BottomNavigationBarItem(
            icon: Icon(Icons.map_outlined),
            activeIcon: Icon(Icons.map, color: AppTheme.blackAccent),
            label: 'Map & Gigs',
          ),
          const BottomNavigationBarItem(
            icon: Icon(Icons.account_balance_wallet_outlined),
            activeIcon: Icon(Icons.account_balance_wallet, color: AppTheme.blackAccent),
            label: 'Wallet',
          ),
          const BottomNavigationBarItem(
            icon: Icon(Icons.receipt_long_outlined),
            activeIcon: Icon(Icons.receipt_long, color: AppTheme.blackAccent),
            label: 'COD Ledger',
          ),
          BottomNavigationBarItem(
            icon: Icon(
              SessionRegistry.instance.isVerified ? Icons.person_outline : Icons.error_outline,
              color: SessionRegistry.instance.isVerified ? null : Colors.orange,
            ),
            activeIcon: Icon(
              SessionRegistry.instance.isVerified ? Icons.person : Icons.error,
              color: SessionRegistry.instance.isVerified ? AppTheme.blackAccent : Colors.orange,
            ),
            label: 'Profile & KYC',
          ),
        ],
      ),
    );
  }

  Widget _buildMapAndGigsTab() {
    return Stack(
      children: [
        // MapLibre map (proxied through Go map-service). We render surge
        // heatmap hexagons as their centroid markers with size scaled to the
        // surge multiplier. Native fill polygons can be added later by
        // extending MapLibreMapWidget.
        MapLibreMapWidget(
          initialCenter: _center ?? const LatLng(0, 0),
          initialZoom: 14.0,
          markers: {
            // Surge hex centroids as oversized translucent dots.
            for (var i = 0; i < _surgeHeatmaps.length; i++)
              'surge_$i': MarkerData(
                position: _hexCentroid(_surgeHeatmaps[i]),
                iconSize: _hexIconSize(_surgeHeatmaps[i]),
              ),
            // Rider position
            if (_center != null) 'rider_me': MarkerData(
              position: _center!,
              iconSize: 1.0,
            ),
            // Vendor pickup and customer dropoff markers when a gig is active.
            if (_broadcastedGig != null && _gigPickupPoint() != null)
              'pickup': MarkerData(
                position: _gigPickupPoint()!,
                iconSize: 1.0,
              ),
            if (_broadcastedGig != null && _gigDropoffPoint() != null)
              'dropoff': MarkerData(
                position: _gigDropoffPoint()!,
                iconSize: 1.0,
              ),
          },
          polylines: _routePolyline.isNotEmpty ? [_routePolyline] : const [],
          onMapCreated: (controller) {
            _mapController = controller;
          },
        ),

        // Floating Re-Center Location Button + Chat Button
        Positioned(
          right: 20,
          top: 0,
          child: SafeArea(
            child: Padding(
              padding: const EdgeInsets.only(top: 100),
              child: Column(
                children: [
                  // Chat button — opens the chat list screen.
                  Container(
                    margin: const EdgeInsets.only(bottom: 12),
                    decoration: const BoxDecoration(
                      color: Colors.white,
                      shape: BoxShape.circle,
                      boxShadow: [
                        BoxShadow(color: Colors.black26, blurRadius: 4, offset: Offset(0, 2)),
                      ],
                    ),
                    child: IconButton(
                      icon: const Icon(Icons.chat_bubble_rounded, color: AppTheme.blackAccent),
                      onPressed: () {
                        Navigator.of(context).push(
                          MaterialPageRoute<void>(builder: (_) => const ChatListScreen()),
                        );
                      },
                    ),
                  ),
                  FloatingActionButton.small(
                    heroTag: 'recenter_gps',
                    backgroundColor: Colors.white,
                    elevation: 4,
                    onPressed: () async {
              if (mounted) ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Fetching hardware GPS...')));
              try {
                final bool serviceEnabled = await Geolocator.isLocationServiceEnabled();
                if (!serviceEnabled) {
                  if (mounted) ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Please enable GPS in your device settings.')));
                  return;
                }
                LocationPermission permission = await Geolocator.checkPermission();
                if (permission == LocationPermission.denied) {
                  permission = await Geolocator.requestPermission();
                  if (permission == LocationPermission.denied) {
                    if (mounted) ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Location permission denied.')));
                    return;
                  }
                }
                if (permission == LocationPermission.deniedForever) {
                  if (mounted) ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Location permissions are permanently denied.')));
                  return;
                }

                final pos = await Geolocator.getCurrentPosition(
                  locationSettings: (!kIsWeb && Platform.isAndroid)
                      ? AndroidSettings(accuracy: LocationAccuracy.high)
                      : const LocationSettings(accuracy: LocationAccuracy.high),
                );
                if (mounted) {
                  setState(() {
                    _center = LatLng(pos.latitude, pos.longitude);
                  });
                  _mapController?.animateCamera(
                    CameraUpdate.newLatLngZoom(_center!, 15.0),
                  );
                }
              } catch (e) {
                if (mounted) ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Failed to get location. Check settings.')));
              }
            },
            child: const Icon(Icons.my_location, color: AppTheme.blackAccent),
          ),
                ],
              ),
            ),
          ),
        ),

        // Top Status Panel (Dribbble clean styling)
        Positioned(
          top: 0,
          left: 0,
          right: 0,
          child: SafeArea(
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
              child: Container(
                padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
                decoration: BoxDecoration(
                  color: Colors.white,
                  borderRadius: BorderRadius.circular(30),
                  boxShadow: const [BoxShadow(color: Colors.black12, blurRadius: 10)],
                ),
                child: SingleChildScrollView(
                  scrollDirection: Axis.horizontal,
                  child: Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    children: [
                GestureDetector(
                  onTap: _showRiderProfile,
                  behavior: HitTestBehavior.opaque,
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Row(
                        children: [
                          Text(widget.trackingId, style: const TextStyle(fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
                          const SizedBox(width: 4),
                          const Icon(Icons.info_outline_rounded, size: 14, color: AppTheme.blackAccent),
                        ],
                      ),
                      Text(_isOnline ? 'Status: Online' : 'Status: Offline', style: TextStyle(color: _isOnline ? Colors.green : Colors.grey, fontSize: 12, fontWeight: FontWeight.bold)),
                    ],
                  ),
                ),
                Row(
                  children: [
                    Text(_isOnline ? 'Go Offline' : 'Go Online', style: const TextStyle(fontSize: 13, fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
                    const SizedBox(width: 8),
                    Switch(
                      value: _isOnline,
                      activeColor: AppTheme.limeAccent,
                      activeTrackColor: AppTheme.blackAccent,
                      onChanged: (val) async {
                        if (val) {
                          await _telemetryService.goOnline();
                          if (mounted) {
                            ScaffoldMessenger.of(context).showSnackBar(
                              const SnackBar(content: Text('You are now ONLINE. Searching for nearby delivery gigs...'), backgroundColor: Colors.green),
                            );
                          }
                        } else {
                          await _telemetryService.goOffline();
                          if (mounted) {
                            ScaffoldMessenger.of(context).showSnackBar(
                              const SnackBar(content: Text('You are now OFFLINE.')),
                            );
                          }
                        }
                        if (mounted) {
                          setState(() {
                            _isOnline = val;
                          });
                        }
                      },
                    ),
                  ],
                ),
                Row(
                  children: [
                    Text(_isPickAndDropActive ? 'Rides: ON' : 'Rides: OFF', style: const TextStyle(fontSize: 13, fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
                    const SizedBox(width: 8),
                    Switch(
                      value: _isPickAndDropActive,
                      activeColor: AppTheme.softPink,
                      activeTrackColor: AppTheme.blackAccent,
                      onChanged: (val) {
                        setState(() {
                          _isPickAndDropActive = val;
                        });
                        ScaffoldMessenger.of(context).showSnackBar(
                          SnackBar(content: Text(val ? 'Ride-Hailing Enabled' : 'Ride-Hailing Disabled')),
                        );
                      },
                    ),
                  ],
                ),
              ],
            ),
          ),
        ),
        ),
      ),
    ),

        // Bottom Action Card (Dribbble styled popup panel)
        Positioned(
          bottom: 0,
          left: 0,
          right: 0,
          child: SafeArea(
            child: Padding(
              padding: const EdgeInsets.all(12),
              child: Container(
                padding: const EdgeInsets.all(24),
            decoration: BoxDecoration(
              color: Colors.white,
              borderRadius: BorderRadius.circular(40),
              boxShadow: const [BoxShadow(color: Colors.black26, blurRadius: 20)],
            ),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                if (_activeGigStatus == 'No Active Gig') ...[
                  if (_broadcastedGig != null) ...[
                    const Text(
                      'Broadcast Gigs',
                      style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold, color: AppTheme.blackAccent),
                    ),
                    const SizedBox(height: 8),
                    Text(
                      'Store: ${_broadcastedGig?['vendor_store_tracking_id'] ?? 'N/A'}\nOrder: ${_broadcastedGig?['order_tracking_id'] ?? 'N/A'}\nEarning: Rs. ${_broadcastedGig?['rider_earning'] ?? 'N/A'}'
                      '${_broadcastedGig?['delivery_fee'] != null ? '\nDelivery Fee: Rs. ${_broadcastedGig?['delivery_fee']}' : ''}',
                      style: const TextStyle(color: Colors.grey, fontSize: 14),
                    ),
                    if (_routeEtaMinutes != null) ...[
                      const SizedBox(height: 8),
                      Text(
                        'Route: ${_routeDistanceKm?.toStringAsFixed(1)} km • ${_routeEtaMinutes?.toStringAsFixed(0)} min',
                        style: const TextStyle(color: AppTheme.blackAccent, fontSize: 13, fontWeight: FontWeight.bold),
                      ),
                    ],
                    const SizedBox(height: 20),
                    ElevatedButton(
                      onPressed: _acceptGig,
                      style: ElevatedButton.styleFrom(backgroundColor: AppTheme.blackAccent),
                      child: const Text('Accept Delivery Gig', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
                    ),
                  ] else if (_isPickAndDropActive) ...[
                    const SizedBox(height: 10),
                    Container(
                      padding: const EdgeInsets.all(12),
                      decoration: BoxDecoration(
                        color: AppTheme.softPink,
                        borderRadius: BorderRadius.circular(20),
                      ),
                      child: const Row(
                        children: [
                          Icon(Icons.directions_car, color: AppTheme.blackAccent),
                          SizedBox(width: 10),
                          Expanded(
                            child: Text(
                              'Ride-Hailing Active: Available for Pick & Drop requests.',
                              style: TextStyle(color: AppTheme.blackAccent, fontSize: 12, fontWeight: FontWeight.bold),
                            ),
                          ),
                        ],
                      ),
                    ),
                  ] else ...[
                    const Center(
                      child: Text(
                        'No active broadcast. Waiting...',
                        style: TextStyle(color: Colors.grey, fontSize: 14),
                      ),
                    ),
                  ],
                ] else ...[
                  Text(
                    'Active Job Status: ${_statusDisplayName(_activeGigStatus)}',
                    style: const TextStyle(fontSize: 18, fontWeight: FontWeight.bold, color: AppTheme.blackAccent),
                  ),
                  const SizedBox(height: 8),
                  if (_activeGigStatus.startsWith('ride_')) ...[
                    Text(
                      'Vehicle: ${_broadcastedGig?['vehicle_type'] ?? 'Standard'}\nFare: PKR ${_broadcastedGig?['fare_amount'] ?? 'N/A'}',
                      style: const TextStyle(color: Colors.grey, fontSize: 13),
                    ),
                  ] else ...[
                    Text(
                      'Store: ${_broadcastedGig?['vendor_store_tracking_id'] ?? 'N/A'}\nOrder: ${_broadcastedGig?['order_tracking_id'] ?? 'N/A'}',
                      style: const TextStyle(color: Colors.grey, fontSize: 13),
                    ),
                  ],
                  const SizedBox(height: 20),
                  if (_broadcastedGig != null && _broadcastedGig!['customer_phone'] != null && _broadcastedGig!['customer_phone'].toString().isNotEmpty)
                    OutlinedButton.icon(
                      onPressed: () async {
                        final messenger = ScaffoldMessenger.of(context);
                        final Uri launchUri = Uri(
                          scheme: 'tel',
                          path: (_broadcastedGig!['customer_phone']?.toString()) ?? '',
                        );
                        if (await canLaunchUrl(launchUri)) {
                          await launchUrl(launchUri);
                        } else {
                          if (mounted) messenger.showSnackBar(const SnackBar(content: Text('Could not launch dialer.')));
                        }
                      },
                      icon: const Icon(Icons.phone, color: Colors.green),
                      label: const Text('Call Customer', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.green)),
                      style: OutlinedButton.styleFrom(
                        side: const BorderSide(color: Colors.green),
                        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                      ),
                    ),
                  if (_nextStatusButtonLabel() != null)
                    ElevatedButton(
                      onPressed: () => _handleNextStatusTransition(),
                      style: ElevatedButton.styleFrom(backgroundColor: Colors.green),
                      child: Text(_nextStatusButtonLabel()!, style: const TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
                    ),
                  if (_activeGigStatus == 'accepted' || _activeGigStatus == 'picked_up' || _activeGigStatus == 'ride_accepted')
                    ElevatedButton.icon(
                      onPressed: _startNavigation,
                      icon: const Icon(Icons.navigation, color: Colors.white),
                      label: const Text('Start Turn-by-Turn Navigation', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
                      style: ElevatedButton.styleFrom(backgroundColor: AppTheme.blackAccent),
                    ),
                  const SizedBox(height: 10),
                  OutlinedButton(
                    onPressed: _cancelGig,
                    style: OutlinedButton.styleFrom(foregroundColor: Colors.red),
                    child: const Text('Cancel Order', style: TextStyle(fontWeight: FontWeight.bold)),
                  ),
                ],
              ],
            ),
          ),
        ),
      ),
    ),
      ],
    );
  }

  Future<void> _pickDocument(String type) async {
    final picked = await ImagePicker().pickImage(source: ImageSource.gallery, maxWidth: 1200, imageQuality: 85);
    if (picked == null) return;
    setState(() {
      if (type == 'cnic') {
        _cnicFile = File(picked.path);
      } else if (type == 'license') {
        _licenseFile = File(picked.path);
      } else if (type == 'vehicle_registration') {
        _vehicleRegFile = File(picked.path);
      }
    });
  }

  Future<void> _uploadVerificationDocs() async {
    if (_cnicFile == null || _licenseFile == null || _vehicleRegFile == null) {
      ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Please upload all 3 documents to complete verification.')));
      return;
    }

    setState(() => _isUploadingDocs = true);
    try {
      final request = http.MultipartRequest(
        'PUT',
        Uri.parse(ApiEndpoints.authKycUpload()),
      );
      final token = SessionRegistry.instance.token ?? widget.trackingId;
      request.headers['Authorization'] = 'Bearer $token';

      request.files.add(await http.MultipartFile.fromPath('cnic', _cnicFile!.path));
      request.files.add(await http.MultipartFile.fromPath('license', _licenseFile!.path));
      request.files.add(await http.MultipartFile.fromPath('vehicle_registration', _vehicleRegFile!.path));

      final response = await request.send().timeout(const Duration(seconds: 30));
      final body = await response.stream.bytesToString();
      debugPrint('KYC upload response: $body');
      
      if (response.statusCode == 200) {
        final data = jsonDecode(body);
        if (data['is_verified'] == true) {
          await SessionRegistry.instance.saveSession(
            token: SessionRegistry.instance.token!,
            role: SessionRegistry.instance.role!,
            trackingId: SessionRegistry.instance.trackingId!,
            isVerified: true,
          );
          if (mounted) {
            ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Verification successful! You can now go online.', style: TextStyle(color: Colors.white)), backgroundColor: Colors.green));
            setState(() {});
          }
        }
      } else {
        if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('Upload failed: ${response.statusCode}')));
      }
    } catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('Network Error: $e')));
    } finally {
      if (mounted) setState(() => _isUploadingDocs = false);
    }
  }

  Widget _buildVerificationWorkspace() {
    return Container(
      padding: const EdgeInsets.all(24),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(40),
        boxShadow: const [BoxShadow(color: Colors.black26, blurRadius: 20)],
      ),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const Text(
            'Verification Workspace',
            style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: AppTheme.blackAccent),
          ),
          const SizedBox(height: 8),
          const Text(
            'Upload the mandatory documents below to instantly verify your account and start earning.',
            style: TextStyle(color: Colors.grey, fontSize: 13),
          ),
          const SizedBox(height: 20),
          _buildUploadButton('CNIC (Front/Back)', 'cnic', _cnicFile),
          const SizedBox(height: 12),
          _buildUploadButton('Driving License', 'license', _licenseFile),
          const SizedBox(height: 12),
          _buildUploadButton('Vehicle Registration (Book/Slip)', 'vehicle_registration', _vehicleRegFile),
          const SizedBox(height: 20),
          ElevatedButton(
            onPressed: _isUploadingDocs ? null : _uploadVerificationDocs,
            style: ElevatedButton.styleFrom(
              backgroundColor: AppTheme.limeAccent,
              padding: const EdgeInsets.symmetric(vertical: 16),
              shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
            ),
            child: _isUploadingDocs
                ? const SizedBox(width: 20, height: 20, child: CircularProgressIndicator(color: AppTheme.blackAccent, strokeWidth: 2))
                : const Text('Submit & Auto-Verify', style: TextStyle(fontWeight: FontWeight.bold, color: AppTheme.blackAccent, fontSize: 16)),
          ),
        ],
      ),
    );
  }

  Widget _buildUploadButton(String title, String type, File? selectedFile) {
    return InkWell(
      onTap: () => _pickDocument(type),
      borderRadius: BorderRadius.circular(12),
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
        decoration: BoxDecoration(
          color: selectedFile != null ? AppTheme.limeAccent.withOpacity(0.1) : Colors.grey.shade100,
          borderRadius: BorderRadius.circular(12),
          border: Border.all(color: selectedFile != null ? AppTheme.limeAccent : Colors.grey.shade300),
        ),
        child: Row(
          children: [
            Icon(selectedFile != null ? Icons.check_circle : Icons.upload_file, color: selectedFile != null ? Colors.green : Colors.grey),
            const SizedBox(width: 12),
            Expanded(
              child: Text(
                title,
                style: TextStyle(
                  color: selectedFile != null ? Colors.green.shade700 : AppTheme.blackAccent,
                  fontWeight: selectedFile != null ? FontWeight.bold : FontWeight.normal,
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  void _showRiderProfile() {
    final session = SessionRegistry.instance;
    showModalBottomSheet<void>(
      context: context,
      shape: const RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(30)),
      ),
      backgroundColor: const Color(0xFF121217),
      builder: (context) {
        return Container(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Center(
                child: Container(
                  width: 50,
                  height: 5,
                  decoration: BoxDecoration(
                    color: Colors.white24,
                    borderRadius: BorderRadius.circular(10),
                  ),
                ),
              ),
              const SizedBox(height: 20),
              Row(
                children: [
                  CircleAvatar(
                    radius: 30,
                    backgroundColor: AppTheme.limeAccent.withOpacity(0.1),
                    child: const Icon(Icons.delivery_dining_rounded, color: AppTheme.limeAccent, size: 30),
                  ),
                  const SizedBox(width: 16),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          session.fullName ?? 'Rider Partner',
                          style: const TextStyle(color: Colors.white, fontSize: 18, fontWeight: FontWeight.bold),
                        ),
                        const SizedBox(height: 4),
                        Row(
                          children: [
                            Container(
                              padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
                              decoration: BoxDecoration(
                                color: AppTheme.limeAccent.withOpacity(0.1),
                                borderRadius: BorderRadius.circular(6),
                              ),
                              child: Text(
                                widget.trackingId,
                                style: const TextStyle(color: AppTheme.limeAccent, fontSize: 11, fontWeight: FontWeight.bold),
                              ),
                            ),
                            const SizedBox(width: 8),
                            Icon(
                              session.isVerified ? Icons.verified_rounded : Icons.pending_rounded,
                              color: session.isVerified ? Colors.greenAccent : Colors.orangeAccent,
                              size: 16,
                            ),
                          ],
                        ),
                      ],
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 24),
              const Divider(color: Colors.white10),
              const SizedBox(height: 16),
              _buildProfileItem(Icons.email_outlined, 'Email Address', session.email ?? 'N/A'),
              const SizedBox(height: 12),
              _buildProfileItem(Icons.phone_outlined, 'Contact Number', session.phone ?? 'N/A'),
              const SizedBox(height: 12),
              _buildProfileItem(Icons.map_outlined, 'Base Address', session.address ?? 'N/A'),
              const SizedBox(height: 12),
              _buildProfileItem(Icons.verified_user_outlined, 'Account Status', session.isVerified ? 'VERIFIED' : 'PENDING APPROVAL'),
              const SizedBox(height: 24),
              SizedBox(
                width: double.infinity,
                height: 50,
                child: ElevatedButton.icon(
                  onPressed: () async {
                    if (_isOnline) {
                      await _telemetryService.goOffline();
                    }
                    await SessionRegistry.instance.logout();
                    if (context.mounted) {
                      unawaited(Navigator.pushNamedAndRemoveUntil(context, '/', (route) => false));
                    }
                  },
                  icon: const Icon(Icons.logout_rounded, color: Colors.white),
                  label: const Text('Log Out', style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontSize: 16)),
                  style: ElevatedButton.styleFrom(
                    backgroundColor: Colors.redAccent,
                    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                  ),
                ),
              ),
              const SizedBox(height: 16),
            ],
          ),
        );
      },
    );
  }

  Widget _buildProfileItem(IconData icon, String label, String value) {
    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Icon(icon, color: Colors.grey, size: 20),
        const SizedBox(width: 12),
        Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(label, style: const TextStyle(color: Colors.grey, fontSize: 11)),
            const SizedBox(height: 2),
            Text(value, style: const TextStyle(color: Colors.white, fontSize: 13, fontWeight: FontWeight.w600)),
          ],
        ),
      ],
    );
  }

  Widget _buildProfileAndKycTab() {
    final session = SessionRegistry.instance;
    return SingleChildScrollView(
      padding: const EdgeInsets.all(20),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          // Rider Header Card
          Container(
            padding: const EdgeInsets.all(20),
            decoration: BoxDecoration(
              color: const Color(0xFF121217),
              borderRadius: BorderRadius.circular(24),
              boxShadow: const [BoxShadow(color: Colors.black12, blurRadius: 10)],
            ),
            child: Column(
              children: [
                CircleAvatar(
                  radius: 36,
                  backgroundColor: AppTheme.limeAccent.withOpacity(0.2),
                  child: const Icon(Icons.two_wheeler_rounded, color: AppTheme.limeAccent, size: 40),
                ),
                const SizedBox(height: 12),
                Text(
                  session.fullName ?? 'Rider Partner',
                  style: const TextStyle(color: Colors.white, fontSize: 20, fontWeight: FontWeight.bold),
                ),
                const SizedBox(height: 8),
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 6),
                  decoration: BoxDecoration(
                    color: AppTheme.limeAccent.withOpacity(0.15),
                    borderRadius: BorderRadius.circular(10),
                    border: Border.all(color: AppTheme.limeAccent.withOpacity(0.3)),
                  ),
                  child: Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      const Icon(Icons.badge_outlined, color: AppTheme.limeAccent, size: 16),
                      const SizedBox(width: 6),
                      Text(
                        widget.trackingId,
                        style: const TextStyle(color: AppTheme.limeAccent, fontSize: 14, fontWeight: FontWeight.bold, letterSpacing: 1),
                      ),
                    ],
                  ),
                ),
              ],
            ),
          ),
          const SizedBox(height: 16),

          // Verification Status Badge Card
          Container(
            padding: const EdgeInsets.all(16),
            decoration: BoxDecoration(
              color: session.isVerified ? Colors.green.shade50 : Colors.amber.shade50,
              borderRadius: BorderRadius.circular(20),
              border: Border.all(color: session.isVerified ? Colors.green.shade300 : Colors.amber.shade300),
            ),
            child: Row(
              children: [
                Icon(
                  session.isVerified ? Icons.verified_rounded : Icons.pending_actions_rounded,
                  color: session.isVerified ? Colors.green.shade700 : Colors.amber.shade800,
                  size: 28,
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        session.isVerified ? 'Account Verified & Active' : 'Verification Required',
                        style: TextStyle(
                          color: session.isVerified ? Colors.green.shade900 : Colors.amber.shade900,
                          fontWeight: FontWeight.bold,
                          fontSize: 15,
                        ),
                      ),
                      const SizedBox(height: 2),
                      Text(
                        session.isVerified
                            ? 'You are authorized to accept delivery gigs and ride requests.'
                            : 'Please upload your mandatory documents below to verify your account.',
                        style: TextStyle(
                          color: session.isVerified ? Colors.green.shade800 : Colors.amber.shade900,
                          fontSize: 12,
                        ),
                      ),
                    ],
                  ),
                ),
              ],
            ),
          ),
          const SizedBox(height: 16),

          // Document Uploader Workspace
          _buildVerificationWorkspace(),
          const SizedBox(height: 16),

          // Live Tracking Shortcut
          InkWell(
            onTap: () {
                setState(() {
                  _selectedTabIndex = 2; // Navigates to 'Active' Tab
                });
              },
              borderRadius: BorderRadius.circular(20),
              child: Container(
                padding: const EdgeInsets.all(20),
                decoration: BoxDecoration(
                  gradient: LinearGradient(
                    colors: [AppTheme.limeAccent, Colors.green.shade400],
                    begin: Alignment.topLeft,
                    end: Alignment.bottomRight,
                  ),
                  borderRadius: BorderRadius.circular(20),
                  boxShadow: const [BoxShadow(color: Colors.black12, blurRadius: 6)],
                ),
                child: Row(
                  children: [
                    Container(
                      padding: const EdgeInsets.all(12),
                      decoration: BoxDecoration(
                        color: Colors.white.withOpacity(0.3),
                        shape: BoxShape.circle,
                      ),
                      child: const Icon(Icons.my_location_rounded, color: AppTheme.blackAccent, size: 28),
                    ),
                    const SizedBox(width: 16),
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          const Text(
                            'Live Tracking & Active Gigs',
                            style: TextStyle(color: AppTheme.blackAccent, fontSize: 16, fontWeight: FontWeight.bold),
                          ),
                          const SizedBox(height: 4),
                          Text(
                            'View real-time progress of your ongoing delivery or ride.',
                            style: TextStyle(color: AppTheme.blackAccent.withOpacity(0.8), fontSize: 13),
                          ),
                        ],
                      ),
                    ),
                    const Icon(Icons.arrow_forward_ios_rounded, color: AppTheme.blackAccent),
                  ],
                ),
              ),
            ),
          const SizedBox(height: 16),
          // Rider Profile Info Card
          Container(
            padding: const EdgeInsets.all(20),
            decoration: BoxDecoration(
              color: Colors.white,
              borderRadius: BorderRadius.circular(24),
              border: Border.all(color: Colors.grey.shade200),
              boxShadow: const [BoxShadow(color: Colors.black12, blurRadius: 6)],
            ),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                const Text(
                  'Partner Profile Information',
                  style: TextStyle(fontSize: 16, fontWeight: FontWeight.bold, color: AppTheme.blackAccent),
                ),
                const SizedBox(height: 16),
                _buildProfileDetailRow(Icons.email_outlined, 'Email', session.email ?? 'N/A'),
                const Divider(height: 20),
                _buildProfileDetailRow(Icons.phone_outlined, 'Phone', session.phone ?? 'N/A'),
                const Divider(height: 20),
                _buildProfileDetailRow(Icons.location_on_outlined, 'Address', session.address ?? 'N/A'),
                const Divider(height: 20),
                _buildProfileDetailRow(Icons.verified_user_outlined, 'Status', session.isVerified ? 'VERIFIED' : 'PENDING APPROVAL'),
                const Divider(height: 20),
                SizedBox(
                  width: double.infinity,
                  height: 50,
                  child: ElevatedButton.icon(
                    onPressed: () async {
                      final navigator = Navigator.of(context);
                      if (_isOnline) {
                        await _telemetryService.goOffline();
                      }
                      await SessionRegistry.instance.logout();
                      if (mounted) {
                        unawaited(navigator.pushNamedAndRemoveUntil('/', (route) => false));
                      }
                    },
                    icon: const Icon(Icons.logout_rounded, color: Colors.white),
                    label: const Text('Log Out', style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontSize: 16)),
                    style: ElevatedButton.styleFrom(
                      backgroundColor: Colors.redAccent,
                      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                    ),
                  ),
                ),
              ],
            ),
          ),
          const SizedBox(height: 16),

          // Rider Analytics / Metrics Card
          Container(
            padding: const EdgeInsets.all(20),
            decoration: BoxDecoration(
              color: AppTheme.blackAccent,
              borderRadius: BorderRadius.circular(24),
              boxShadow: const [BoxShadow(color: Colors.black12, blurRadius: 6)],
            ),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                const Text(
                  'Analytics & Earnings',
                  style: TextStyle(fontSize: 16, fontWeight: FontWeight.bold, color: Colors.white),
                ),
                const SizedBox(height: 16),
                Row(
                  children: [
                    Expanded(
                      child: Container(
                        padding: const EdgeInsets.all(16),
                        decoration: BoxDecoration(color: AppTheme.softGreen, borderRadius: BorderRadius.circular(16)),
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            const Text('Lifetime Earned', style: TextStyle(color: AppTheme.blackAccent, fontSize: 12)),
                            const SizedBox(height: 6),
                            Text(
                              'PKR ${((_walletSummary?['lifetime_earnings'] as num?) ?? 0).toStringAsFixed(2)}',
                              style: const TextStyle(color: AppTheme.blackAccent, fontSize: 18, fontWeight: FontWeight.bold),
                            ),
                          ],
                        ),
                      ),
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      child: Container(
                        padding: const EdgeInsets.all(16),
                        decoration: BoxDecoration(color: AppTheme.softBlue, borderRadius: BorderRadius.circular(16)),
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            const Text('Available Balance', style: TextStyle(color: AppTheme.blackAccent, fontSize: 12)),
                            const SizedBox(height: 6),
                            Text(
                              // FIX M5: Use 'balance' key (matches API response)
                              'PKR ${((_walletSummary?['balance'] as num?) ?? 0).toStringAsFixed(2)}',
                              style: const TextStyle(color: AppTheme.blackAccent, fontSize: 18, fontWeight: FontWeight.bold),
                            ),
                          ],
                        ),
                      ),
                    ),
                  ],
                ),
                const SizedBox(height: 16),
                Container(
                  padding: const EdgeInsets.all(16),
                  decoration: BoxDecoration(
                    color: Colors.red.shade50,
                    borderRadius: BorderRadius.circular(16),
                    border: Border.all(color: Colors.red.shade200),
                  ),
                  child: Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    children: [
                      const Text('Pending COD Debts', style: TextStyle(color: Colors.red, fontWeight: FontWeight.bold)),
                      Text(
                        'PKR ${_codDebts.fold<double>(0.0, (sum, item) => sum + ((item['amount_owed'] as num?) ?? 0.0)).toStringAsFixed(2)}',
                        style: const TextStyle(color: Colors.red, fontWeight: FontWeight.bold, fontSize: 16),
                      ),
                    ],
                  ),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }

  Widget _buildProfileDetailRow(IconData icon, String label, String value) {
    return Row(
      children: [
        Icon(icon, color: Colors.grey.shade600, size: 20),
        const SizedBox(width: 12),
        Text(label, style: TextStyle(color: Colors.grey.shade600, fontSize: 13)),
        const Spacer(),
        Text(value, style: const TextStyle(fontWeight: FontWeight.bold, color: AppTheme.blackAccent, fontSize: 13)),
      ],
    );
  }

  Future<void> _fetchCodDebts() async {
    setState(() => _isLoadingCodDebts = true);
    try {
      final token = SessionRegistry.instance.token ?? '';
      final res = await http.get(
        Uri.parse(ApiEndpoints.codDebts(widget.trackingId)),
        headers: {'Authorization': 'Bearer $token'},
      ).timeout(const Duration(seconds: 8));

      if (res.statusCode == 200 && mounted) {
        final data = jsonDecode(res.body) as Map<String, dynamic>;
        setState(() {
          _codDebts = (data['debts'] as List<dynamic>?) ?? [];
          _isLoadingCodDebts = false;
        });
      } else {
        if (mounted) setState(() => _isLoadingCodDebts = false);
      }
    } catch (e) {
      debugPrint('Failed to fetch COD debts: $e');
      if (mounted) setState(() => _isLoadingCodDebts = false);
    }
  }

  Future<void> _fetchWalletSummary() async {
    try {
      final token = SessionRegistry.instance.token ?? '';
      final res = await http.get(
        Uri.parse(ApiEndpoints.riderWallet(widget.trackingId)),
        headers: {'Authorization': 'Bearer $token'},
      ).timeout(const Duration(seconds: 8));

      if (res.statusCode == 200 && mounted) {
        final data = jsonDecode(res.body) as Map<String, dynamic>;
        setState(() {
          _walletSummary = data;
        });
      }
    } catch (e) {
      debugPrint('Failed to fetch wallet summary: $e');
    }
  }

  void _showVerificationPromptDialog() {
    showDialog<void>(
      context: context,
      builder: (ctx) => AlertDialog(
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(20)),
        title: const Row(
          children: [
            Icon(Icons.warning_amber_rounded, color: Colors.orange, size: 28),
            SizedBox(width: 10),
            Text('Verification Required', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 16)),
          ],
        ),
        content: const Text(
          'You cannot go online until your mandatory documents (CNIC, License, Vehicle Registration) are verified by Admin.\n\nPlease upload them in the Profile & KYC section.',
          style: TextStyle(fontSize: 14),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Cancel', style: TextStyle(color: Colors.grey)),
          ),
          ElevatedButton(
            onPressed: () {
              Navigator.pop(ctx);
              setState(() {
                _selectedTabIndex = 3;
              });
            },
            style: ElevatedButton.styleFrom(backgroundColor: AppTheme.blackAccent),
            child: const Text('Go to Profile & KYC', style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold)),
          ),
        ],
      ),
    );
  }

  void _showPayDebtDialog(String codDebtId, double amount) {
    String selectedGateway = 'jazzcash';
    bool isSubmitting = false;

    showDialog<void>(
      context: context,
      barrierDismissible: false,
      builder: (ctx) => StatefulBuilder(
        builder: (context, setDialogState) => AlertDialog(
          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(24)),
          title: const Row(
            children: [
              Icon(Icons.account_balance_wallet_outlined, color: AppTheme.blackAccent),
              SizedBox(width: 10),
              Text('COD Debt Settlement', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 18)),
            ],
          ),
          content: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Container(
                  padding: const EdgeInsets.all(16),
                  decoration: BoxDecoration(
                    color: Colors.red.shade50,
                    borderRadius: BorderRadius.circular(16),
                    border: Border.all(color: Colors.red.shade200),
                  ),
                  child: Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    children: [
                      const Text('Total Payable:', style: TextStyle(fontWeight: FontWeight.w600, color: Colors.black87)),
                      Text('PKR ${amount.toStringAsFixed(2)}', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 18, color: Colors.red.shade800)),
                    ],
                  ),
                ),
                const SizedBox(height: 20),
                const Text('Select Payment Gateway:', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 13, color: Colors.black87)),
                const SizedBox(height: 10),
                Row(
                  children: [
                    Expanded(
                      child: InkWell(
                        onTap: () => setDialogState(() => selectedGateway = 'jazzcash'),
                        child: Container(
                          padding: const EdgeInsets.symmetric(vertical: 12),
                          decoration: BoxDecoration(
                            color: selectedGateway == 'jazzcash' ? AppTheme.limeAccent.withOpacity(0.2) : Colors.grey.shade100,
                            borderRadius: BorderRadius.circular(12),
                            border: Border.all(color: selectedGateway == 'jazzcash' ? AppTheme.blackAccent : Colors.grey.shade300, width: 2),
                          ),
                          child: const Column(
                            children: [
                              Icon(Icons.flash_on, color: Colors.orange, size: 24),
                              SizedBox(height: 4),
                              Text('JazzCash', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 13)),
                            ],
                          ),
                        ),
                      ),
                    ),
                    const SizedBox(width: 12),
                    Expanded(
                      child: InkWell(
                        onTap: () => setDialogState(() => selectedGateway = 'easypaisa'),
                        child: Container(
                          padding: const EdgeInsets.symmetric(vertical: 12),
                          decoration: BoxDecoration(
                            color: selectedGateway == 'easypaisa' ? AppTheme.limeAccent.withOpacity(0.2) : Colors.grey.shade100,
                            borderRadius: BorderRadius.circular(12),
                            border: Border.all(color: selectedGateway == 'easypaisa' ? AppTheme.blackAccent : Colors.grey.shade300, width: 2),
                          ),
                          child: const Column(
                            children: [
                              Icon(Icons.account_balance, color: Colors.green, size: 24),
                              SizedBox(height: 4),
                              Text('EasyPaisa', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 13)),
                            ],
                          ),
                        ),
                      ),
                    ),
                  ],
                ),
                const SizedBox(height: 20),
                // FIX C8: Removed MPIN input — it was collected but never sent to the API.
                // The settlement is handled server-side via JazzCash/EasyPaisa webhook callback.
                // Payment is initiated by launching the deep link returned from codPayNow.
              ],
            ),
          ),
          actions: [
            TextButton(
              onPressed: isSubmitting ? null : () => Navigator.pop(ctx),
              child: const Text('Cancel', style: TextStyle(color: Colors.grey)),
            ),
            ElevatedButton(
              onPressed: isSubmitting ? null : () async {
                final messenger = ScaffoldMessenger.of(context);
                final dialogNavigator = Navigator.of(ctx);
                setDialogState(() => isSubmitting = true);
                try {
                  final token = SessionRegistry.instance.token ?? '';
                  final payNowUrl = Uri.parse(ApiEndpoints.codPayNow());
                  final res = await http.post(
                    payNowUrl,
                    headers: {
                      'Authorization': 'Bearer $token',
                      'Content-Type': 'application/json',
                    },
                    body: jsonEncode({
                      'cod_debt_id': codDebtId,
                      'gateway': selectedGateway,
                    }),
                  ).timeout(const Duration(seconds: 10));

                  if (res.statusCode == 200) {
                    // FIX C9: Removed client-fabricated settlement call.
                    // The backend handles settlement via the payment gateway's
                    // webhook callback. The client should NOT fabricate
                    // transaction_id or webhook_event_id — these must come
                    // from the actual payment gateway.
                    // Decode the response to get the deep link for payment.
                    final respBody = jsonDecode(res.body) as Map<String, dynamic>;
                    final deepLink = respBody['deep_link'] as String?;
                    if (deepLink != null && deepLink.isNotEmpty) {
                      final uri = Uri.parse(deepLink);
                      if (await canLaunchUrl(uri)) {
                        await launchUrl(uri, mode: LaunchMode.externalApplication);
                      }
                    }

                    if (mounted) {
                      dialogNavigator.pop();
                      messenger.showSnackBar(
                        SnackBar(
                          content: Text('Payment initiated via ${selectedGateway.toUpperCase()}. Complete payment in the ${selectedGateway} app.'),
                          backgroundColor: Colors.green,
                        ),
                      );
                      _fetchCodDebts();
                      _fetchWalletSummary();
                    }
                  } else {
                    if (mounted) {
                      messenger.showSnackBar(
                        SnackBar(content: Text('Gateway Error: ${res.body}')),
                      );
                    }
                  }
                } catch (e) {
                  if (mounted) {
                    messenger.showSnackBar(
                      SnackBar(content: Text('Settlement Error: $e')),
                    );
                  }
                } finally {
                  setDialogState(() => isSubmitting = false);
                }
              },
              style: ElevatedButton.styleFrom(
                backgroundColor: AppTheme.blackAccent,
                padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 12),
                shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
              ),
              child: isSubmitting
                  ? const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(color: Colors.white, strokeWidth: 2))
                  : const Text('Confirm Payment', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildWalletTab() {
    final balance = ((_walletSummary?['balance'] as num?) ?? 0).toDouble();
    final lifetime = ((_walletSummary?['lifetime_earnings'] as num?) ?? 0).toDouble();
    final credits = (_walletSummary?['recent_credits'] ?? <dynamic>[]) as List<dynamic>;

    return RefreshIndicator(
      onRefresh: _fetchWalletSummary,
      color: AppTheme.blackAccent,
      child: SingleChildScrollView(
        physics: const AlwaysScrollableScrollPhysics(),
        padding: const EdgeInsets.all(20),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Container(
              width: double.infinity,
              padding: const EdgeInsets.all(24),
              decoration: BoxDecoration(
                color: const Color(0xFF121217),
                borderRadius: BorderRadius.circular(24),
                boxShadow: const [BoxShadow(color: Colors.black12, blurRadius: 10)],
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Text('Available Earnings Balance', style: TextStyle(color: Colors.white70, fontSize: 13)),
                  const SizedBox(height: 8),
                  Text('PKR ${balance.toStringAsFixed(2)}', style: const TextStyle(color: AppTheme.limeAccent, fontSize: 32, fontWeight: FontWeight.bold)),
                  const SizedBox(height: 20),
                  const Divider(color: Colors.white24),
                  const SizedBox(height: 12),
                  Row(
                    children: [
                      const Icon(Icons.trending_up, color: AppTheme.limeAccent, size: 20),
                      const SizedBox(width: 8),
                      Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          const Text('Lifetime Completed Earnings', style: TextStyle(color: Colors.white70, fontSize: 12)),
                          Text('PKR ${lifetime.toStringAsFixed(2)}', style: const TextStyle(color: Colors.white, fontSize: 16, fontWeight: FontWeight.bold)),
                        ],
                      ),
                    ],
                  ),
                ],
              ),
            ),
            const SizedBox(height: 24),
            const Text('Recent Completed Gig Earnings', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: AppTheme.blackAccent)),
            const SizedBox(height: 12),
            if (credits.isEmpty)
              Container(
                width: double.infinity,
                padding: const EdgeInsets.all(32),
                decoration: BoxDecoration(
                  color: Colors.white,
                  borderRadius: BorderRadius.circular(20),
                  border: Border.all(color: Colors.grey.shade200),
                ),
                child: const Column(
                  children: [
                    Icon(Icons.inbox_outlined, color: Colors.grey, size: 40),
                    SizedBox(height: 12),
                    Text('No completed delivery credits yet.', style: TextStyle(color: Colors.grey, fontSize: 14, fontWeight: FontWeight.w500)),
                  ],
                ),
              )
            else
              ...credits.map((c) {
                final item = c as Map<String, dynamic>;
                final orderId = item['order_id']?.toString() ?? 'N/A';
                final net = num.tryParse(item['net_credit']?.toString() ?? '0')?.toDouble() ?? 0.0;
                final fee = num.tryParse(item['delivery_fee']?.toString() ?? '0')?.toDouble() ?? 0.0;
                final commission = num.tryParse(item['admin_commission']?.toString() ?? '0')?.toDouble() ?? 0.0;

                return Container(
                  margin: const EdgeInsets.only(bottom: 12),
                  padding: const EdgeInsets.all(16),
                  decoration: BoxDecoration(
                    color: Colors.white,
                    borderRadius: BorderRadius.circular(16),
                    border: Border.all(color: Colors.grey.shade200),
                    boxShadow: const [BoxShadow(color: Colors.black12, blurRadius: 4)],
                  ),
                  child: Row(
                    children: [
                      CircleAvatar(
                        radius: 20,
                        backgroundColor: Colors.green.shade50,
                        child: const Icon(Icons.arrow_downward, color: Colors.green, size: 20),
                      ),
                      const SizedBox(width: 14),
                      Expanded(
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Text('Order: $orderId', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 14, color: AppTheme.blackAccent)),
                            const SizedBox(height: 2),
                            Text('Delivery Fee: PKR ${fee.toStringAsFixed(2)} • Admin Fee: PKR ${commission.toStringAsFixed(2)}', style: TextStyle(color: Colors.grey.shade600, fontSize: 11)),
                          ],
                        ),
                      ),
                      Text('+ PKR ${net.toStringAsFixed(2)}', style: const TextStyle(color: Colors.green, fontWeight: FontWeight.bold, fontSize: 14)),
                    ],
                  ),
                );
              }),
          ],
        ),
      ),
    );
  }

  Widget _buildCODLedgerTab() {
    final pendingDebts = _codDebts.where((d) => (d['status']?.toString() ?? '') == 'pending').toList();
    final settledDebts = _codDebts.where((d) => (d['status']?.toString() ?? '') == 'settled').toList();
    final totalOwed = pendingDebts.fold<double>(0, (sum, d) => sum + ((d['amount_owed'] as num?) ?? 0).toDouble());

    return RefreshIndicator(
      onRefresh: _fetchCodDebts,
      color: AppTheme.blackAccent,
      child: SingleChildScrollView(
        physics: const AlwaysScrollableScrollPhysics(),
        padding: const EdgeInsets.all(20),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Container(
              width: double.infinity,
              padding: const EdgeInsets.all(24),
              decoration: BoxDecoration(
                color: totalOwed > 0 ? Colors.red.shade900 : const Color(0xFF121217),
                borderRadius: BorderRadius.circular(24),
                boxShadow: const [BoxShadow(color: Colors.black12, blurRadius: 10)],
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    children: [
                      Icon(totalOwed > 0 ? Icons.warning_amber_rounded : Icons.check_circle_outline, color: totalOwed > 0 ? Colors.amber : Colors.greenAccent, size: 24),
                      const SizedBox(width: 8),
                      Text(
                        totalOwed > 0 ? 'Pending Platform COD Deposit' : 'COD Ledger Clear',
                        style: const TextStyle(color: Colors.white, fontSize: 15, fontWeight: FontWeight.bold),
                      ),
                    ],
                  ),
                  const SizedBox(height: 12),
                  Text(
                    'PKR ${totalOwed.toStringAsFixed(2)}',
                    style: const TextStyle(color: Colors.white, fontSize: 34, fontWeight: FontWeight.bold),
                  ),
                  const SizedBox(height: 8),
                  Text(
                    totalOwed > 0
                        ? 'Cash collected from customer deliveries to be deposited to platform via JazzCash/EasyPaisa.'
                        : 'No pending Cash on Delivery debt. All customer collections are fully settled.',
                    style: const TextStyle(color: Colors.white70, fontSize: 12),
                  ),
                ],
              ),
            ),
            const SizedBox(height: 24),
            Row(
              mainAxisAlignment: MainAxisAlignment.spaceBetween,
              children: [
                const Text('Pending COD Collections', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: AppTheme.blackAccent)),
                if (_isLoadingCodDebts)
                  const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2))
                else
                  Text('${pendingDebts.length} Pending', style: TextStyle(color: Colors.red.shade700, fontWeight: FontWeight.bold, fontSize: 12)),
              ],
            ),
            const SizedBox(height: 12),
            if (pendingDebts.isEmpty)
              Container(
                width: double.infinity,
                padding: const EdgeInsets.all(32),
                decoration: BoxDecoration(
                  color: Colors.white,
                  borderRadius: BorderRadius.circular(20),
                  border: Border.all(color: Colors.grey.shade200),
                ),
                child: const Column(
                  children: [
                    Icon(Icons.verified_rounded, color: Colors.green, size: 40),
                    SizedBox(height: 12),
                    Text('All COD cash collections have been settled.', style: TextStyle(color: Colors.grey, fontSize: 14, fontWeight: FontWeight.w500)),
                  ],
                ),
              )
            else
              ...pendingDebts.map((debt) {
                final debtMap = debt as Map<String, dynamic>;
                final debtId = debtMap['id']?.toString() ?? '';
                final amount = ((debtMap['amount_owed'] as num?) ?? 0).toDouble();
                final orderId = debtMap['order_tracking_id']?.toString() ?? 'N/A';

                return Container(
                  margin: const EdgeInsets.only(bottom: 12),
                  padding: const EdgeInsets.all(16),
                  decoration: BoxDecoration(
                    color: Colors.white,
                    borderRadius: BorderRadius.circular(20),
                    border: Border.all(color: Colors.red.shade100),
                    boxShadow: const [BoxShadow(color: Colors.black12, blurRadius: 4)],
                  ),
                  child: Column(
                    children: [
                      Row(
                        mainAxisAlignment: MainAxisAlignment.spaceBetween,
                        children: [
                          Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              Text('Order: $orderId', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 14, color: AppTheme.blackAccent)),
                              const SizedBox(height: 4),
                              Text('Cash Collected', style: TextStyle(color: Colors.grey.shade600, fontSize: 12)),
                            ],
                          ),
                          Text('PKR ${amount.toStringAsFixed(2)}', style: TextStyle(color: Colors.red.shade700, fontWeight: FontWeight.bold, fontSize: 16)),
                        ],
                      ),
                      const SizedBox(height: 14),
                      SizedBox(
                        width: double.infinity,
                        child: ElevatedButton.icon(
                          onPressed: () => _showPayDebtDialog(debtId, amount),
                          icon: const Icon(Icons.payment, size: 18, color: Colors.white),
                          label: const Text('Pay Settlement (MPIN Required)', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
                          style: ElevatedButton.styleFrom(
                            backgroundColor: Colors.red.shade800,
                            padding: const EdgeInsets.symmetric(vertical: 12),
                            shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
                          ),
                        ),
                      ),
                    ],
                  ),
                );
              }),
            const SizedBox(height: 24),
            if (settledDebts.isNotEmpty) ...[
              const Text('Settlement History', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: AppTheme.blackAccent)),
              const SizedBox(height: 12),
              ...settledDebts.map((debt) {
                final debtMap = debt as Map<String, dynamic>;
                final amount = ((debtMap['amount_owed'] as num?) ?? 0).toDouble();
                final orderId = debtMap['order_tracking_id']?.toString() ?? 'N/A';
                final via = debtMap['settled_via']?.toString() ?? 'gateway';

                return Container(
                  margin: const EdgeInsets.only(bottom: 12),
                  padding: const EdgeInsets.all(14),
                  decoration: BoxDecoration(
                    color: Colors.white,
                    borderRadius: BorderRadius.circular(16),
                    border: Border.all(color: Colors.grey.shade200),
                  ),
                  child: Row(
                    children: [
                      const Icon(Icons.check_circle, color: Colors.green, size: 22),
                      const SizedBox(width: 12),
                      Expanded(
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Text('Order: $orderId', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 13, color: AppTheme.blackAccent)),
                            Text('Settled via ${via.toUpperCase()}', style: TextStyle(color: Colors.grey.shade600, fontSize: 11)),
                          ],
                        ),
                      ),
                      Text('PKR ${amount.toStringAsFixed(2)}', style: const TextStyle(color: Colors.green, fontWeight: FontWeight.bold, fontSize: 13)),
                    ],
                  ),
                );
              }),
            ],
          ],
        ),
      ),
    );
  }

  // ── Surge heatmap helpers ────────────────────────────────────────
  // The previous flutter_map PolygonLayer rendered full hex polygons.
  // MapLibreMapWidget does not yet expose a fill layer, so we approximate
  // each hex with a centroid marker whose icon size scales with the
  // surge multiplier (1.0 → 0.6, 2.0+ → 1.4). When the widget gains
  // polygon support these helpers can be deleted.

  LatLng _hexCentroid(dynamic hex) {
    final boundary = (hex['boundary'] as List<dynamic>?) ?? const [];
    if (boundary.isEmpty) return const LatLng(0, 0);
    double sumLat = 0, sumLng = 0;
    for (final b in boundary) {
      final coord = b as List<dynamic>;
      sumLng += (coord[0] as num).toDouble();
      sumLat += (coord[1] as num).toDouble();
    }
    return LatLng(sumLat / boundary.length, sumLng / boundary.length);
  }

  double _hexIconSize(dynamic hex) {
    final surge = (hex['surge_multiplier'] as num?)?.toDouble() ?? 1.0;
    if (surge >= 2.0) return 1.4;
    if (surge >= 1.5) return 1.0;
    return 0.6;
  }
}
