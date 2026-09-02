import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:http/http.dart' as http;
import 'package:maplibre_gl/maplibre_gl.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:geolocator/geolocator.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/services/session_registry.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/network/websocket_client.dart';
import '../../../../core/di/service_locator.dart';
import 'package:provider/provider.dart';
import '../../../../core/services/cart_provider.dart';
import '../../data/models/product.dart';
import 'product_details_screen.dart';
import 'edit_profile_screen.dart';
import 'wishlist_screen.dart';
import 'order_detail_screen.dart';
import 'cart_screen.dart';
import '../widgets/vehicle_selector_sheet.dart';
import 'customer_wallet_screen.dart';
import '../../../shared/presentation/widgets/map_libre_map_widget.dart';
import '../../../../shared/presentation/widgets/chat_nav_button.dart';
import '../../../../shared/presentation/services/chat_service.dart';

class CustomerDashboardScreen extends StatefulWidget {
  const CustomerDashboardScreen({super.key, required this.trackingId});
  final String trackingId;

  @override
  CustomerDashboardScreenState createState() =>
      CustomerDashboardScreenState();
}

class CustomerDashboardScreenState extends State<CustomerDashboardScreen> {
  int _currentIndex = 0;
  MapLibreMapController? _mapController;
  // Default to Pakistan's centroid (≈ Rahim Yar Khan) — neutral, not
  // Lahore-specific. This is only shown for the few seconds before the
  // first GPS fix arrives, after which _fetchMapCenterFromGPS() replaces
  // it. We deliberately do NOT bias the map to a specific city.
  LatLng _mapCenter = const LatLng(30.3753, 69.3451);
  final Map<String, MarkerData> _mapMarkers = {};
  final TextEditingController _mapSearchController = TextEditingController();
  final TextEditingController _searchController = TextEditingController();

  String _searchQuery = '';
  String _selectedCategory = 'All';
  Timer? _searchDebounce;

  // Strict UI Loading state for Button Debouncing


  // Pagination & API state
  bool _isLoadingCatalog = false;
  bool _hasMore = true;
  int _offset = 0;
  final int _limit = 20;
  List<Product> _allProducts = [];
  final ScrollController _scrollController = ScrollController();

  List<dynamic> _customerOrders = [];
  bool _isLoadingOrders = false;

  // ── Wishlist state ───────────────────────────────────────────────
  Set<String> _favoriteProductIds = {};
  bool _isLoadingWishlist = false;

  bool _isGeocoding = false;

  // ── Live Rider Tracking (WebSocket telemetry) ────────────────────
  WebSocketClient? _wsClient;
  StreamSubscription<dynamic>? _wsSub;
  StreamSubscription<dynamic>? _wsOrderTopicSub;
  StreamSubscription<WSConnectionState>? _wsStateSub;
  // riderTrackingId → latest LatLng, broadcast so Map tab rebuilds only
  // the marker layer without touching the rest of the dashboard.
  final ValueNotifier<Map<String, LatLng>> _riderMarkers =
      ValueNotifier<Map<String, LatLng>>({});

  // ── Ride Hailing / Dynamic Pricing State ─────────────────────────
  List<LatLng> _rideRoutePolyline = [];
  LatLng? _ridePickupLatLng;
  LatLng? _rideDropoffLatLng;
  bool _isEstimatingRide = false;

  @override
  void initState() {
    super.initState();
    _fetchCatalog();
    _fetchOrders();
    _fetchWishlist();
    _fetchMapCenterFromGPS(); // Center map on real user location

    _scrollController.addListener(() {
      if (_scrollController.position.pixels >=
              _scrollController.position.maxScrollExtent - 200 &&
          !_isLoadingCatalog &&
          _hasMore) {
        _fetchCatalog();
      }
    });

    _mapMarkers['me'] = MarkerData(
      position: _mapCenter,
      iconSize: 1.0,
    );
  }

  /// Fetch real GPS location and center map on user's position.
  ///
  /// Production flow (Session 59):
  ///   1. Check service enabled → if off, show "Open Settings" dialog.
  ///   2. Check permission → walk through denied / deniedForever /
  ///      unableToDetermine with proper dialogs.
  ///   3. On success, replace the default centroid with the real fix and
  ///      pan the map to it.
  Future<void> _fetchMapCenterFromGPS() async {
    try {
      final bool serviceEnabled = await Geolocator.isLocationServiceEnabled();
      if (!serviceEnabled) {
        unawaited(_showLocationServiceOffDialog());
        return;
      }

      LocationPermission permission = await Geolocator.checkPermission();
      if (permission == LocationPermission.denied) {
        permission = await Geolocator.requestPermission();
        if (permission == LocationPermission.denied) {
          unawaited(_showPermissionDeniedDialog());
          return;
        }
      }
      if (permission == LocationPermission.deniedForever) {
        unawaited(_showPermissionPermanentlyDeniedDialog());
        return;
      }
      if (permission == LocationPermission.unableToDetermine) {
        permission = await Geolocator.requestPermission();
        if (permission != LocationPermission.always &&
            permission != LocationPermission.whileInUse) {
          unawaited(_showPermissionDeniedDialog());
          return;
        }
      }

      final position = await Geolocator.getCurrentPosition(
        locationSettings: const LocationSettings(
          accuracy: LocationAccuracy.high,
          timeLimit: Duration(seconds: 10),
        ),
      );

      if (mounted) {
        final realLocation = LatLng(position.latitude, position.longitude);
        setState(() {
          _mapCenter = realLocation;
          // Replace the default centroid marker with real location
          _mapMarkers.clear();
          _mapMarkers['me'] = MarkerData(
            position: realLocation,
            iconSize: 1.0,
          );
        });
        // Pan the map to the real fix.
        _mapController?.animateCamera(
          CameraUpdate.newLatLng(realLocation),
        );
      }
    } catch (e) {
      debugPrint('GPS fetch failed: $e');
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
            content: Text('Could not get your location. Search by address instead.'),
            duration: Duration(seconds: 3),
          ),
        );
      }
    }
  }

  Future<void> _showLocationServiceOffDialog() async {
    if (!mounted) return;
    return showDialog<void>(
      context: context,
      barrierDismissible: false,
      builder: (ctx) => AlertDialog(
        title: const Text('Location Service is Off'),
        content: const Text(
          'OMNIGO needs your location to show nearby stores and accurate '
          'delivery estimates. Please turn on Location in your device settings.',
        ),
        actions: [
          TextButton(onPressed: () => Navigator.of(ctx).pop(), child: const Text('Not now')),
          FilledButton(
            onPressed: () {
              Navigator.of(ctx).pop();
              Geolocator.openLocationSettings();
            },
            child: const Text('Open Settings'),
          ),
        ],
      ),
    );
  }

  Future<void> _showPermissionDeniedDialog() async {
    if (!mounted) return;
    return showDialog<void>(
      context: context,
      barrierDismissible: false,
      builder: (ctx) => AlertDialog(
        title: const Text('Location Permission Denied'),
        content: const Text(
          'OMNIGO cannot show your location without permission. '
          'Tap "Try Again" to request it again.',
        ),
        actions: [
          TextButton(onPressed: () => Navigator.of(ctx).pop(), child: const Text('Cancel')),
          FilledButton(
            onPressed: () {
              Navigator.of(ctx).pop();
              _fetchMapCenterFromGPS();
            },
            child: const Text('Try Again'),
          ),
        ],
      ),
    );
  }

  Future<void> _showPermissionPermanentlyDeniedDialog() async {
    if (!mounted) return;
    return showDialog<void>(
      context: context,
      barrierDismissible: false,
      builder: (ctx) => AlertDialog(
        title: const Text('Permission Blocked'),
        content: const Text(
          'You have permanently denied Location permission. Open the system '
          'app settings to grant it.',
        ),
        actions: [
          TextButton(onPressed: () => Navigator.of(ctx).pop(), child: const Text('Cancel')),
          FilledButton(
            onPressed: () {
              Navigator.of(ctx).pop();
              Geolocator.openAppSettings();
            },
            child: const Text('Open App Settings'),
          ),
        ],
      ),
    );
  }

  @override
  void dispose() {
    _searchDebounce?.cancel();
    _wsSub?.cancel();
    _wsOrderTopicSub?.cancel();
    _wsStateSub?.cancel();
    _wsClient?.disconnect();
    _riderMarkers.dispose();
    _scrollController.dispose();
    _mapSearchController.dispose();
    _searchController.dispose();
    super.dispose();
  }

  // ── Wishlist API calls ───────────────────────────────────────────

  Future<void> _fetchWishlist() async {
    if (_isLoadingWishlist) return;
    setState(() => _isLoadingWishlist = true);

    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      final response = await http.get(
        Uri.parse(ApiEndpoints.wishlistList()),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
      ).timeout(const Duration(seconds: 8));

      if (response.statusCode == 200) {
        final data = jsonDecode(response.body) as Map<String, dynamic>;
        final List<dynamic> ids = (data['product_tracking_ids'] as List<dynamic>?) ?? <dynamic>[];
        if (mounted) {
          setState(() {
            _favoriteProductIds = ids.map((e) => e.toString()).toSet();
            _isLoadingWishlist = false;
          });
        }
      } else {
        if (mounted) setState(() => _isLoadingWishlist = false);
      }
    } catch (e) {
      if (mounted) setState(() => _isLoadingWishlist = false);
      debugPrint('Error fetching wishlist: $e');
    }
  }

  Future<void> _toggleFavorite(String productId) async {
    final wasFavorited = _favoriteProductIds.contains(productId);
    // Optimistic update
    setState(() {
      if (wasFavorited) {
        _favoriteProductIds.remove(productId);
      } else {
        _favoriteProductIds.add(productId);
      }
    });

    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      final response = await http.post(
        Uri.parse(ApiEndpoints.wishlistToggle(productId)),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
      ).timeout(const Duration(seconds: 5));

      if (response.statusCode != 200) {
        // Rollback on failure
        if (!mounted) return;
        setState(() {
          if (wasFavorited) {
            _favoriteProductIds.add(productId);
          } else {
            _favoriteProductIds.remove(productId);
          }
        });
      }
    } catch (e) {
      // Rollback on network error
      if (!mounted) return;
      setState(() {
        if (wasFavorited) {
          _favoriteProductIds.add(productId);
        } else {
          _favoriteProductIds.remove(productId);
        }
      });
    }
  }

  /// Connects to the Rust WebSocket gateway (port 8087) to receive live
  /// rider telemetry for any of this customer's shipped orders. Safe to
  /// call multiple times — guards against duplicate connections.
  void _connectRiderTelemetry() {
    if (_wsClient != null) return; // already connected
    final token = SessionRegistry.instance.token ?? '';
    if (token.isEmpty) return;

    _wsClient = sl<WebSocketClient>();
    _wsClient!.connect(
      token,
      clientType: 'customer',
      trackingId: SessionRegistry.instance.trackingId ?? '',
    );

    // Bind the chat service to the WS so incoming chat messages are
    // surfaced to the chat button's badge.
    ChatService.instance.bindToWebSocket(_wsClient!);
    ChatService.instance.setUserId(SessionRegistry.instance.trackingId ?? '');

    _wsSub = _wsClient!.stream.listen((raw) {
      if (raw is! String) return;
      try {
        final frame = jsonDecode(raw) as Map<String, dynamic>;
        // Skip chat frames — the ChatService handles those.
        if (frame['action'] == 'CHAT_MESSAGE') return;

        // Handle rider telemetry for live map
        final riderId = frame['rider_id']?.toString() ??
            frame['rider_tracking_id']?.toString() ??
            '';
        final lat = (frame['lat'] as num?)?.toDouble();
        final lng = (frame['lng'] as num?)?.toDouble();
        if (riderId.isEmpty || lat == null || lng == null) return;

        // Mutate a copy so ValueNotifier fires reliably.
        final updated = Map<String, LatLng>.from(_riderMarkers.value);
        updated[riderId] = LatLng(lat, lng);
        _riderMarkers.value = updated;
      } catch (e) {
        debugPrint('[Rider Telemetry Parse Error]: $e');
      }
    }, onError: (dynamic e) {
      debugPrint('[Rider WS Error]: $e');
    },);

    _wsStateSub = _wsClient!.stateStream.listen((s) {
      debugPrint('[Customer WS State]: $s');
    });

    // ── Topic Stream: Order Status Updates ──────────────────────────
    // Uses throttled topic stream to prevent UI flooding when server
    // pushes bursts of order status frames. Backward compatible:
    // untagged gateway frames are broadcast to all topic controllers.
    _wsOrderTopicSub = _wsClient!.topicStream('orders').listen((raw) {
      if (raw is! String) return;
      try {
        final frame = jsonDecode(raw) as Map<String, dynamic>;
        if (frame['action'] == 'ORDER_STATUS_UPDATED') {
          final orderId = frame['order_id']?.toString() ?? '';
          final newStatus = frame['status']?.toString() ?? '';
          if (orderId.isNotEmpty && newStatus.isNotEmpty) {
            _applyOrderStatusUpdate(orderId, newStatus);
          }
        }
      } catch (_) {}
    });
  }

  /// Applies a real-time order status update received via WebSocket.
  /// Updates the local order list without requiring an HTTP refresh.
  void _applyOrderStatusUpdate(String orderId, String newStatus) {
    if (!mounted) return;
    setState(() {
      for (var i = 0; i < _customerOrders.length; i++) {
        if ((_customerOrders[i]['order_tracking_id']?.toString() ?? _customerOrders[i]['id']?.toString()) == orderId) {
          _customerOrders[i] = Map<String, dynamic>.from(_customerOrders[i] as Map) ..['status'] = newStatus;
          break;
        }
      }
    });
    debugPrint('[OrderWS] Order $orderId status updated to $newStatus (real-time)');
  }

  /// Resets pagination and fetches a fresh page from the backend using the
  /// current `_searchQuery` and `_selectedCategory` as server-side filters.
  /// This replaces the old client-side `.where()` filter — the Go
  /// product-service already supports `?search=` and `?category=` query
  /// params via ILIKE + exact category match.
  final String _selectedSort = 'newest';
  final double _minPriceFilter = 0.0;
  final double _maxPriceFilter = 50000.0;

  Future<void> _fetchCatalog({bool reset = false}) async {
    if (_isLoadingCatalog) return;
    if (reset) {
      _offset = 0;
      _hasMore = true;
      _allProducts = [];
    }
    if (!_hasMore) return;

    setState(() => _isLoadingCatalog = true);

    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      final url = Uri.parse(ApiEndpoints.productsList(
        limit: _limit,
        offset: _offset,
        search: _searchQuery,
        category: _selectedCategory,
        sort: _selectedSort,
        minPrice: _minPriceFilter,
        maxPrice: _maxPriceFilter,
      ),);
      final response = await http.get(url, headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer $token',
      },);

      if (response.statusCode == 200) {
        final List<dynamic> data = jsonDecode(response.body) as List<dynamic>;
        if (mounted) {
          setState(() {
            if (data.isEmpty) {
              _hasMore = false;
            } else {
              _allProducts.addAll(data.map((e) => Product.fromJson(e as Map<String, dynamic>)).toList());
              _offset += _limit;
            }
            _isLoadingCatalog = false;
          });
        }
      } else {
        throw Exception('Server error: ${response.statusCode}');
      }
    } catch (e) {
      if (mounted) {
        setState(() {
          _isLoadingCatalog = false;
        });
        debugPrint('Error fetching catalog: $e');
      }
    }
  }

  /// Debounced server-side search trigger. Called from the search TextField.
  void _onSearchChanged(String val) {
    setState(() => _searchQuery = val);
    _searchDebounce?.cancel();
    _searchDebounce = Timer(const Duration(milliseconds: 400), () {
      _fetchCatalog(reset: true);
    });
  }

  /// Server-side category filter trigger. Called from category pills.
  void _onCategorySelected(String cat) {
    setState(() => _selectedCategory = cat);
    _fetchCatalog(reset: true);
  }

  /// Geocodes a free-text place query via the open Nominatim OSM API.
  ///
  /// Nominatim is free and requires no API key, but mandates a descriptive
  /// `User-Agent` header (per OSM Tile Usage Policy). We fall back to the
  /// local mock DB if the network call fails — keeps the UX responsive
  /// even offline.
  Future<void> _searchMap(String query) async {
    if (query.isEmpty) return;
    final q = query.trim();

    setState(() => _isGeocoding = true);

    LatLng? coords;
    String? displayName;

    try {
      // Geocoding via secure OMNIGO Backend Proxy
      final url = Uri.parse(ApiEndpoints.geocodingSearch(q));
      final response = await http.get(url, headers: {
        'Accept': 'application/json',
      },).timeout(const Duration(seconds: 8));

      if (response.statusCode == 200) {
        final List<dynamic> results = jsonDecode(response.body) as List<dynamic>;
        if (results.isNotEmpty) {
          final hit = results.first as Map<String, dynamic>;
          final lat = double.tryParse(hit['lat']?.toString() ?? '') ?? 0.0;
          final lon = double.tryParse(hit['lon']?.toString() ?? '') ?? 0.0;
          if (lat != 0.0 || lon != 0.0) {
            coords = LatLng(lat, lon);
            displayName = hit['display_name']?.toString();
          }
        }
      }
    } catch (e) {
      debugPrint('Nominatim geocoding failed: $e');
    }

    setState(() => _isGeocoding = false);

    if (coords == null) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
              content: Text(
                  'No location found for "$q". Try a more specific name.',),),
        );
      }
      return;
    }

    _mapController?.animateCamera(
      CameraUpdate.newLatLngZoom(coords, 14.0),
    );
    _rideDropoffLatLng = coords;

    if (mounted && displayName != null) {
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(
          content: Text('Location: $displayName'),
          duration: const Duration(seconds: 4),
          action: SnackBarAction(
            label: 'Book Ride',
            onPressed: () => _estimateAndSelectVehicle(),
          ),
        ),
      );
    }
  }

  Future<void> _estimateAndSelectVehicle() async {
    if (_rideDropoffLatLng == null) return;

    setState(() => _isEstimatingRide = true);

    // Safety timeout: auto-dismiss overlay after 20s if API hangs
    Future.delayed(const Duration(seconds: 20), () {
      if (mounted && _isEstimatingRide) {
        setState(() => _isEstimatingRide = false);
      }
    });

    LatLng pickup = _mapCenter; // fallback
    try {
      // Fetch live user GPS location
      final position = await Geolocator.getCurrentPosition(
        locationSettings: const LocationSettings(
          accuracy: LocationAccuracy.high,
          timeLimit: Duration(seconds: 5),
        ),
      );
      pickup = LatLng(position.latitude, position.longitude);
    } catch (e) {
      debugPrint('Failed to get GPS location for pickup, using default Lahore center: $e');
    }

    _ridePickupLatLng = pickup;

    // Update map markers
    setState(() {
      _mapMarkers.clear();
      _mapMarkers['pickup'] = MarkerData(
        position: _ridePickupLatLng!,
        iconSize: 1.0,
      );
      _mapMarkers['dropoff'] = MarkerData(
        position: _rideDropoffLatLng!,
        iconSize: 1.0,
      );
    });

    try {
      final response = await ApiClient().post('/ride/estimate', {
        'pickup_lat': _ridePickupLatLng!.latitude,
        'pickup_lng': _ridePickupLatLng!.longitude,
        'dropoff_lat': _rideDropoffLatLng!.latitude,
        'dropoff_lng': _rideDropoffLatLng!.longitude,
        'vehicle_type': '',
      });

      final List<dynamic> estimates = (response['estimates'] as List<dynamic>?) ?? <dynamic>[];
      final List<dynamic> geometry = (response['geometry'] as List<dynamic>?) ?? <dynamic>[];

      if (mounted) {
        setState(() {
          _isEstimatingRide = false;
          // OSRM coordinates are [lng, lat], map to LatLng(lat, lng)
          _rideRoutePolyline = geometry.map<LatLng>((c) {
            final double lng = (c[0] as num).toDouble();
            final double lat = (c[1] as num).toDouble();
            return LatLng(lat, lng);
          }).toList();
        });

        // Trigger premium selector sheet overlay
        unawaited(showModalBottomSheet<void>(
          context: context,
          isScrollControlled: true,
          backgroundColor: Colors.transparent,
          barrierColor: Colors.black54,
          builder: (context) {
            return VehicleSelectorSheet(
              estimates: estimates,
              onBookRide: (type, fare) => _bookRideConfirmation(type, fare),
            );
          },
        ),);
      }
    } catch (e) {
      setState(() => _isEstimatingRide = false);
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Fare estimation failed: $e')),
        );
      }
    }
  }

  void _bookRideConfirmation(String vehicleType, double fare) {
    _requestRide(vehicleType, fare);
  }

  Future<void> _requestRide(String vehicleType, double fare) async {
    if (_ridePickupLatLng == null || _rideDropoffLatLng == null) return;

    unawaited(showDialog<void>(
      context: context,
      barrierDismissible: false,
      builder: (context) => const Center(
        child: CircularProgressIndicator(color: Color(0xFFCAFF33)),
      ),
    ),);

    try {
      await ApiClient().post('/rides/', {
        'customer_tracking_id': widget.trackingId,
        'vehicle_type': vehicleType,
        'pickup_lat': _ridePickupLatLng!.latitude,
        'pickup_lng': _ridePickupLatLng!.longitude,
        'dropoff_lat': _rideDropoffLatLng!.latitude,
        'dropoff_lng': _rideDropoffLatLng!.longitude,
        'fare_amount': fare,
      });

      if (mounted) {
        Navigator.pop(context); // pop spinner
        _showSuccessRideDialog(vehicleType, fare);
      }
    } catch (e) {
      if (mounted) {
        Navigator.pop(context); // pop spinner
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed to request ride: $e')),
        );
      }
    }
  }

  void _showSuccessRideDialog(String vehicleType, double fare) {
    unawaited(showDialog<void>(
      context: context,
      builder: (context) => AlertDialog(
        backgroundColor: const Color(0xFF121212),
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(24),
          side: BorderSide(color: Colors.white.withOpacity(0.08)),
        ),
        title: const Row(
          children: [
            Icon(Icons.check_circle_rounded, color: Color(0xFFCAFF33), size: 28),
            SizedBox(width: 12),
            Text(
              'Ride Confirmed',
              style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontFamily: 'Outfit'),
            ),
          ],
        ),
        content: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              'Your ${vehicleType.toUpperCase()} has been requested.',
              style: const TextStyle(color: Colors.white70, fontFamily: 'Outfit'),
            ),
            const SizedBox(height: 12),
            Text(
              'Estimated Fare: PKR ${fare.toStringAsFixed(0)}',
              style: const TextStyle(color: Color(0xFFCAFF33), fontWeight: FontWeight.w900, fontSize: 16, fontFamily: 'Outfit'),
            ),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () {
              Navigator.pop(context);
              // Clear temporary route preview markers and polyline
              setState(() {
                _rideRoutePolyline.clear();
                _mapMarkers.clear();
                // Put back basic Lahore center pin
                _mapMarkers['me'] = MarkerData(
                  position: _mapCenter,
                  iconSize: 1.0,
                );
              });
            },
            child: const Text(
              'OK',
              style: TextStyle(color: Color(0xFFCAFF33), fontWeight: FontWeight.bold, fontFamily: 'Outfit'),
            ),
          ),
        ],
      ),
    ),);
  }

  Future<void> _fetchOrders() async {
    if (_isLoadingOrders) return;
    setState(() {
      _isLoadingOrders = true;
    });

    try {
      final response =
          await ApiClient().get('/orders/customer/${widget.trackingId}');
      if (mounted) {
        setState(() {
          _customerOrders = response as List<dynamic>;
          _isLoadingOrders = false;
        });

        // Connect WebSocket for any active order to receive real-time
        // status updates and live rider GPS telemetry.
        final activeStatuses = {'pending', 'paid', 'accepted', 'shipped', 'in_transit'};
        final hasActiveOrder = _customerOrders.any(
          (o) => activeStatuses.contains(o['status']?.toString() ?? ''),
        );
        if (hasActiveOrder) {
          _connectRiderTelemetry();
        }
      }
    } catch (e) {
      if (mounted) {
        setState(() {
          _isLoadingOrders = false;
        });
        debugPrint('Error fetching customer orders: $e');
      }
    }
  }

  void _showCartBottomSheet() {
    Navigator.push(context, MaterialPageRoute<void>(builder: (_) => const CartScreen()));
  }

  List<Product> _getFilteredProducts() {
    // Server-side filtering is now used — `_allProducts` already reflects
    // the current `_searchQuery` + `_selectedCategory` from the backend.
    return _allProducts;
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: AppTheme.bgColor,
      body: SafeArea(
        child: Stack(
          children: [
            IndexedStack(
              index: _currentIndex,
              children: [
                _buildHomeTab(),
                _buildCatalogTab(),
                _buildWishlistTab(),
                _buildMapTab(),
                _buildOrdersTab(),
                _buildProfileTab(),
              ],
            ),
          ],
        ),
      ),
      bottomNavigationBar: _buildBottomNavbar(),
    );
  }

  Widget _buildHomeTab() {
    return RefreshIndicator(
      color: AppTheme.limeAccent,
      backgroundColor: AppTheme.blackAccent,
      onRefresh: () => _fetchCatalog(reset: true),
      child: SingleChildScrollView(
        physics: const AlwaysScrollableScrollPhysics(),
        padding: const EdgeInsets.all(24.0),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              mainAxisAlignment: MainAxisAlignment.spaceBetween,
              children: [
                Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    const Text('Welcome to OMNIGO',
                        style: TextStyle(fontSize: 16, color: Colors.grey),),
                    Text(widget.trackingId,
                        style: const TextStyle(
                            fontSize: 14,
                            fontWeight: FontWeight.bold,
                            color: AppTheme.blackAccent,),),
                  ],
                ),
                Container(
                  padding: const EdgeInsets.all(10),
                  decoration: const BoxDecoration(
                      color: AppTheme.blackAccent, shape: BoxShape.circle,),
                  child: const ChatNavButton(
                    iconColor: Colors.white,
                  ),
                ),
              ],
            ),
            const SizedBox(height: 30),
            Container(
              width: double.infinity,
              padding: const EdgeInsets.all(24),
              decoration: BoxDecoration(
                color: AppTheme.limeAccent,
                borderRadius: BorderRadius.circular(30),
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Text('Summer Sale',
                      style: TextStyle(
                          fontSize: 24,
                          fontWeight: FontWeight.w900,
                          color: AppTheme.blackAccent,),),
                  const SizedBox(height: 8),
                  const Text('Up to 50% off on premium items',
                      style:
                          TextStyle(fontSize: 14, color: AppTheme.blackAccent),),
                  const SizedBox(height: 20),
                  ElevatedButton(
                    onPressed: () =>
                        setState(() => _currentIndex = 1), // Go to catalog tab
                    style: ElevatedButton.styleFrom(
                      backgroundColor: AppTheme.blackAccent,
                      foregroundColor: Colors.white,
                      elevation: 0,
                    ),
                    child: const Text('Shop Now'),
                  ),
                ],
              ),
            ),
            const SizedBox(height: 30),
            const Text('Top Categories',
                style: TextStyle(
                    fontSize: 20,
                    fontWeight: FontWeight.bold,
                    color: AppTheme.blackAccent,),),
            const SizedBox(height: 16),
            SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              child: Row(
                children:
                    ['Shoes', 'Apparel', 'Electronics', 'Accessories'].map((cat) {
                  return GestureDetector(
                    onTap: () {
                      setState(() {
                        _selectedCategory = cat;
                        _currentIndex = 1;
                      });
                    },
                    child: Container(
                      margin: const EdgeInsets.only(right: 16),
                      width: 100,
                      height: 100,
                      decoration: BoxDecoration(
                        color: Colors.white,
                        borderRadius: BorderRadius.circular(20),
                        boxShadow: [
                          BoxShadow(
                              color: Colors.black.withOpacity(0.02),
                              blurRadius: 10,
                              offset: const Offset(0, 5),),
                        ],
                      ),
                      child: Center(
                        child: Column(
                          mainAxisAlignment: MainAxisAlignment.center,
                          children: [
                            Icon(
                              cat == 'Shoes'
                                  ? Icons.snowshoeing
                                  : cat == 'Electronics'
                                      ? Icons.devices
                                      : Icons.category,
                              color: AppTheme.blackAccent,
                              size: 30,
                            ),
                            const SizedBox(height: 8),
                            Text(cat,
                                style: const TextStyle(
                                    fontWeight: FontWeight.bold,
                                    fontSize: 13,
                                    color: AppTheme.blackAccent,),),
                          ],
                        ),
                      ),
                    ),
                  );
                }).toList(),
              ),
            ),
            const SizedBox(height: 30),
            Row(
              mainAxisAlignment: MainAxisAlignment.spaceBetween,
              children: [
                const Text('Featured Products',
                    style: TextStyle(
                        fontSize: 20,
                        fontWeight: FontWeight.bold,
                        color: AppTheme.blackAccent,),),
                GestureDetector(
                  onTap: () => setState(() => _currentIndex = 1),
                  child: const Text('See All',
                      style: TextStyle(
                          fontSize: 14,
                          fontWeight: FontWeight.bold,
                          color: Colors.blue,),),
                ),
              ],
            ),
            const SizedBox(height: 16),
            if (_allProducts.isNotEmpty)
              GridView.builder(
                shrinkWrap: true,
                physics: const NeverScrollableScrollPhysics(),
                itemCount: _allProducts.length > 8 ? 8 : _allProducts.length,
                gridDelegate: const SliverGridDelegateWithFixedCrossAxisCount(
                  crossAxisCount: 2,
                  crossAxisSpacing: 16,
                  mainAxisSpacing: 16,
                  childAspectRatio: 0.75,
                ),
                itemBuilder: (context, idx) {
                  return _buildProductCard(_allProducts[idx]);
                },
              )
            else if (_isLoadingCatalog)
              const Center(
                child: Padding(
                  padding: EdgeInsets.all(20.0),
                  child: CircularProgressIndicator(color: AppTheme.limeAccent),
                ),
              )
            else
              const Center(
                child: Padding(
                  padding: EdgeInsets.all(20.0),
                  child: Text('Featured products currently unavailable.',
                      style: TextStyle(color: Colors.grey),),
                ),
              ),
          ],
        ),
      ),
    );
  }

  Widget _buildCatalogTab() {
    final products = _getFilteredProducts();

    return RefreshIndicator(
      color: AppTheme.limeAccent,
      backgroundColor: AppTheme.blackAccent,
      onRefresh: () => _fetchCatalog(reset: true),
      child: SingleChildScrollView(
        physics: const AlwaysScrollableScrollPhysics(),
        controller: _scrollController,
        padding: const EdgeInsets.all(24.0),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Text('OMNIGO E-Shop',
                      style: TextStyle(fontSize: 16, color: Colors.grey),),
                  Text(widget.trackingId,
                      style: const TextStyle(
                          fontSize: 14,
                          fontWeight: FontWeight.bold,
                          color: AppTheme.blackAccent,),),
                ],
              ),
              GestureDetector(
                onTap: _showCartBottomSheet,
                child: Stack(
                  alignment: Alignment.topRight,
                  children: [
                    Container(
                      padding: const EdgeInsets.all(10),
                      decoration: const BoxDecoration(
                          color: AppTheme.blackAccent, shape: BoxShape.circle,),
                      child: const Icon(Icons.shopping_cart_outlined,
                          color: Colors.white, size: 20,),
                    ),
                    Consumer<CartProvider>(
                      builder: (context, cart, child) {
                        if (cart.itemCount == 0) return const SizedBox.shrink();
                        return Container(
                          padding: const EdgeInsets.all(4),
                          decoration: const BoxDecoration(
                              color: Colors.redAccent, shape: BoxShape.circle,),
                          constraints:
                              const BoxConstraints(minWidth: 16, minHeight: 16),
                          child: Text(
                            '${cart.itemCount}',
                            style: const TextStyle(
                                color: Colors.white,
                                fontSize: 9,
                                fontWeight: FontWeight.bold,),
                            textAlign: TextAlign.center,
                          ),
                        );
                      },
                    ),
                  ],
                ),
              ),
            ],
          ),
          const SizedBox(height: 24),
          Container(
            padding: const EdgeInsets.symmetric(horizontal: 16),
            decoration: BoxDecoration(
              color: Colors.white,
              borderRadius: BorderRadius.circular(20),
              boxShadow: [
                BoxShadow(
                    color: Colors.black.withOpacity(0.02),
                    blurRadius: 10,
                    offset: const Offset(0, 5),),
              ],
            ),
            child: TextField(
              controller: _searchController,
              onChanged: _onSearchChanged,
              decoration: const InputDecoration(
                hintText: 'Search products and brands...',
                prefixIcon: Icon(Icons.search, color: Colors.grey),
                border: InputBorder.none,
                enabledBorder: InputBorder.none,
                focusedBorder: InputBorder.none,
              ),
            ),
          ),
          const SizedBox(height: 20),
          SingleChildScrollView(
            scrollDirection: Axis.horizontal,
            child: Row(
              children: ['All', 'Shoes', 'Apparel', 'Electronics'].map((cat) {
                final isSelected = _selectedCategory == cat;
                return GestureDetector(
                  onTap: () => _onCategorySelected(cat),
                  child: Container(
                    margin: const EdgeInsets.only(right: 12),
                    padding: const EdgeInsets.symmetric(
                        horizontal: 20, vertical: 10,),
                    decoration: BoxDecoration(
                      color: isSelected ? AppTheme.blackAccent : Colors.white,
                      borderRadius: BorderRadius.circular(20),
                      border: Border.all(color: Colors.grey.shade200),
                    ),
                    child: Text(
                      cat,
                      style: TextStyle(
                        fontWeight: FontWeight.bold,
                        color: isSelected ? Colors.white : AppTheme.blackAccent,
                        fontSize: 13,
                      ),
                    ),
                  ),
                );
              }).toList(),
            ),
          ),
          const SizedBox(height: 24),
          Text(
            '$_selectedCategory Catalog (${products.length})',
            style: const TextStyle(
                fontSize: 20,
                fontWeight: FontWeight.bold,
                color: AppTheme.blackAccent,),
          ),
          const SizedBox(height: 16),
          if (products.isEmpty && !_isLoadingCatalog)
            const Center(
              child: Padding(
                padding: EdgeInsets.symmetric(vertical: 40.0),
                child: Text('No products found.',
                    style: TextStyle(color: Colors.grey),),
              ),
            )
          else
            GridView.builder(
              shrinkWrap: true,
              physics: const NeverScrollableScrollPhysics(),
              itemCount: products.length,
              gridDelegate: const SliverGridDelegateWithFixedCrossAxisCount(
                crossAxisCount: 2,
                crossAxisSpacing: 16,
                mainAxisSpacing: 16,
                childAspectRatio: 0.75,
              ),
              itemBuilder: (context, idx) {
                return _buildProductCard(products[idx]);
              },
            ),
          if (_isLoadingCatalog)
            const Center(
              child: Padding(
                padding: EdgeInsets.all(20),
                child: CircularProgressIndicator(color: AppTheme.limeAccent),
              ),
            ),
        ],
      ),
    ),
  );
}

  Widget _buildProductCard(Product p) {
    final String name = p.name.isNotEmpty ? p.name : 'Unknown';
    final double price = p.basePrice;
    final String storeId = p.storeTrackingId;
    final String prodId = p.productTrackingId.isNotEmpty ? p.productTrackingId : 'PROD-N/A';
    final bool isFavorited = _favoriteProductIds.contains(prodId);

    return GestureDetector(
      onTap: () {
        Navigator.push(
          context,
          MaterialPageRoute<void>(
            builder: (context) => ProductDetailsScreen(
              product: p,
              userTrackingId: widget.trackingId,
            ),
          ),
        );
      },
      child: Container(
        decoration: BoxDecoration(
          color: Colors.white,
          borderRadius: BorderRadius.circular(24),
          boxShadow: [
            BoxShadow(
                color: Colors.black.withOpacity(0.02),
                blurRadius: 10,
                offset: const Offset(0, 5),),
          ],
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Expanded(
              child: Stack(
                children: [
                  Container(
                    width: double.infinity,
                    decoration: const BoxDecoration(
                      color: AppTheme.softBlue,
                      borderRadius: BorderRadius.vertical(top: Radius.circular(24)),
                    ),
                    child: ClipRRect(
                      borderRadius: const BorderRadius.vertical(top: Radius.circular(24)),
                      child: (p.imageUrl != null && p.imageUrl!.isNotEmpty)
                          ? Image.network(
                              p.imageUrl!,
                              fit: BoxFit.cover,
                              errorBuilder: (context, error, stackTrace) =>
                                  const Center(child: Icon(Icons.shopping_bag_outlined, size: 40, color: AppTheme.blackAccent)),
                            )
                          : const Center(child: Icon(Icons.shopping_bag_outlined, size: 40, color: AppTheme.blackAccent)),
                    ),
                  ),
                  // Favorite heart toggle
                  Positioned(
                    top: 8,
                    right: 8,
                    child: GestureDetector(
                      onTap: () => _toggleFavorite(prodId),
                      child: Container(
                        padding: const EdgeInsets.all(6),
                        decoration: BoxDecoration(
                          color: Colors.white.withOpacity(0.9),
                          shape: BoxShape.circle,
                        ),
                        child: Icon(
                          isFavorited ? Icons.favorite : Icons.favorite_border_rounded,
                          color: isFavorited ? Colors.redAccent : Colors.grey,
                          size: 18,
                        ),
                      ),
                    ),
                  ),
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.all(12.0),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(name,
                      style: const TextStyle(
                          fontWeight: FontWeight.bold,
                          color: AppTheme.blackAccent,
                          fontSize: 15,),
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,),
                  const SizedBox(height: 4),
                  Text(storeId,
                      style: const TextStyle(color: Colors.grey, fontSize: 11),),
                  const SizedBox(height: 8),
                  Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    children: [
                      Text('PKR ${price.toStringAsFixed(2)}',
                          style: const TextStyle(
                              fontWeight: FontWeight.w800,
                              color: AppTheme.blackAccent,),),
                      GestureDetector(
                        onTap: () {
                          context.read<CartProvider>().addItem(p);
                          ScaffoldMessenger.of(context).showSnackBar(
                            SnackBar(
                              content: Text('Added $name to cart!'),
                              backgroundColor: Colors.green,
                              behavior: SnackBarBehavior.floating,
                              duration: const Duration(milliseconds: 800),
                            ),
                          );
                        },
                        child: Container(
                          padding: const EdgeInsets.all(8),
                          decoration: const BoxDecoration(
                              color: AppTheme.blackAccent,
                              shape: BoxShape.circle,),
                          child: const Icon(Icons.add_shopping_cart_outlined,
                              color: AppTheme.limeAccent, size: 14,),
                        ),
                      ),
                    ],
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildMapTab() {
    return Stack(
      children: [
        // ── Live Rider Tracking Layer ────────────────────────────
        // Rebuilds ONLY when rider coordinates change, leaving the
        // base map tiles untouched (no global repaint).
        ValueListenableBuilder<Map<String, LatLng>>(
          valueListenable: _riderMarkers,
          builder: (context, riderCoords, _) {
            // Merge the live rider positions into the static marker
            // map keyed by "rider:<id>" so the MapLibre widget can
            // reconcile symbols efficiently via didUpdateWidget.
            final merged = Map<String, MarkerData>.from(_mapMarkers);
            riderCoords.forEach((id, pos) {
              merged['rider:$id'] = MarkerData(
                position: pos,
                iconSize: 1.0,
              );
            });

            return MapLibreMapWidget(
              initialCenter: _mapCenter,
              initialZoom: 13.0,
              markers: merged,
              polylines: _rideRoutePolyline.isNotEmpty
                  ? [_rideRoutePolyline]
                  : const [],
              onMapCreated: (controller) {
                _mapController = controller;
              },
            );
          },
        ),
        Positioned(
          top: 0,
          left: 0,
          right: 0,
          child: SafeArea(
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 8),
              child: Container(
                padding: const EdgeInsets.symmetric(horizontal: 16),
                decoration: BoxDecoration(
                  color: Colors.white,
                  borderRadius: BorderRadius.circular(30),
                  boxShadow: const [
                    BoxShadow(color: Colors.black12, blurRadius: 10),
                  ],
                ),
                child: Row(
                  children: [
                    const Icon(Icons.location_on, color: AppTheme.blackAccent),
                    const SizedBox(width: 10),
                    Expanded(
                      child: TextField(
                    controller: _mapSearchController,
                    textInputAction: TextInputAction.search,
                    onSubmitted: (val) => _searchMap(val),
                    decoration: const InputDecoration(
                      hintText: 'Search city (Lahore, Karachi, London...)',
                      border: InputBorder.none,
                      enabledBorder: InputBorder.none,
                      focusedBorder: InputBorder.none,
                    ),
                  ),
                ),
                IconButton(
                  icon: _isGeocoding
                      ? const SizedBox(
                          width: 20,
                          height: 20,
                          child: CircularProgressIndicator(
                              strokeWidth: 2, color: AppTheme.blackAccent,),
                        )
                      : const Icon(Icons.search, color: AppTheme.blackAccent),
                  onPressed: _isGeocoding
                      ? null
                      : () => _searchMap(_mapSearchController.text),
                ),
              ],
            ),
          ),
        ),
      ),
      ),
        // Live rider tracking status banner
        Positioned(
          bottom: 0,
          left: 0,
          right: 0,
          child: SafeArea(
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 8),
              child: ValueListenableBuilder<Map<String, LatLng>>(
                valueListenable: _riderMarkers,
                builder: (context, riderCoords, _) {
                  if (riderCoords.isEmpty) return const SizedBox.shrink();
                  return Container(
                    padding: const EdgeInsets.all(12),
                    decoration: BoxDecoration(
                      color: Colors.black.withOpacity(0.85),
                      borderRadius: BorderRadius.circular(16),
                    ),
                    child: Row(
                      children: [
                    const Icon(Icons.radar_rounded,
                        color: Color(0xFFCAFF33), size: 18,),
                    const SizedBox(width: 10),
                    Expanded(
                      child: Text(
                        'Live rider tracking: ${riderCoords.length} ${riderCoords.length == 1 ? "rider" : "riders"} on the move',
                        style: const TextStyle(
                            color: Colors.white,
                            fontSize: 12,
                            fontWeight: FontWeight.bold,),
                      ),
                    ),
                  ],
                ),
              );
            },
          ),
        ),
      ),
    ),
        if (_isEstimatingRide)
          Positioned.fill(
            child: Container(
              color: Colors.black.withOpacity(0.5),
              child: const Center(
                child: CircularProgressIndicator(
                  color: Color(0xFFCAFF33),
                ),
              ),
            ),
          ),
      ],
    );
  }

  Widget _buildOrdersTab() {
    return Padding(
      padding: const EdgeInsets.all(24.0),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const Text('Track Orders',
              style: TextStyle(
                  fontSize: 24,
                  fontWeight: FontWeight.bold,
                  color: AppTheme.blackAccent,),),
          const SizedBox(height: 20),
          Expanded(
            child: _isLoadingOrders
                ? const Center(
                    child:
                        CircularProgressIndicator(color: AppTheme.limeAccent),)
                : _customerOrders.isEmpty
                    ? const Center(
                        child: Text('No orders placed yet.',
                            style: TextStyle(color: Colors.grey, fontSize: 16),),)
                    : RefreshIndicator(
                        onRefresh: _fetchOrders,
                        child: ListView.builder(
                          itemCount: _customerOrders.length,
                          itemBuilder: (context, index) {
                            final order = _customerOrders[index];
                            final id =
                                order['order_tracking_id']?.toString() ?? 'ORD-UNKNOWN';
                            final store =
                                order['store_tracking_id']?.toString() ?? 'STOR-UNKNOWN';
                            final total =
                                (order['total_amount'] ?? 0.0).toString();
                            final currency = order['currency']?.toString() ?? 'USD';
                            final status = order['status']?.toString() ?? 'pending';
                            final activeStatuses = {'pending', 'paid', 'accepted', 'processing', 'shipped', 'in_transit'};
                            final isActive = activeStatuses.contains(status);
                            final isCancelled = status == 'cancelled' || status == 'failed' || status == 'payment_failed';
                            return GestureDetector(
                              onTap: () {
                                Navigator.push(
                                  context,
                                  MaterialPageRoute<void>(
                                    builder: (_) =>
                                        OrderDetailScreen(order: order as Map<String, dynamic>),
                                  ),
                                );
                              },
                              child: _buildTrackingItem(
                                'Order #$id',
                                'Total: $currency $total from $store',
                                store,
                                status.toUpperCase(),
                                isActive,
                                isCancelled: isCancelled,
                              ),
                            );
                          },
                        ),
                      ),
          ),
        ],
      ),
    );
  }

  Widget _buildTrackingItem(
      String id, String desc, String storeId, String status, bool isActive, {bool isCancelled = false,}) {
    return Container(
      margin: const EdgeInsets.only(bottom: 16),
      padding: const EdgeInsets.all(20),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(20),
        boxShadow: [
          BoxShadow(
              color: Colors.black.withOpacity(0.01),
              blurRadius: 10,
              offset: const Offset(0, 5),),
        ],
      ),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceBetween,
        children: [
          Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(id,
                  style: const TextStyle(
                      fontWeight: FontWeight.bold,
                      color: AppTheme.blackAccent,),),
              const SizedBox(height: 4),
              Text(desc,
                  style: const TextStyle(color: Colors.grey, fontSize: 13),),
            ],
          ),
          Container(
            padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
            decoration: BoxDecoration(
              color: isCancelled ? Colors.red.shade100 : (isActive ? AppTheme.limeAccent : Colors.grey.shade200),
              borderRadius: BorderRadius.circular(20),
            ),
            child: Text(
              status,
              style: TextStyle(
                fontWeight: FontWeight.bold,
                color: isActive ? AppTheme.blackAccent : Colors.grey.shade600,
                fontSize: 12,
              ),
            ),
          ),
        ],
      ),
    );
  }

  Widget _buildProfileTab() {
    return SingleChildScrollView(
      padding: const EdgeInsets.all(24.0),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const Text('Customer Profile',
              style: TextStyle(
                  fontSize: 26,
                  fontWeight: FontWeight.bold,
                  color: AppTheme.blackAccent,),),
          const SizedBox(height: 24),
          Container(
            padding: const EdgeInsets.all(24),
            decoration: BoxDecoration(
              color: AppTheme.softPink,
              borderRadius: BorderRadius.circular(30),
            ),
            child: Row(
              children: [
                const CircleAvatar(
                  radius: 35,
                  backgroundColor: AppTheme.blackAccent,
                  child: Icon(Icons.person, size: 40, color: Colors.white),
                ),
                const SizedBox(width: 20),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(SessionRegistry.instance.fullName ?? 'Unknown User',
                          style: const TextStyle(
                              fontSize: 20,
                              fontWeight: FontWeight.bold,
                              color: AppTheme.blackAccent,),),
                      const SizedBox(height: 4),
                      Text(widget.trackingId,
                          style: const TextStyle(
                              fontSize: 14,
                              fontWeight: FontWeight.bold,
                              color: Colors.grey,),),
                    ],
                  ),
                ),
              ],
            ),
          ),
          const SizedBox(height: 30),
          const Text('Account Information',
              style: TextStyle(
                  fontSize: 18,
                  fontWeight: FontWeight.bold,
                  color: AppTheme.blackAccent,),),
          const SizedBox(height: 16),
          _buildInfoRow(
              'Email Address',
              SessionRegistry.instance.email ?? 'Not provided',
              Icons.email_outlined,),
          _buildInfoRow(
              'Phone Number',
              SessionRegistry.instance.phone?.isNotEmpty == true
                  ? SessionRegistry.instance.phone!
                  : 'Not provided',
              Icons.phone_outlined,),
          _buildInfoRow(
              'Default Address',
              SessionRegistry.instance.address?.isNotEmpty == true
                  ? SessionRegistry.instance.address!
                  : 'Not provided',
              Icons.home_outlined,),
          const SizedBox(height: 30),
          const Text('Payment Methods',
              style: TextStyle(
                  fontSize: 18,
                  fontWeight: FontWeight.bold,
                  color: AppTheme.blackAccent,),),
          const SizedBox(height: 16),
          // Payment method cards — wired to Stripe integration status.
          // When STRIPE_API_KEY is set on the backend, the checkout flow
          // creates a real Payment Intent. Until then, show a clear
          // "Add Payment Method" call-to-action.
          _buildPaymentCard(
            'Stripe Credit / Debit',
            'Tap to add a card',
            Colors.blue.shade100,
            Icons.credit_card,
            onTap: () {
              ScaffoldMessenger.of(context).showSnackBar(
                const SnackBar(
                  content: Text('Stripe payment integration is ready on the backend. Add flutter_stripe package to enable card entry.'),
                  behavior: SnackBarBehavior.floating,
                ),
              );
            },
          ),
          _buildPaymentCard(
            'JazzCash / EasyPaisa',
            'Tap to load your mobile wallet',
            Colors.amber.shade100,
            Icons.account_balance_wallet_outlined,
            onTap: () {
              Navigator.push(
                context,
                MaterialPageRoute<void>(
                  builder: (_) => CustomerWalletScreen(
                    trackingId: widget.trackingId,
                  ),
                ),
              );
            },
          ),
          const SizedBox(height: 30),
          // Edit Profile button
          ElevatedButton.icon(
            onPressed: () async {
              final result = await Navigator.push<bool>(
                context,
                MaterialPageRoute(builder: (_) => const EditProfileScreen()),
              );
              // Refresh the profile tab display after an edit.
              if (result == true && mounted) setState(() {});
            },
            icon: const Icon(Icons.edit_outlined, color: Colors.white),
            label: const Text('Edit Profile', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 16)),
            style: ElevatedButton.styleFrom(
              backgroundColor: AppTheme.blackAccent,
              foregroundColor: Colors.white,
              minimumSize: const Size(double.infinity, 54),
              shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
            ),
          ),
          const SizedBox(height: 16),
          ElevatedButton(
            onPressed: () async {
              // Clear ALL session data (memory + SharedPreferences) before navigating
              try {
                await SessionRegistry.instance.logout();
              } finally {
                if (mounted) {
                  unawaited(Navigator.pushNamedAndRemoveUntil(context, '/', (route) => false));
                }
              }
            },
            style: ElevatedButton.styleFrom(
              backgroundColor: Colors.redAccent,
              foregroundColor: Colors.white,
              minimumSize: const Size(double.infinity, 54),
            ),
            child: const Text('Log Out',
                style: TextStyle(fontWeight: FontWeight.bold, fontSize: 16),),
          ),
          const SizedBox(height: 20),
        ],
      ),
    );
  }

  Widget _buildInfoRow(String title, String value, IconData icon) {
    return Container(
      margin: const EdgeInsets.only(bottom: 12),
      padding: const EdgeInsets.all(16),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(20),
        border: Border.all(color: Colors.grey.shade100),
      ),
      child: Row(
        children: [
          Icon(icon, color: Colors.grey),
          const SizedBox(width: 16),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(title,
                    style: const TextStyle(fontSize: 12, color: Colors.grey),),
                const SizedBox(height: 2),
                Text(value,
                    style: const TextStyle(
                        fontSize: 14,
                        fontWeight: FontWeight.bold,
                        color: AppTheme.blackAccent,),),
              ],
            ),
          ),
        ],
      ),
    );
  }

  Widget _buildPaymentCard(
      String gateway, String detail, Color color, IconData icon,
      {VoidCallback? onTap,}) {
    return GestureDetector(
      onTap: onTap,
      child: Container(
        margin: const EdgeInsets.only(bottom: 12),
        padding: const EdgeInsets.all(16),
        decoration: BoxDecoration(
          color: color,
          borderRadius: BorderRadius.circular(20),
        ),
        child: Row(
          children: [
            Icon(icon, color: AppTheme.blackAccent),
            const SizedBox(width: 16),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(gateway,
                      style: const TextStyle(
                          fontSize: 14,
                          fontWeight: FontWeight.bold,
                          color: AppTheme.blackAccent,),),
                  const SizedBox(height: 2),
                  Text(detail,
                      style: const TextStyle(
                          fontSize: 12, color: AppTheme.blackAccent,),),
                ],
              ),
            ),
            Icon(onTap != null ? Icons.add_circle_outline : Icons.lock_outline,
                color: AppTheme.blackAccent.withOpacity(0.5),),
          ],
        ),
      ),
    );
  }

  Widget _buildWishlistTab() {
    return WishlistScreen(
      customerTrackingId: widget.trackingId,
      onNavigateToCatalog: (index) => setState(() => _currentIndex = index),
    );
  }

  Widget _buildBottomNavbar() {
    return Container(
      margin: const EdgeInsets.symmetric(horizontal: 30, vertical: 16),
      padding: const EdgeInsets.symmetric(vertical: 14, horizontal: 20),
      decoration: BoxDecoration(
        color: AppTheme.blackAccent.withOpacity(0.95),
        borderRadius: BorderRadius.circular(40),
        boxShadow: const [
          BoxShadow(color: Colors.black26, blurRadius: 10, offset: Offset(0, 5)),
        ],
      ),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceAround,
        children: [
          _buildNavItem(Icons.home_filled, 0),
          _buildNavItem(Icons.search, 1),
          _buildNavItem(Icons.favorite_outline_rounded, 2),
          _buildNavItem(Icons.map_outlined, 3),
          _buildNavItem(Icons.local_shipping_outlined, 4),
          _buildNavItem(Icons.person_outline, 5),
        ],
      ),
    );
  }

  Widget _buildNavItem(IconData icon, int index) {
    final isSelected = _currentIndex == index;
    return GestureDetector(
      onTap: () => setState(() => _currentIndex = index),
      child: Icon(
        icon,
        color: isSelected ? AppTheme.limeAccent : Colors.white.withOpacity(0.5),
        size: 28,
      ),
    );
  }
}
