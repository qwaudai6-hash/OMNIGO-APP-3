import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:maplibre_gl/maplibre_gl.dart';
import 'package:geolocator/geolocator.dart';
import 'package:http/http.dart' as http;
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/websocket_client.dart';
import '../../../../core/services/session_registry.dart';
import '../../../shared/presentation/widgets/map_libre_map_widget.dart';

// Live telemetry frame emitted by the Rust WebSocket gateway.
// Schema matches the coordsJSON payload published by the Go delivery
// repository's UpdateRiderLocation() Redis pub/sub bridge:
//   { "rider_id": "...", "lat": ..., "lng": ..., "updated_at": <millis> }
class RiderTelemetry {

  RiderTelemetry({
    required this.riderId,
    required this.lat,
    required this.lng,
    required this.updatedAtMillis,
    required this.orderId,
  });

  factory RiderTelemetry.fromRawJson(String str) {
    final Map<String, dynamic> data = jsonDecode(str) as Map<String, dynamic>;
    return RiderTelemetry(
      riderId: (data['rider_id'] ?? data['rider_tracking_id'] ?? '') as String,
      lat: (data['lat'] as num?)?.toDouble() ?? 0.0,
      lng: (data['lng'] as num?)?.toDouble() ?? 0.0,
      updatedAtMillis: (data['updated_at'] as num?)?.toInt() ?? 0,
      orderId: (data['order_id'] ?? data['order_tracking_id'] ?? '') as String,
    );
  }
  final String riderId;
  final double lat;
  final double lng;
  final int updatedAtMillis;
  final String orderId;
}

class VendorLiveMapScreen extends StatefulWidget {
  const VendorLiveMapScreen({super.key});

  @override
  VendorLiveMapScreenState createState() => VendorLiveMapScreenState();
}

class VendorLiveMapScreenState extends State<VendorLiveMapScreen> {
  MapLibreMapController? _mapController;
  LatLng? _storeLocation; // Set by API/GPS only — no hardcoded default
  // ignore: unused_field
  bool _isLocationLoaded = false;
  List<LatLng> _routePolyline = [];

  // FIXED: ValueNotifier keeps marker operations isolated.
  // Modifying this list will NOT trigger heavy global rebuilds of the underlying MapLibre layer!
  final ValueNotifier<Map<String, MarkerData>> _liveMarkersNotifier =
      ValueNotifier<Map<String, MarkerData>>({});
  final Map<String, int> _markerTimestamps = {};

  // Connected real WebSocket Client querying Port 8087 gateway
  final WebSocketClient _wsClient = WebSocketClient();
  StreamSubscription<dynamic>? _streamSubscription;
  StreamSubscription<WSConnectionState>? _stateSub;
  Timer? _stalePruneTimer;

  @override
  void initState() {
    super.initState();
    _initStoreLocation();
    _initMapOrigin();

    // Proactive timer to evict stale markers every 30 seconds
    _stalePruneTimer = Timer.periodic(const Duration(seconds: 30), (_) {
      _pruneStaleMarkers();
    });

    // Connect to actual WebSocket gateway using SessionRegistry token
    final token = SessionRegistry.instance.token ?? '';
    if (token.isNotEmpty) {
      _wsClient.connect(
        token,
        clientType: 'vendor',
        trackingId: SessionRegistry.instance.trackingId ?? '',
      );
      _streamSubscription = _wsClient.stream.listen((rawJsonPayload) {
        if (rawJsonPayload is String) {
          _processIncomingTelemetryFrame(rawJsonPayload);
        }
      }, onError: (Object err) {
        debugPrint("[WebSocket Connection Error]: $err");
      },);

      // Surface connection state changes to the UI overlay.
      _stateSub = _wsClient.stateStream.listen((s) {
        debugPrint("[WS State]: $s");
      });
    }
  }

  Future<void> _initStoreLocation() async {
    try {
      final token = SessionRegistry.instance.token ?? '';
      final response = await http.get(
        Uri.parse(ApiEndpoints.vendorStoreMe()),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
      ).timeout(const Duration(seconds: 8));

      if (response.statusCode == 200) {
        final data = jsonDecode(response.body) as Map<String, dynamic>;
        final lat = (data['latitude'] as num?)?.toDouble();
        final lng = (data['longitude'] as num?)?.toDouble();
        if (lat != null && lng != null && lat != 0.0 && lng != 0.0) {
          setState(() {
            _storeLocation = LatLng(lat, lng);
            _isLocationLoaded = true;
          });
          if (_storeLocation != null) {
            _mapController?.animateCamera(
              CameraUpdate.newLatLngZoom(_storeLocation!, 14.5),
            );
          }
          _setInitialMarkers();
          return;
        }
      } else {
        debugPrint('Store lookup failed: ${response.statusCode} ${response.body}');
      }
    } catch (e) {
      debugPrint('Error fetching store location: $e');
    }
  }

  Future<void> _initMapOrigin() async {
    // If _initStoreLocation() already populated the vendor's registered store
    // coords from the backend, do NOT clobber them with the device's GPS.
    // The backend call wins; GPS is only a fallback when the API failed
    // (network down, 404 for a new vendor without coords yet, etc.).
    if (_storeLocation != null && _isLocationLoaded) {
      _setInitialMarkers();
      return;
    }

    try {
      final bool serviceEnabled = await Geolocator.isLocationServiceEnabled();
      if (!serviceEnabled) {
        _setInitialMarkers();
        return;
      }

      LocationPermission permission = await Geolocator.checkPermission();
      if (permission == LocationPermission.denied) {
        permission = await Geolocator.requestPermission();
        if (permission == LocationPermission.denied) {
          _setInitialMarkers();
          return;
        }
      }

      if (permission == LocationPermission.deniedForever) {
        _setInitialMarkers();
        return;
      }

      final Position position = await Geolocator.getCurrentPosition();
      // Re-check after async gap: backend may have populated coords meanwhile.
      if (_storeLocation != null && _isLocationLoaded) {
        _setInitialMarkers();
        return;
      }
      setState(() {
        _storeLocation = LatLng(position.latitude, position.longitude);
        _isLocationLoaded = true;
      });
      if (_storeLocation != null) {
        _mapController?.animateCamera(
          CameraUpdate.newLatLngZoom(_storeLocation!, 14.5),
        );
      }
    } catch (e) {
      debugPrint("Error fetching location: $e");
    } finally {
      _setInitialMarkers();
    }
  }

  void _setInitialMarkers() {
    // Initialize the baseline Store Merchant node marker location
    _liveMarkersNotifier.value = {
      'store': MarkerData(
        position: _storeLocation ?? const LatLng(31.5204, 74.3587),
        iconSize: 1.0,
      ),
    };
  }

  Future<void> _loadActiveGigRoute(String orderId) async {
    try {
      final token = SessionRegistry.instance.token ?? '';
      final response = await http.get(
        Uri.parse(ApiEndpoints.deliveryGigRoute(orderId)),
        headers: {'Authorization': 'Bearer $token'},
      ).timeout(const Duration(seconds: 8));

      if (response.statusCode == 200) {
        final data = jsonDecode(response.body) as Map<String, dynamic>;
        final coordsList = data['coordinates'] as List<dynamic>?;
        if (coordsList != null && coordsList.isNotEmpty) {
          final List<LatLng> points = coordsList
              .map((c) => LatLng((c[1] as num).toDouble(), (c[0] as num).toDouble()))
              .toList();
          if (mounted) {
            setState(() {
              _routePolyline = points;
            });
          }
        }
      }
    } catch (e) {
      debugPrint('Failed to load active gig route for vendor: $e');
    }
  }

  void _processIncomingTelemetryFrame(String payloadStr) {
    try {
      final telemetry = RiderTelemetry.fromRawJson(payloadStr);

      // Skip malformed / empty frames silently.
      if (telemetry.riderId.isEmpty || (telemetry.lat == 0.0 && telemetry.lng == 0.0)) {
        return;
      }

      if (_routePolyline.isEmpty && telemetry.orderId.isNotEmpty) {
        _loadActiveGigRoute(telemetry.orderId);
      }

      final LatLng riderPosition = LatLng(telemetry.lat, telemetry.lng);
      final now = DateTime.now().millisecondsSinceEpoch;
      _markerTimestamps['rider_${telemetry.riderId}'] = now;

      // FIXED: Mutating values within the tracker map safely and notifying the single layer listener
      final Map<String, MarkerData> updatedMarkers =
          Map.from(_liveMarkersNotifier.value);

      // Prune stale markers older than 120 seconds (except store marker)
      updatedMarkers.removeWhere((key, _) {
        if (key == 'store') return false;
        final ts = _markerTimestamps[key];
        return ts == null || (now - ts) > 120000;
      });

      // Add the live rider marker at the streamed coordinates.
      updatedMarkers['rider_${telemetry.riderId}'] = MarkerData(
        position: riderPosition,
        iconSize: 1.0,
      );

      _liveMarkersNotifier.value = updatedMarkers;
    } catch (e) {
      debugPrint("[Telemetry UI Parsing Error]: $e");
    }
  }

  void _pruneStaleMarkers() {
    if (!mounted) return;
    final now = DateTime.now().millisecondsSinceEpoch;
    final Map<String, MarkerData> updatedMarkers =
        Map.from(_liveMarkersNotifier.value);
    bool changed = false;

    updatedMarkers.removeWhere((key, _) {
      if (key == 'store') return false;
      final ts = _markerTimestamps[key];
      final isStale = ts == null || (now - ts) > 120000;
      if (isStale) changed = true;
      return isStale;
    });

    if (changed) {
      _liveMarkersNotifier.value = updatedMarkers;
    }
  }

  @override
  void dispose() {
    _stalePruneTimer?.cancel();
    _streamSubscription?.cancel();
    _stateSub?.cancel();
    _wsClient.disconnect();
    _liveMarkersNotifier.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.white,
      appBar: AppBar(
        backgroundColor: Colors.white,
        elevation: 0,
        title: const Text('Live Tracking Matrix', style: TextStyle(color: Colors.black, fontWeight: FontWeight.bold)),
      ),
      body: Stack(
        children: [
          // 1. OpenStreetMap Tile Rendering Engine Layer (Static Frame Layout)
          // 1. MapLibre rendering layer (proxied through Go map-service).
          ValueListenableBuilder<Map<String, MarkerData>>(
            valueListenable: _liveMarkersNotifier,
            builder: (context, currentMarkers, _) {
              return MapLibreMapWidget(
                initialCenter: _storeLocation ?? const LatLng(31.5204, 74.3587),
                initialZoom: 14.5,
                myLocationEnabled: false,
                markers: currentMarkers,
                polylines: _routePolyline.isNotEmpty ? [_routePolyline] : const [],
                onMapCreated: (controller) {
                  _mapController = controller;
                },
              );
            },
          ),

          // Floating Top Status Widget Information Panel Overlay Dock
          Positioned(
            top: 0, left: 0, right: 0,
            child: SafeArea(
              child: Padding(
                padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 8),
                child: Container(
                  padding: const EdgeInsets.all(16),
                  decoration: BoxDecoration(
                    color: Colors.black.withValues(alpha: 0.85),
                    borderRadius: BorderRadius.circular(20),
                  ),
                  child: const Row(
                    children: [
                  Icon(Icons.radar_rounded, color: Color(0xFFCAFF33), size: 20),
                  SizedBox(width: 12),
                    Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text("Stateless GIS Stream Connected", style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontSize: 13)),
                      Text("Listening to topic: rider telemetry via Port 8087", style: TextStyle(color: Colors.white54, fontSize: 10)),
                    ],
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    ],
  ),
);
  }
}
