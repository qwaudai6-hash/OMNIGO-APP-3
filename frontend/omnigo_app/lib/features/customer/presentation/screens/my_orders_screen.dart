import 'package:flutter/material.dart';
import 'package:provider/provider.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/services/cart_provider.dart';
import '../../data/models/cart_item.dart';
import 'checkout_screen.dart';
import 'order_detail_screen.dart';

class MyOrdersScreen extends StatefulWidget {
  const MyOrdersScreen({super.key, required this.customerTrackingId});

  final String customerTrackingId;

  @override
  State<MyOrdersScreen> createState() => _MyOrdersScreenState();
}

class _MyOrdersScreenState extends State<MyOrdersScreen> with SingleTickerProviderStateMixin {
  late TabController _tabController;
  final TextEditingController _searchController = TextEditingController();
  final ScrollController _scrollController = ScrollController();

  List<Map<String, dynamic>> _allOrders = [];
  List<Map<String, dynamic>> _filteredOrders = [];
  bool _isLoading = false;
  bool _hasMore = true;
  int _offset = 0;
  final int _limit = 20;
  String _searchQuery = '';
  DateTimeRange? _selectedDateRange;

  static const _activeStatuses = {'pending', 'paid', 'accepted', 'processing', 'shipped', 'in_transit'};
  static const _completedStatuses = {'delivered', 'completed'};
  static const _cancelledStatuses = {'cancelled', 'failed', 'payment_failed'};

  @override
  void initState() {
    super.initState();
    _tabController = TabController(length: 4, vsync: this);
    _tabController.addListener(_onTabChanged);
    _scrollController.addListener(_onScroll);
    _fetchOrders();
  }

  @override
  void dispose() {
    _tabController.dispose();
    _searchController.dispose();
    _scrollController.dispose();
    super.dispose();
  }

  void _onTabChanged() {
    if (!_tabController.indexIsChanging) {
      _applyFilter();
    }
  }

  void _onScroll() {
    if (_scrollController.position.pixels >= _scrollController.position.maxScrollExtent - 200 && !_isLoading && _hasMore) {
      _fetchOrdersMore();
    }
  }

  Future<void> _fetchOrders({bool reset = false}) async {
    if (_isLoading) return;
    setState(() {
      _isLoading = true;
      if (reset) {
        _offset = 0;
        _hasMore = true;
      }
    });

    try {
      final response = await ApiClient().get('/orders/customer/${widget.customerTrackingId}');
      final List<dynamic> orders = response is List ? response : [];
      setState(() {
        if (reset) {
          _allOrders = orders.cast<Map<String, dynamic>>();
        } else {
          _allOrders = [..._allOrders, ...orders.cast<Map<String, dynamic>>()];
        }
        _hasMore = orders.length >= _limit;
        _offset += orders.length;
        _applyFilter();
        _isLoading = false;
      });
    } catch (e) {
      setState(() => _isLoading = false);
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed to load orders: $e')),
        );
      }
    }
  }

  Future<void> _fetchOrdersMore() async {
    await _fetchOrders();
  }

  Future<void> _onRefresh() async {
    await _fetchOrders(reset: true);
  }

  void _applyFilter() {
    final tabIndex = _tabController.index;
    final query = _searchQuery.toLowerCase();

    setState(() {
      _filteredOrders = _allOrders.where((order) {
        final status = (order['status'] ?? '').toString().toLowerCase();
        final orderId = (order['order_tracking_id'] ?? '').toString().toLowerCase();
        final storeId = (order['store_tracking_id'] ?? '').toString().toLowerCase();

        bool matchesTab;
        switch (tabIndex) {
          case 1:
            matchesTab = _activeStatuses.contains(status);
            break;
          case 2:
            matchesTab = _completedStatuses.contains(status);
            break;
          case 3:
            matchesTab = _cancelledStatuses.contains(status);
            break;
          default:
            matchesTab = true;
        }

        final matchesSearch = query.isEmpty ||
            orderId.contains(query) ||
            storeId.contains(query);

        bool matchesDate = true;
        if (_selectedDateRange != null) {
          final rawCreated = order['created_at']?.toString();
          if (rawCreated != null && rawCreated.isNotEmpty) {
            final dt = DateTime.tryParse(rawCreated);
            if (dt != null) {
              final start = DateTime(_selectedDateRange!.start.year, _selectedDateRange!.start.month, _selectedDateRange!.start.day);
              final end = DateTime(_selectedDateRange!.end.year, _selectedDateRange!.end.month, _selectedDateRange!.end.day, 23, 59, 59);
              matchesDate = dt.isAfter(start.subtract(const Duration(seconds: 1))) &&
                  dt.isBefore(end.add(const Duration(seconds: 1)));
            }
          }
        }

        return matchesTab && matchesSearch && matchesDate;
      }).toList();
    });
  }

  Future<void> _pickDateRange() async {
    final picked = await showDateRangePicker(
      context: context,
      firstDate: DateTime(2023),
      lastDate: DateTime.now().add(const Duration(days: 1)),
      initialDateRange: _selectedDateRange,
      builder: (context, child) {
        return Theme(
          data: Theme.of(context).copyWith(
            colorScheme: const ColorScheme.light(
              primary: AppTheme.blackAccent,
              onPrimary: Colors.white,
              surface: Colors.white,
              onSurface: AppTheme.blackAccent,
            ),
          ),
          child: child!,
        );
      },
    );
    if (picked != null) {
      setState(() {
        _selectedDateRange = picked;
      });
      _applyFilter();
    }
  }

  void _clearDateFilter() {
    setState(() {
      _selectedDateRange = null;
    });
    _applyFilter();
  }

  Future<void> _reorder(Map<String, dynamic> order) async {
    try {
      List<dynamic> items = (order['items'] as List<dynamic>?) ??
          (order['products'] as List<dynamic>?) ??
          [];
      if (items.isEmpty) {
        final orderId = order['order_tracking_id']?.toString() ?? '';
        if (orderId.isNotEmpty) {
          final detail = await ApiClient().get(ApiEndpoints.orderDetail(orderId));
          if (detail is Map<String, dynamic>) {
            items = (detail['items'] as List<dynamic>?) ??
                (detail['products'] as List<dynamic>?) ??
                [];
          }
        }
      }
      if (items.isEmpty) {
        if (mounted) {
          ScaffoldMessenger.of(context).showSnackBar(
            const SnackBar(content: Text('No item details available to reorder.')),
          );
        }
        return;
      }

      if (!mounted) return;
      final cart = Provider.of<CartProvider>(context, listen: false);
      for (final rawItem in items) {
        if (rawItem is Map<String, dynamic>) {
          final cartItem = CartItem.fromJson(rawItem);
          if (cartItem.productId.isNotEmpty) {
            await cart.addCartItem(cartItem, clearIfDifferentStore: true);
          }
        }
      }

      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
            content: const Text('Items added to cart!'),
            backgroundColor: Colors.green,
            action: SnackBarAction(
              label: 'Checkout',
              textColor: Colors.white,
              onPressed: () {
                Navigator.push(
                  context,
                  MaterialPageRoute<void>(builder: (_) => const CheckoutScreen()),
                );
              },
            ),
          ),
        );
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed to reorder: $e')),
        );
      }
    }
  }

  void _onSearchChanged(String value) {
    setState(() {
      _searchQuery = value;
      _applyFilter();
    });
  }

  Color _getStatusColor(String status) {
    switch (status.toLowerCase()) {
      case 'pending':
        return Colors.orange;
      case 'paid':
        return Colors.blue;
      case 'accepted':
      case 'processing':
        return Colors.purple;
      case 'shipped':
      case 'in_transit':
        return Colors.teal;
      case 'delivered':
      case 'completed':
        return Colors.green;
      case 'cancelled':
      case 'failed':
      case 'payment_failed':
        return Colors.red;
      default:
        return Colors.grey;
    }
  }

  IconData _getStatusIcon(String status) {
    switch (status.toLowerCase()) {
      case 'pending':
        return Icons.schedule;
      case 'paid':
        return Icons.payment;
      case 'accepted':
        return Icons.check_circle_outline;
      case 'processing':
        return Icons.inventory_2_outlined;
      case 'shipped':
      case 'in_transit':
        return Icons.local_shipping_outlined;
      case 'delivered':
      case 'completed':
        return Icons.check_circle;
      case 'cancelled':
      case 'failed':
      case 'payment_failed':
        return Icons.cancel_outlined;
      default:
        return Icons.help_outline;
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: AppTheme.bgColor,
      appBar: AppBar(
        backgroundColor: AppTheme.bgColor,
        elevation: 0,
        title: const Text(
          'Orders',
          style: TextStyle(
            color: AppTheme.blackAccent,
            fontWeight: FontWeight.bold,
          ),
        ),
        actions: [
          if (_selectedDateRange != null)
            IconButton(
              icon: const Icon(Icons.filter_alt_off, color: Colors.redAccent),
              tooltip: 'Clear Date Filter',
              onPressed: _clearDateFilter,
            ),
          IconButton(
            icon: Icon(
              Icons.date_range_outlined,
              color: _selectedDateRange != null ? AppTheme.blackAccent : Colors.grey.shade700,
            ),
            tooltip: 'Filter by Date Range',
            onPressed: _pickDateRange,
          ),
        ],
        bottom: PreferredSize(
          preferredSize: Size.fromHeight(_selectedDateRange != null ? 144 : 110),
          child: Column(
            children: [
              if (_selectedDateRange != null)
                Padding(
                  padding: const EdgeInsets.only(left: 16, right: 16, bottom: 8),
                  child: Row(
                    children: [
                      Container(
                        padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
                        decoration: BoxDecoration(
                          color: AppTheme.limeAccent.withOpacity(0.4),
                          borderRadius: BorderRadius.circular(16),
                        ),
                        child: Row(
                          mainAxisSize: MainAxisSize.min,
                          children: [
                            const Icon(Icons.calendar_today, size: 12),
                            const SizedBox(width: 6),
                            Text(
                              '${_selectedDateRange!.start.day}/${_selectedDateRange!.start.month}/${_selectedDateRange!.start.year} - ${_selectedDateRange!.end.day}/${_selectedDateRange!.end.month}/${_selectedDateRange!.end.year}',
                              style: const TextStyle(fontSize: 12, fontWeight: FontWeight.bold),
                            ),
                            const SizedBox(width: 4),
                            GestureDetector(
                              onTap: _clearDateFilter,
                              child: const Icon(Icons.close, size: 14),
                            ),
                          ],
                        ),
                      ),
                    ],
                  ),
                ),
              Padding(
                padding: const EdgeInsets.symmetric(horizontal: 16),
                child: TextField(
                  controller: _searchController,
                  onChanged: _onSearchChanged,
                  decoration: InputDecoration(
                    hintText: 'Search by Order ID or Store...',
                    prefixIcon: const Icon(Icons.search, color: Colors.grey),
                    filled: true,
                    fillColor: Colors.white,
                    border: OutlineInputBorder(
                      borderRadius: BorderRadius.circular(12),
                      borderSide: BorderSide.none,
                    ),
                    contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
                  ),
                ),
              ),
              const SizedBox(height: 8),
              TabBar(
                controller: _tabController,
                isScrollable: true,
                labelColor: AppTheme.blackAccent,
                unselectedLabelColor: Colors.grey,
                indicatorColor: AppTheme.limeAccent,
                indicatorWeight: 3,
                tabAlignment: TabAlignment.start,
                tabs: const [
                  Tab(text: 'All'),
                  Tab(text: 'Active'),
                  Tab(text: 'Completed'),
                  Tab(text: 'Cancelled'),
                ],
              ),
            ],
          ),
        ),
      ),
      body: TabBarView(
        controller: _tabController,
        children: [
          _buildOrdersList(),
          _buildOrdersList(),
          _buildOrdersList(),
          _buildOrdersList(),
        ],
      ),
    );
  }

  Widget _buildOrdersList() {
    if (_isLoading && _filteredOrders.isEmpty) {
      return const Center(
        child: CircularProgressIndicator(color: AppTheme.limeAccent),
      );
    }

    if (_filteredOrders.isEmpty) {
      return Center(
        child: Column(
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            Icon(
              Icons.shopping_bag_outlined,
              size: 64,
              color: Colors.grey.shade400,
            ),
            const SizedBox(height: 16),
            Text(
              _searchQuery.isNotEmpty ? 'No orders match your search' : 'No orders yet',
              style: TextStyle(
                fontSize: 16,
                color: Colors.grey.shade600,
              ),
            ),
          ],
        ),
      );
    }

    return RefreshIndicator(
      onRefresh: _onRefresh,
      color: AppTheme.limeAccent,
      backgroundColor: AppTheme.blackAccent,
      child: ListView.builder(
        controller: _scrollController,
        padding: const EdgeInsets.all(16),
        itemCount: _filteredOrders.length + (_hasMore ? 1 : 0),
        itemBuilder: (context, index) {
          if (index >= _filteredOrders.length) {
            return const Center(
              child: Padding(
                padding: EdgeInsets.all(16),
                child: CircularProgressIndicator(color: AppTheme.limeAccent),
              ),
            );
          }
          return _buildOrderCard(_filteredOrders[index]);
        },
      ),
    );
  }

  Widget _buildOrderCard(Map<String, dynamic> order) {
    final orderId = order['order_tracking_id']?.toString() ?? 'ORD-UNKNOWN';
    final storeId = order['store_tracking_id']?.toString() ?? 'STOR-UNKNOWN';
    final total = (order['total_amount'] ?? 0.0).toString();
    final currency = order['currency']?.toString() ?? 'PKR';
    final status = order['status']?.toString() ?? 'pending';
    final createdAt = order['created_at']?.toString() ?? '';
    final products = order['products'] as List<dynamic>? ?? [];
    final paymentMethod = order['payment_method']?.toString().toUpperCase() ?? order['payment_gateway']?.toString().toUpperCase() ?? 'N/A';

    final statusColor = _getStatusColor(status);
    final statusIcon = _getStatusIcon(status);
    final isActive = _activeStatuses.contains(status.toLowerCase());
    final isCancelled = _cancelledStatuses.contains(status.toLowerCase());

    return GestureDetector(
      onTap: () async {
        await Navigator.push(
          context,
          MaterialPageRoute<void>(
            builder: (_) => OrderDetailScreen(order: order),
          ),
        );
        _onRefresh();
      },
      child: Container(
        margin: const EdgeInsets.only(bottom: 16),
        decoration: BoxDecoration(
          color: Colors.white,
          borderRadius: BorderRadius.circular(20),
          boxShadow: [
            BoxShadow(
              color: Colors.black.withOpacity(0.04),
              blurRadius: 10,
              offset: const Offset(0, 4),
            ),
          ],
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Padding(
              padding: const EdgeInsets.all(16),
              child: Row(
                mainAxisAlignment: MainAxisAlignment.spaceBetween,
                children: [
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          'Order #$orderId',
                          style: const TextStyle(
                            fontWeight: FontWeight.bold,
                            fontSize: 15,
                            color: AppTheme.blackAccent,
                          ),
                          overflow: TextOverflow.ellipsis,
                        ),
                        const SizedBox(height: 4),
                        Text(
                          storeId,
                          style: TextStyle(
                            fontSize: 12,
                            color: Colors.grey.shade600,
                          ),
                        ),
                      ],
                    ),
                  ),
                  Container(
                    padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
                    decoration: BoxDecoration(
                      color: statusColor.withOpacity(0.1),
                      borderRadius: BorderRadius.circular(20),
                    ),
                    child: Row(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        Icon(statusIcon, size: 14, color: statusColor),
                        const SizedBox(width: 4),
                        Text(
                          status.toUpperCase(),
                          style: TextStyle(
                            fontWeight: FontWeight.bold,
                            color: statusColor,
                            fontSize: 11,
                          ),
                        ),
                      ],
                    ),
                  ),
                ],
              ),
            ),
            if (products.isNotEmpty)
              SizedBox(
                height: 60,
                child: ListView.builder(
                  scrollDirection: Axis.horizontal,
                  padding: const EdgeInsets.symmetric(horizontal: 16),
                  itemCount: products.length > 4 ? 4 : products.length,
                  itemBuilder: (context, idx) {
                    final product = products[idx] as Map<String, dynamic>;
                    final imageUrl = product['image_url']?.toString();
                    return Container(
                      width: 50,
                      height: 50,
                      margin: const EdgeInsets.only(right: 8),
                      decoration: BoxDecoration(
                        color: Colors.grey.shade200,
                        borderRadius: BorderRadius.circular(8),
                        image: imageUrl != null
                            ? DecorationImage(
                                image: NetworkImage(imageUrl),
                                fit: BoxFit.cover,
                              )
                            : null,
                      ),
                      child: imageUrl == null
                          ? Icon(Icons.image, color: Colors.grey.shade400, size: 24)
                          : null,
                    );
                  },
                ),
              ),
            const SizedBox(height: 12),
            const Divider(height: 1),
            Padding(
              padding: const EdgeInsets.all(16),
              child: Row(
                mainAxisAlignment: MainAxisAlignment.spaceBetween,
                children: [
                  Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        '$currency $total',
                        style: const TextStyle(
                          fontWeight: FontWeight.bold,
                          fontSize: 16,
                          color: AppTheme.blackAccent,
                        ),
                      ),
                      const SizedBox(height: 2),
                      Text(
                        paymentMethod,
                        style: TextStyle(
                          fontSize: 12,
                          color: Colors.grey.shade600,
                        ),
                      ),
                    ],
                  ),
                  Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      if (!isActive) ...[
                        ElevatedButton.icon(
                          onPressed: () => _reorder(order),
                          icon: const Icon(Icons.repeat, size: 14),
                          label: const Text(
                            'Reorder',
                            style: TextStyle(fontSize: 12, fontWeight: FontWeight.bold),
                          ),
                          style: ElevatedButton.styleFrom(
                            backgroundColor: isCancelled ? Colors.grey.shade200 : AppTheme.limeAccent,
                            foregroundColor: AppTheme.blackAccent,
                            padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
                            shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(20)),
                            elevation: 0,
                          ),
                        ),
                        const SizedBox(width: 8),
                      ],
                      if (isActive)
                        Container(
                          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
                          decoration: BoxDecoration(
                            color: AppTheme.limeAccent.withOpacity(0.2),
                            borderRadius: BorderRadius.circular(20),
                          ),
                          child: const Row(
                            mainAxisSize: MainAxisSize.min,
                            children: [
                              Icon(Icons.radar, size: 14, color: AppTheme.blackAccent),
                              SizedBox(width: 4),
                              Text(
                                'Track',
                                style: TextStyle(
                                  fontWeight: FontWeight.bold,
                                  color: AppTheme.blackAccent,
                                  fontSize: 12,
                                ),
                              ),
                            ],
                          ),
                        )
                      else if (isCancelled)
                        Container(
                          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
                          decoration: BoxDecoration(
                            color: Colors.red.withOpacity(0.1),
                            borderRadius: BorderRadius.circular(20),
                          ),
                          child: const Row(
                            mainAxisSize: MainAxisSize.min,
                            children: [
                              Icon(Icons.help_outline, size: 14, color: Colors.red),
                              SizedBox(width: 4),
                              Text(
                                'Get Help',
                                style: TextStyle(
                                  fontWeight: FontWeight.bold,
                                  color: Colors.red,
                                  fontSize: 12,
                                ),
                              ),
                            ],
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
}
