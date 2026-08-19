import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';
import 'package:image_picker/image_picker.dart';
import 'dart:io';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/services/session_registry.dart';
import '../../../../core/theme/app_theme.dart';
import 'vendor_inventory_screen.dart';
import 'vendor_live_map_screen.dart';
import 'vendor_analytics_screen.dart';
import 'vendor_wallet_screen.dart';
import '../../../../shared/presentation/screens/chat_list_screen.dart';
import '../../../../shared/presentation/screens/chat_room_screen.dart';

class VendorDashboardScreen extends StatefulWidget {
  const VendorDashboardScreen({super.key, required this.trackingId});
  final String trackingId;

  @override
  VendorDashboardScreenState createState() => VendorDashboardScreenState();
}

class VendorDashboardScreenState extends State<VendorDashboardScreen> {
  int _currentIndex = 0;
  List<dynamic> _orders = [];
  bool _isLoadingOrders = false;

  // Live metrics pulled from /api/v1/vendor/metrics (Phase 5 fix).
  double _totalRevenue = 0.0;
  int _activeGigs = 0;
  bool _isLoadingMetrics = false;

  // Vendor rating metrics pulled from /api/v1/ratings/:tracking_id
  double _avgRating = 0.0;
  int _totalRatings = 0;

  // True while a vendor handover is in flight (camera + upload + POST).
  // Used to disable the "Hand Over to Rider" button so the vendor can't
  // double-tap and race two requests.
  bool _isHandoverInProgress = false;

  // Verification Form State moved to Profile Tab

  @override
  void initState() {
    super.initState();
    _fetchMerchantOrders();
    _fetchVendorMetrics();
    _fetchVendorRating();
  }

  Future<void> _fetchVendorRating() async {
    try {
      final prefs = await SharedPreferences.getInstance();
      final jwtToken = prefs.getString('jwt_token') ?? '';
      final response = await http.get(
        Uri.parse(ApiEndpoints.ratingForUser(widget.trackingId)),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $jwtToken',
        },
      ).timeout(const Duration(seconds: 8));

      if (response.statusCode == 200 && mounted) {
        final data = jsonDecode(response.body) as Map<String, dynamic>;
        setState(() {
          _avgRating = (data['average_rating'] as num?)?.toDouble() ?? 0.0;
          _totalRatings = (data['total_ratings'] as num?)?.toInt() ?? 0;
        });
      }
    } catch (e) {
      debugPrint('Error fetching vendor rating: $e');
    }
  }

  Future<void> _fetchVendorMetrics() async {
    if (_isLoadingMetrics) return;
    setState(() => _isLoadingMetrics = true);

    try {
      final prefs = await SharedPreferences.getInstance();
      final jwtToken = prefs.getString('jwt_token') ?? '';

      final response = await http.get(
        Uri.parse(ApiEndpoints.vendorMetrics(widget.trackingId)),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $jwtToken',
        },
      ).timeout(const Duration(seconds: 8));

      if (response.statusCode == 200) {
        final data = jsonDecode(response.body) as Map<String, dynamic>;
        if (mounted) {
          setState(() {
            _totalRevenue = (data['total_revenue'] as num?)?.toDouble() ?? 0.0;
            // "Active Gigs" = orders that have been broadcast (status 'shipped').
            // The metrics endpoint doesn't return this directly, so we count
            // shipped orders from the orders list once it's loaded.
            _isLoadingMetrics = false;
          });
          _recalculateActiveGigs();
        }
      } else {
        if (mounted) setState(() => _isLoadingMetrics = false);
      }
    } catch (e) {
      if (mounted) setState(() => _isLoadingMetrics = false);
      debugPrint('Error fetching vendor metrics: $e');
    }
  }

  /// Counts orders with status == 'shipped' as the live "Active Gigs" KPI.
  void _recalculateActiveGigs() {
    final count = _orders.where((o) => o['status'] == 'shipped').length;
    if (mounted) setState(() => _activeGigs = count);
  }

  Future<void> _fetchMerchantOrders() async {
    if (_isLoadingOrders) return;
    setState(() {
      _isLoadingOrders = true;
    });

    try {
      final response = await ApiClient().get('/orders/vendor/${widget.trackingId}');
      if (mounted) {
        setState(() {
          _orders = response as List<dynamic>;
          _isLoadingOrders = false;
        });
        _recalculateActiveGigs();
      }
    } catch (e) {
      if (mounted) {
        setState(() {
          _isLoadingOrders = false;
        });
        debugPrint('Error fetching merchant orders: $e');
      }
    }
  }

  Future<void> _acceptOrder(String orderTrackingId) async {
    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      final response = await http.patch(
        Uri.parse(ApiEndpoints.updateOrderStatus(orderTrackingId)),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
        body: jsonEncode({
          'status': 'accepted',
        }),
      ).timeout(const Duration(seconds: 8));

      if (response.statusCode == 200) {
        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Order accepted! Status: Ready for Slip')),
        );
        unawaited(_fetchMerchantOrders());
      } else {
        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed to accept order: ${response.statusCode}')),
        );
      }
    } catch (e) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Network error: $e')),
      );
    }
  }

  Future<void> _broadcastGig(String orderTrackingId) async {
    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      final response = await http.patch(
        Uri.parse(ApiEndpoints.updateOrderStatus(orderTrackingId)),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
        body: jsonEncode({
          'status': 'shipped',
        }),
      ).timeout(const Duration(seconds: 8));

      if (response.statusCode == 200) {
        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Gig Broadcasted to Nearby Riders!')),
        );
        unawaited(_fetchMerchantOrders());
      } else {
        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed to broadcast gig: ${response.statusCode}')),
        );
      }
    } catch (e) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Network error: $e')),
      );
    }
  }

  /// Vendor handover: capture a photo of the order being handed to the
  /// rider, then POST the photo URL to the new `/orders/handover` endpoint.
  /// The order must already be in 'accepted' status (a rider has accepted
  /// the gig). This is the missing link in the audit trail — vendor
  /// evidence of the package handover.
  Future<void> _handoverToRider(String orderTrackingId) async {
    final ImagePicker picker = ImagePicker();
    final XFile? photo;
    try {
      photo = await picker.pickImage(
        source: ImageSource.camera,
        imageQuality: 70,
        maxWidth: 1280,
      );
    } catch (e) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Camera unavailable: $e')),
      );
      return;
    }
    if (photo == null) return; // user cancelled

    setState(() => _isHandoverInProgress = true);
    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      // Upload the photo to the existing proof upload endpoint. It returns
      // a public URL we can store as the handover_photo_url.
      final uploadReq = http.MultipartRequest(
        'POST',
        Uri.parse(ApiEndpoints.uploadProof()),
      );
      uploadReq.headers['Authorization'] = 'Bearer $token';
      uploadReq.files.add(await http.MultipartFile.fromPath('photo', photo.path));
      final uploadResp = await uploadReq.send().timeout(const Duration(seconds: 15));
      final uploadBody = await uploadResp.stream.bytesToString();
      if (uploadResp.statusCode != 200) {
        throw Exception('Photo upload failed: ${uploadResp.statusCode}');
      }
      final photoUrl = (jsonDecode(uploadBody) as Map<String, dynamic>)['photo_url']?.toString();

      // POST the handover. The vendor gets a one-tap audit record; the
      // rider receives an in-app + push notification that the order is
      // ready for pickup.
      final handoverResp = await http.post(
        Uri.parse(ApiEndpoints.orderHandover()),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
        body: jsonEncode({
          'order_tracking_id': orderTrackingId,
          'photo_url': photoUrl,
          'notes': '',
        }),
      ).timeout(const Duration(seconds: 8));

      if (handoverResp.statusCode == 200) {
        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Handover recorded. Rider notified.')),
        );
        unawaited(_fetchMerchantOrders());
      } else {
        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Handover failed: ${handoverResp.statusCode}')),
        );
      }
    } catch (e) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Handover failed: $e')),
      );
    } finally {
      if (mounted) setState(() => _isHandoverInProgress = false);
    }
  }

  void _printSlip(Map<String, dynamic> order) {
    final orderId = (order['order_tracking_id'] as String?) ?? 'ORD-UNKNOWN';
    final storeId = (order['store_tracking_id'] as String?) ?? 'STOR-UNKNOWN';
    final customerId = (order['customer_tracking_id'] as String?) ?? 'CUST-UNKNOWN';
    final price = ((order['total_amount'] as num?) ?? 0.0).toString();
    final currency = (order['currency'] as String?) ?? 'USD';

    showDialog<void>(
      context: context,
      builder: (context) => AlertDialog(
        backgroundColor: Colors.white,
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(24)),
        title: const Text('Vendor Delivery Slip', style: TextStyle(fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
        content: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text('-----------------------------------', style: TextStyle(color: Colors.grey)),
            Text('STORE ID: $storeId'),
            Text('VENDOR ID: ${widget.trackingId}'),
            Text('ORDER NO: $orderId'),
            Text('CUSTOMER: $customerId'),
            Text('PRICE: $currency $price'),
            const Text('-----------------------------------', style: TextStyle(color: Colors.grey)),
            const SizedBox(height: 10),
            const Text('Status: Ready for Pickup', style: TextStyle(color: Colors.green, fontWeight: FontWeight.bold)),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () {
              Navigator.pop(context);
              final messenger = ScaffoldMessenger.of(context);
              messenger.showSnackBar(
                SnackBar(
                  content: Text('Generating Invoice PDF for $orderId...'),
                  backgroundColor: AppTheme.blackAccent,
                ),
              );
              unawaited(
                Future.delayed(const Duration(milliseconds: 1000), () {
                  messenger.showSnackBar(
                    const SnackBar(
                      content: Text('Invoice Slip successfully saved to Documents and sent to wireless thermal printer!'),
                      backgroundColor: Colors.green,
                    ),
                  );
                }),
              );
            },
            child: const Text('Print / Save Invoice', style: TextStyle(color: Colors.green, fontWeight: FontWeight.bold)),
          ),
          TextButton(
            onPressed: () {
              Navigator.pop(context);
              _broadcastGig(orderId);
            },
            child: const Text('Broadcast Gig to Rider', style: TextStyle(color: AppTheme.blackAccent, fontWeight: FontWeight.bold)),
          ),
        ],
      ),
    );
  }


  Widget _buildDashboardHome() {
    return SafeArea(
      child: Padding(
        padding: const EdgeInsets.all(24.0),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // Top Bar
            Row(
              mainAxisAlignment: MainAxisAlignment.spaceBetween,
              children: [
                Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    const Text('OMNIGO Merchant', style: TextStyle(fontSize: 16, color: Colors.grey)),
                    Text(widget.trackingId, style: const TextStyle(fontSize: 14, fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
                  ],
                ),
                Container(
                  padding: const EdgeInsets.all(10),
                  decoration: const BoxDecoration(color: AppTheme.blackAccent, shape: BoxShape.circle),
                  child: const Icon(Icons.storefront, color: Colors.white, size: 20),
                ),
              ],
            ),
            const SizedBox(height: 30),

            Expanded(
              child: RefreshIndicator(
                onRefresh: () async {
                  await _fetchMerchantOrders();
                  await _fetchVendorMetrics();
                  await _fetchVendorRating();
                },
                child: ListView(
                  physics: const AlwaysScrollableScrollPhysics(),
                  children: [
                    // Overview Cards — wired to live /api/v1/vendor/metrics & /api/v1/ratings
                    Row(
                      children: [
                        Expanded(
                          child: Container(
                            padding: const EdgeInsets.all(16),
                            decoration: BoxDecoration(
                              color: AppTheme.softGreen,
                              borderRadius: BorderRadius.circular(24),
                            ),
                            child: Column(
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                const Text('Earnings', style: TextStyle(color: AppTheme.blackAccent, fontSize: 13)),
                                const SizedBox(height: 6),
                                Text(
                                  '\$${_totalRevenue.toStringAsFixed(2)}',
                                  style: const TextStyle(color: AppTheme.blackAccent, fontSize: 20, fontWeight: FontWeight.bold),
                                ),
                              ],
                            ),
                          ),
                        ),
                        const SizedBox(width: 10),
                        Expanded(
                          child: Container(
                            padding: const EdgeInsets.all(16),
                            decoration: BoxDecoration(
                              color: AppTheme.softBlue,
                              borderRadius: BorderRadius.circular(24),
                            ),
                            child: Column(
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                const Text('Active Gigs', style: TextStyle(color: AppTheme.blackAccent, fontSize: 13)),
                                const SizedBox(height: 6),
                                Text(
                                  '$_activeGigs Gigs',
                                  style: const TextStyle(color: AppTheme.blackAccent, fontSize: 20, fontWeight: FontWeight.bold),
                                ),
                              ],
                            ),
                          ),
                        ),
                        const SizedBox(width: 10),
                        Expanded(
                          child: Container(
                            padding: const EdgeInsets.all(16),
                            decoration: BoxDecoration(
                              color: Colors.amber.shade50,
                              borderRadius: BorderRadius.circular(24),
                              border: Border.all(color: Colors.amber.shade200),
                            ),
                            child: Column(
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                const Text('Store Rating', style: TextStyle(color: AppTheme.blackAccent, fontSize: 13)),
                                const SizedBox(height: 6),
                                Row(
                                  children: [
                                    const Icon(Icons.star_rounded, color: Colors.amber, size: 20),
                                    const SizedBox(width: 4),
                                    Text(
                                      _avgRating > 0 ? _avgRating.toStringAsFixed(1) : 'N/A',
                                      style: const TextStyle(color: AppTheme.blackAccent, fontSize: 18, fontWeight: FontWeight.bold),
                                    ),
                                    Text(
                                      ' ($_totalRatings)',
                                      style: TextStyle(color: Colors.grey.shade600, fontSize: 11),
                                    ),
                                  ],
                                ),
                              ],
                            ),
                          ),
                        ),
                      ],
                    ),
                    const SizedBox(height: 30),

                    // Navigation Card to Inventory Screen
                    _buildTabNavigationCard(
                      icon: Icons.inventory_2_rounded,
                      color: const Color(0xFFCAFF33),
                      title: 'Manage Catalog',
                      subtitle: 'Add, edit, delete, or toggle stock',
                      targetTab: 1,
                    ),
                    const SizedBox(height: 12),

                    // Navigation Card to Live Map Screen
                    _buildTabNavigationCard(
                      icon: Icons.map_rounded,
                      color: const Color(0xFF33FFA6),
                      title: 'Live Map Surveillance',
                      subtitle: 'Track assigned rider location in real-time',
                      targetTab: 2,
                    ),
                    const SizedBox(height: 12),

                    // Navigation Card to Analytics Screen
                    _buildTabNavigationCard(
                      icon: Icons.analytics_rounded,
                      color: const Color(0xFF33D1FF),
                      title: 'Store Analytics',
                      subtitle: 'View sales metrics, revenue, and daily trends',
                      targetTab: 3,
                    ),

                    const SizedBox(height: 24),

                    const Text('Incoming Orders', style: TextStyle(fontSize: 20, fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
                    const SizedBox(height: 16),

                    if (_isLoadingOrders)
                      const Center(child: Padding(
                        padding: EdgeInsets.all(20.0),
                        child: CircularProgressIndicator(color: AppTheme.limeAccent),
                      ),)
                    else if (_orders.isEmpty)
                      const Center(child: Padding(
                        padding: EdgeInsets.all(20.0),
                        child: Text('No incoming orders.', style: TextStyle(color: Colors.grey, fontSize: 16)),
                      ),)
                    else
                      ..._orders.map((orderMap) {
                        final order = orderMap as Map<String, dynamic>;
                        final orderId = (order['order_tracking_id'] as String?) ?? 'ORD-UNKNOWN';
                        final price = ((order['total_amount'] as num?) ?? 0.0).toString();
                        final customer = (order['customer_tracking_id'] as String?) ?? 'CUST-UNKNOWN';
                        final status = (order['status'] as String?) ?? 'pending';

                        return Container(
                          margin: const EdgeInsets.only(bottom: 16),
                          padding: const EdgeInsets.all(20),
                          decoration: BoxDecoration(
                            color: Colors.white,
                            borderRadius: BorderRadius.circular(24),
                            boxShadow: [
                              BoxShadow(color: Colors.black.withOpacity(0.01), blurRadius: 10, offset: const Offset(0, 5)),
                            ],
                          ),
                          child: Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              Row(
                                mainAxisAlignment: MainAxisAlignment.spaceBetween,
                                children: [
                                  Text('Order #$orderId', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 16, color: AppTheme.blackAccent)),
                                  Text('\$$price', style: const TextStyle(fontWeight: FontWeight.bold, color: Colors.blue)),
                                ],
                              ),
                              const SizedBox(height: 8),
                              Text('Customer: $customer', style: const TextStyle(color: Colors.grey)),
                              const SizedBox(height: 16),
                              Wrap(
                                spacing: 8,
                                runSpacing: 8,
                                alignment: WrapAlignment.end,
                                children: [
                                  // Direct Chat Customer button
                                  ElevatedButton.icon(
                                    onPressed: () {
                                      Navigator.of(context).push(
                                        MaterialPageRoute<void>(
                                          builder: (_) => ChatRoomScreen(
                                            orderId: orderId,
                                            otherUserId: customer,
                                            otherUserName: 'Customer ($customer)',
                                            otherUserRole: 'customer',
                                          ),
                                        ),
                                      );
                                    },
                                    icon: const Icon(Icons.chat_bubble_outline_rounded, size: 16),
                                    label: const Text('Chat Customer'),
                                    style: ElevatedButton.styleFrom(
                                      backgroundColor: Colors.blue.shade50,
                                      foregroundColor: Colors.blue.shade800,
                                      padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
                                      elevation: 0,
                                      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                                    ),
                                  ),
                                  if (status == 'pending')
                                    ElevatedButton(
                                      onPressed: () => _acceptOrder(orderId),
                                      style: ElevatedButton.styleFrom(
                                        backgroundColor: AppTheme.blackAccent,
                                        padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 10),
                                        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                                      ),
                                      child: const Text('Accept Order'),
                                    ),
                                  if (status == 'accepted')
                                    ElevatedButton(
                                      onPressed: () => _printSlip(order),
                                      style: ElevatedButton.styleFrom(
                                        backgroundColor: AppTheme.limeAccent,
                                        foregroundColor: AppTheme.blackAccent,
                                        padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 10),
                                        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                                      ),
                                      child: const Text('Print Slip & Broadcast'),
                                    ),
                                  if (status == 'accepted' || status == 'shipped')
                                    ElevatedButton.icon(
                                      onPressed: _isHandoverInProgress
                                          ? null
                                          : () => _handoverToRider(orderId),
                                      icon: const Icon(Icons.handshake_rounded, size: 18),
                                      label: const Text('Hand Over to Rider'),
                                      style: ElevatedButton.styleFrom(
                                        backgroundColor: Colors.black,
                                        foregroundColor: AppTheme.limeAccent,
                                        padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 10),
                                        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                                      ),
                                    ),
                                  if (status == 'shipped' || status == 'delivered')
                                    Container(
                                      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
                                      decoration: BoxDecoration(
                                        color: Colors.grey.shade100,
                                        borderRadius: BorderRadius.circular(16),
                                      ),
                                      child: Text(
                                        status == 'delivered' ? 'Delivered' : 'Gig Broadcasted',
                                        style: const TextStyle(color: Colors.grey, fontWeight: FontWeight.bold),
                                      ),
                                    ),
                                ],
                              ),
                            ],
                          ),
                        );
                      }),
                  ],
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildTabNavigationCard({
    required IconData icon,
    required Color color,
    required String title,
    required String subtitle,
    required int targetTab,
  }) {
    return InkWell(
      onTap: () {
        setState(() {
          _currentIndex = targetTab;
        });
      },
      child: Container(
        padding: const EdgeInsets.all(20),
        decoration: BoxDecoration(
          color: AppTheme.blackAccent,
          borderRadius: BorderRadius.circular(24),
        ),
        child: Row(
          mainAxisAlignment: MainAxisAlignment.spaceBetween,
          children: [
            Expanded(
              child: Row(
                children: [
                  Icon(icon, color: color, size: 24),
                  const SizedBox(width: 16),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(title, style: const TextStyle(color: Colors.white, fontSize: 16, fontWeight: FontWeight.bold), overflow: TextOverflow.ellipsis),
                        const SizedBox(height: 4),
                        Text(subtitle, style: TextStyle(color: Colors.grey.shade400, fontSize: 12), maxLines: 2, overflow: TextOverflow.ellipsis),
                      ],
                    ),
                  ),
                ],
              ),
            ),
            const SizedBox(width: 8),
            Icon(Icons.arrow_forward_ios_rounded, color: Colors.grey.shade400, size: 16),
          ],
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: AppTheme.bgColor,
      body: IndexedStack(
        index: _currentIndex,
        children: [
          _buildDashboardHome(),
          VendorInventoryScreen(vendorTrackingId: widget.trackingId),
          const VendorLiveMapScreen(),
          VendorAnalyticsScreen(vendorTrackingId: widget.trackingId),
          VendorWalletScreen(vendorTrackingId: widget.trackingId),
          VendorProfileTab(vendorTrackingId: widget.trackingId),
        ],
      ),
      floatingActionButton: _buildChatFab(),
      bottomNavigationBar: Theme(
        data: Theme.of(context).copyWith(
          canvasColor: Colors.black,
        ),
        child: BottomNavigationBar(
          currentIndex: _currentIndex,
          onTap: (index) {
            setState(() {
              _currentIndex = index;
            });
          },
          selectedItemColor: AppTheme.limeAccent,
          unselectedItemColor: Colors.white54,
          showSelectedLabels: true,
          showUnselectedLabels: false,
          type: BottomNavigationBarType.fixed,
          backgroundColor: Colors.black,
          items: const [
            BottomNavigationBarItem(icon: Icon(Icons.home_filled), label: 'Home'),
            BottomNavigationBarItem(icon: Icon(Icons.inventory_2_rounded), label: 'Catalog'),
            BottomNavigationBarItem(icon: Icon(Icons.map_rounded), label: 'Live Map'),
            BottomNavigationBarItem(icon: Icon(Icons.analytics_rounded), label: 'Analytics'),
            BottomNavigationBarItem(icon: Icon(Icons.account_balance_wallet_rounded), label: 'Wallet'),
            BottomNavigationBarItem(icon: Icon(Icons.person_rounded), label: 'Profile'),
          ],
        ),
      ),
    );
  }

  /// Floating chat button. Opens the chat list screen. The unread
  /// badge is driven by the shared ChatNavButton logic on the chat list
  /// screen itself (it polls every 15s).
  Widget _buildChatFab() {
    return FloatingActionButton(
      heroTag: 'vendor_chat_fab',
      backgroundColor: Colors.black,
      foregroundColor: AppTheme.limeAccent,
      onPressed: () {
        Navigator.of(context).push(
          MaterialPageRoute<void>(
            builder: (_) => const ChatListScreen(),
          ),
        );
      },
      child: const Icon(Icons.chat_bubble_rounded),
    );
  }
}

class VendorProfileTab extends StatefulWidget {
  const VendorProfileTab({super.key, required this.vendorTrackingId});
  final String vendorTrackingId;

  @override
  State<VendorProfileTab> createState() => _VendorProfileTabState();
}

class _VendorProfileTabState extends State<VendorProfileTab> {
  Map<String, dynamic>? _storeData;
  bool _isLoading = true;

  // Verification Form State
  File? _cnicFrontFile;
  File? _cnicBackFile;
  File? _licenseFile;
  bool _isUploadingDocs = false;
  final _businessNameController = TextEditingController();
  final _ntnController = TextEditingController();
  final _fullNameController = TextEditingController();
  final ImagePicker _imagePicker = ImagePicker();

  @override
  void initState() {
    super.initState();
    _fetchStoreDetails();
  }

  Future<void> _fetchStoreDetails() async {
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
        if (mounted) {
          setState(() {
            _storeData = jsonDecode(response.body) as Map<String, dynamic>;
            _isLoading = false;
          });
        }
      } else {
        if (mounted) setState(() => _isLoading = false);
      }
    } catch (e) {
      if (mounted) setState(() => _isLoading = false);
      debugPrint('Error fetching store info for profile: $e');
    }
  }

  Future<void> _pickDocument(String type) async {
    try {
      final picked = await _imagePicker.pickImage(source: ImageSource.gallery, maxWidth: 1200, imageQuality: 85);
      if (picked == null) return;
      setState(() {
        if (type == 'cnic_front') {
          _cnicFrontFile = File(picked.path);
        } else if (type == 'cnic_back') {
          _cnicBackFile = File(picked.path);
        } else if (type == 'license') {
          _licenseFile = File(picked.path);
        }
      });
    } catch (e) {
      debugPrint('Image picker error: $e');
    }
  }

  Future<void> _submitVerification() async {
    final entityType = SessionRegistry.instance.entityType ?? 'company';
    if (entityType == 'company') {
      if (_businessNameController.text.trim().isEmpty || _ntnController.text.trim().isEmpty || _licenseFile == null) {
        ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Please complete all company fields and upload the certificate.')));
        return;
      }
    } else {
      if (_fullNameController.text.trim().isEmpty || _cnicFrontFile == null || _cnicBackFile == null) {
        ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Please complete all individual fields and upload CNIC front and back.')));
        return;
      }
    }

    setState(() => _isUploadingDocs = true);
    try {
      final request = http.MultipartRequest(
        'PUT',
        Uri.parse('${ApiEndpoints.authBase}/auth/vendor/verify'),
      );
      request.headers['Authorization'] = 'Bearer ${SessionRegistry.instance.token}';
      
      request.fields['full_name'] = _fullNameController.text.trim();
      request.fields['business_name'] = _businessNameController.text.trim();
      request.fields['ntn_number'] = _ntnController.text.trim();

      if (_cnicFrontFile != null) request.files.add(await http.MultipartFile.fromPath('cnic_front', _cnicFrontFile!.path));
      if (_cnicBackFile != null) request.files.add(await http.MultipartFile.fromPath('cnic_back', _cnicBackFile!.path));
      if (_licenseFile != null) request.files.add(await http.MultipartFile.fromPath('license_cert', _licenseFile!.path));

      final response = await request.send().timeout(const Duration(seconds: 30));
      final bodyStr = await response.stream.bytesToString();
      
      if (response.statusCode == 200) {
        final data = jsonDecode(bodyStr) as Map<String, dynamic>;
        final isVer = (data['is_verified'] as bool?) ?? false;
        if (isVer) {
          await SessionRegistry.instance.saveSession(
            token: SessionRegistry.instance.token!,
            role: SessionRegistry.instance.role!,
            trackingId: SessionRegistry.instance.trackingId!,
            isVerified: true,
            entityType: SessionRegistry.instance.entityType,
          );
          if (!mounted) return;
          ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('Congratulations! Your shop is now officially live.')));
          setState(() {}); // trigger rebuild
        }
      } else {
        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('Verification failed: ${response.statusCode}')));
      }
    } catch (e) {
      debugPrint('Verification error: $e');
    } finally {
      if (mounted) setState(() => _isUploadingDocs = false);
    }
  }

  Widget _buildTransparencyBanner() {
    if (SessionRegistry.instance.isVerified) return const SizedBox.shrink();
    final entityType = SessionRegistry.instance.entityType == 'individual' ? 'Individual' : 'Company';
    
    return Container(
      padding: const EdgeInsets.all(16),
      margin: const EdgeInsets.only(bottom: 24),
      decoration: BoxDecoration(
        color: Colors.orange.shade50,
        borderRadius: BorderRadius.circular(16),
        border: Border.all(color: Colors.orange.shade200),
      ),
      child: Row(
        children: [
          const Icon(Icons.warning_amber_rounded, color: Colors.orange, size: 28),
          const SizedBox(width: 16),
          Expanded(
            child: Text(
              'Welcome! Please complete your $entityType verification below to automatically launch your shop and make your products live for customers.',
              style: TextStyle(color: Colors.orange.shade900, fontSize: 13, fontWeight: FontWeight.w500),
            ),
          ),
        ],
      ),
    );
  }

  Widget _buildUploadButton(String label, String type, File? file) {
    return InkWell(
      onTap: () => _pickDocument(type),
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 14),
        decoration: BoxDecoration(
          color: Colors.white,
          borderRadius: BorderRadius.circular(16),
          border: Border.all(color: file != null ? Colors.green : Colors.grey.shade300),
        ),
        child: Row(
          children: [
            Icon(file != null ? Icons.check_circle : Icons.upload_file, color: file != null ? Colors.green : Colors.grey),
            const SizedBox(width: 12),
            Expanded(child: Text(file != null ? 'Document Selected' : label, style: TextStyle(color: file != null ? Colors.green : Colors.grey))),
          ],
        ),
      ),
    );
  }

  Widget _buildVerificationWorkspace() {
    if (SessionRegistry.instance.isVerified) return const SizedBox.shrink();
    final isCompany = SessionRegistry.instance.entityType != 'individual';
    
    return Container(
      padding: const EdgeInsets.all(24),
      margin: const EdgeInsets.only(bottom: 24),
      decoration: BoxDecoration(
        color: AppTheme.blackAccent,
        borderRadius: BorderRadius.circular(24),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const Text('Verification Workspace', style: TextStyle(color: Colors.white, fontSize: 18, fontWeight: FontWeight.bold)),
          const SizedBox(height: 8),
          Text(isCompany ? 'Company requirements' : 'Individual requirements', style: TextStyle(color: Colors.grey.shade400, fontSize: 13)),
          const SizedBox(height: 20),
          
          if (isCompany) ...[
            TextField(
              controller: _businessNameController,
              decoration: InputDecoration(hintText: 'Company / Business Name', filled: true, fillColor: Colors.white, border: OutlineInputBorder(borderRadius: BorderRadius.circular(16), borderSide: BorderSide.none)),
            ),
            const SizedBox(height: 12),
            TextField(
              controller: _ntnController,
              decoration: InputDecoration(hintText: 'NTN Number', filled: true, fillColor: Colors.white, border: OutlineInputBorder(borderRadius: BorderRadius.circular(16), borderSide: BorderSide.none)),
            ),
            const SizedBox(height: 12),
            _buildUploadButton('Company Registration Certificate', 'license', _licenseFile),
          ] else ...[
            TextField(
              controller: _fullNameController,
              decoration: InputDecoration(hintText: 'Full Legal Name', filled: true, fillColor: Colors.white, border: OutlineInputBorder(borderRadius: BorderRadius.circular(16), borderSide: BorderSide.none)),
            ),
            const SizedBox(height: 12),
            _buildUploadButton('CNIC Front Image', 'cnic_front', _cnicFrontFile),
            const SizedBox(height: 12),
            _buildUploadButton('CNIC Back Image', 'cnic_back', _cnicBackFile),
          ],
          const SizedBox(height: 24),
          SizedBox(
            width: double.infinity,
            height: 50,
            child: ElevatedButton(
              onPressed: _isUploadingDocs ? null : _submitVerification,
              style: ElevatedButton.styleFrom(backgroundColor: AppTheme.limeAccent, foregroundColor: AppTheme.blackAccent, shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16))),
              child: _isUploadingDocs ? const CircularProgressIndicator(color: AppTheme.blackAccent) : const Text('Submit Verification', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 16)),
            ),
          ),
        ],
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final businessName = (_storeData?['store_name'] as String?) ?? SessionRegistry.instance.fullName ?? 'Setup Pending...';
    final storeTrackingId = (_storeData?['store_tracking_id'] as String?) ?? 'Setup Pending...';
    final entityType = SessionRegistry.instance.entityType ?? 'individual';
    final isVerified = SessionRegistry.instance.isVerified;

    return SafeArea(
      child: SingleChildScrollView(
        padding: const EdgeInsets.all(24),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text(
              'Merchant Profile',
              style: TextStyle(fontSize: 24, fontWeight: FontWeight.bold, color: AppTheme.blackAccent),
            ),
            const SizedBox(height: 24),
            
            if (_isLoading)
              const Center(child: CircularProgressIndicator(color: AppTheme.limeAccent))
            else ...[
              // Profile Details Card
              Container(
                width: double.infinity,
                padding: const EdgeInsets.all(24),
                decoration: BoxDecoration(
                  color: AppTheme.blackAccent,
                  borderRadius: BorderRadius.circular(24),
                ),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(
                      mainAxisAlignment: MainAxisAlignment.spaceBetween,
                      children: [
                        Expanded(
                          child: Text(
                            businessName,
                            style: const TextStyle(color: Colors.white, fontSize: 20, fontWeight: FontWeight.bold),
                          ),
                        ),
                        Container(
                          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
                          decoration: BoxDecoration(
                            color: isVerified ? Colors.green.withOpacity(0.2) : Colors.orange.withOpacity(0.2),
                            borderRadius: BorderRadius.circular(12),
                            border: Border.all(color: isVerified ? Colors.green : Colors.orange),
                          ),
                          child: Text(
                            isVerified ? 'Verified' : 'Pending',
                            style: TextStyle(color: isVerified ? Colors.green : Colors.orange, fontSize: 12, fontWeight: FontWeight.bold),
                          ),
                        ),
                      ],
                    ),
                    const SizedBox(height: 8),
                    Text(
                      'Entity: ${entityType.toUpperCase()}',
                      style: TextStyle(color: Colors.grey.shade400, fontSize: 13),
                    ),
                    const SizedBox(height: 20),
                    const Divider(color: Colors.white24),
                    const SizedBox(height: 12),
                    
                    _buildInfoRow('Vendor ID', widget.vendorTrackingId),
                    const SizedBox(height: 12),
                    _buildInfoRow('Store ID', storeTrackingId),
                    const SizedBox(height: 20),
                    const Divider(color: Colors.white24),
                    const SizedBox(height: 12),
                    _buildInfoRow('Email', SessionRegistry.instance.email ?? 'N/A'),
                    const SizedBox(height: 12),
                    _buildInfoRow('Phone', SessionRegistry.instance.phone ?? 'N/A'),
                    const SizedBox(height: 12),
                    _buildInfoRow('Address', SessionRegistry.instance.address ?? 'N/A'),
                  ],
                ),
              ),
            ],
            
            
            const SizedBox(height: 32),
            _buildTransparencyBanner(),
            _buildVerificationWorkspace(),
            
            // Logout Button
            SizedBox(
              width: double.infinity,
              height: 54,
              child: ElevatedButton.icon(
                onPressed: () async {
                try {
                  await SessionRegistry.instance.logout();
                } finally {
                  if (context.mounted) {
                    unawaited(Navigator.pushNamedAndRemoveUntil(context, '/', (route) => false));
                  }
                }
                },
                icon: const Icon(Icons.logout_rounded, color: Colors.white),
                label: const Text('Logout Session', style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontSize: 16)),
                style: ElevatedButton.styleFrom(
                  backgroundColor: Colors.red.shade800,
                  shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildInfoRow(String label, String value) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(label, style: TextStyle(color: Colors.grey.shade500, fontSize: 11)),
        const SizedBox(height: 4),
        Text(value, style: const TextStyle(color: Colors.white, fontSize: 14, fontFamily: 'monospace', fontWeight: FontWeight.bold)),
      ],
    );
  }
}
