import 'package:flutter/material.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/network/api_client.dart';
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

        bool matchesSearch = query.isEmpty ||
            orderId.contains(query) ||
            storeId.contains(query);

        return matchesTab && matchesSearch;
      }).toList();
    });
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
          'Order',
          style: TextStyle(
            color: AppTheme.blackAccent,
            fontWeight: FontWeight.bold,
          ),
        ),
        bottom: PreferredSize(
          preferredSize: const Size.fromHeight(110),
          child: Column(
            children: [
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
    final paymentMethod = order['payment_method']?.toString() ?? 'Unknown';

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
            ),
          ],
        ),
      ),
    );
  }
}
