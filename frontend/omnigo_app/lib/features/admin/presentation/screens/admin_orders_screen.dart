import 'package:flutter/material.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';
import '../../../../core/theme/app_theme.dart';

class AdminOrdersScreen extends StatefulWidget {
  const AdminOrdersScreen({super.key});

  @override
  State<AdminOrdersScreen> createState() => _AdminOrdersScreenState();
}

class _AdminOrdersScreenState extends State<AdminOrdersScreen> {
  List<dynamic> _orders = [];
  bool _isLoading = true;
  String _selectedStatus = '';
  int _total = 0;
  int _offset = 0;
  final int _limit = 20;
  bool _hasMore = true;
  final _searchController = TextEditingController();

  @override
  void initState() {
    super.initState();
    _fetchOrders();
  }

  @override
  void dispose() {
    _searchController.dispose();
    super.dispose();
  }

  Future<void> _fetchOrders({bool refresh = false}) async {
    if (refresh) {
      _offset = 0;
      _hasMore = true;
    }
    if (!_hasMore && !refresh) return;

    setState(() => _isLoading = true);
    try {
      final data = await sl<ApiClient>().get(
        ApiEndpoints.adminOrders(
          status: _selectedStatus.isEmpty ? null : _selectedStatus,
          limit: _limit,
          offset: _offset,
        ),
      );
      if (data is Map<String, dynamic> && mounted) {
        final orders = data['orders'] as List<dynamic>? ?? [];
        setState(() {
          if (refresh) {
            _orders = orders;
          } else {
            _orders.addAll(orders);
          }
          _total = data['total'] as int? ?? 0;
          _hasMore = orders.length == _limit;
          _offset += orders.length;
          _isLoading = false;
        });
      }
    } catch (e) {
      if (mounted) setState(() => _isLoading = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.grey[100],
      appBar: AppBar(
        title: const Text('Orders Management', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
        bottom: PreferredSize(
          preferredSize: const Size.fromHeight(60),
          child: Padding(
            padding: const EdgeInsets.all(8.0),
            child: Row(
              children: [
                Expanded(
                  child: TextField(
                    controller: _searchController,
                    decoration: InputDecoration(
                      hintText: 'Search by Order ID...',
                      hintStyle: TextStyle(color: Colors.grey.shade400),
                      prefixIcon: const Icon(Icons.search, color: Colors.grey),
                      filled: true,
                      fillColor: Colors.white,
                      border: OutlineInputBorder(
                        borderRadius: BorderRadius.circular(12),
                        borderSide: BorderSide.none,
                      ),
                    ),
                    onSubmitted: (_) => _fetchOrders(refresh: true),
                  ),
                ),
                const SizedBox(width: 8),
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 12),
                  decoration: BoxDecoration(
                    color: Colors.white,
                    borderRadius: BorderRadius.circular(12),
                  ),
                  child: DropdownButton<String>(
                    value: _selectedStatus.isEmpty ? null : _selectedStatus,
                    hint: const Text('All Status'),
                    underline: const SizedBox(),
                    items: const [
                      DropdownMenuItem(value: '', child: Text('All')),
                      DropdownMenuItem(value: 'pending', child: Text('Pending')),
                      DropdownMenuItem(value: 'paid', child: Text('Paid')),
                      DropdownMenuItem(value: 'accepted', child: Text('Accepted')),
                      DropdownMenuItem(value: 'shipped', child: Text('Shipped')),
                      DropdownMenuItem(value: 'in_transit', child: Text('In Transit')),
                      DropdownMenuItem(value: 'delivered', child: Text('Delivered')),
                      DropdownMenuItem(value: 'completed', child: Text('Completed')),
                      DropdownMenuItem(value: 'cancelled', child: Text('Cancelled')),
                      DropdownMenuItem(value: 'refunded', child: Text('Refunded')),
                    ],
                    onChanged: (v) {
                      setState(() => _selectedStatus = v ?? '');
                      _fetchOrders(refresh: true);
                    },
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
      body: _isLoading && _orders.isEmpty
          ? const Center(child: CircularProgressIndicator(color: Colors.black87))
          : RefreshIndicator(
              onRefresh: () => _fetchOrders(refresh: true),
              child: ListView.builder(
                padding: const EdgeInsets.all(12),
                itemCount: _orders.length + (_hasMore ? 1 : 0),
                itemBuilder: (context, index) {
                  if (index == _orders.length) {
                    _fetchOrders();
                    return const Center(child: Padding(
                      padding: EdgeInsets.all(16),
                      child: CircularProgressIndicator(color: Colors.black87),
                    ));
                  }
                  return _buildOrderCard(_orders[index] as Map<String, dynamic>);
                },
              ),
            ),
    );
  }

  Widget _buildOrderCard(Map<String, dynamic> order) {
    final orderId = order['order_tracking_id']?.toString() ?? '';
    final status = order['status']?.toString() ?? 'unknown';
    final amount = (order['total_amount'] as num?)?.toDouble() ?? 0.0;
    final paymentStatus = order['payment_status']?.toString() ?? '';
    final createdAt = order['created_at']?.toString() ?? '';
    final customerId = order['customer_tracking_id']?.toString() ?? '';
    final customerName = order['customer_name']?.toString()?.trim() ?? '';
    final customerPhone = order['customer_phone']?.toString() ?? '';
    final customerLat = (order['customer_lat'] as num?)?.toDouble() ?? 0.0;
    final customerLng = (order['customer_lng'] as num?)?.toDouble() ?? 0.0;
    final vendorId = order['vendor_tracking_id']?.toString() ?? '';
    final storeId = order['store_tracking_id']?.toString() ?? '';
    final storeName = order['store_name']?.toString() ?? '';
    final storeLat = (order['store_lat'] as num?)?.toDouble() ?? 0.0;
    final storeLng = (order['store_lng'] as num?)?.toDouble() ?? 0.0;
    final riderId = order['rider_tracking_id']?.toString() ?? '';
    final riderName = order['rider_name']?.toString()?.trim() ?? '';
    final riderPhone = order['rider_phone']?.toString() ?? '';
    final commission = (order['admin_commission'] as num?)?.toDouble() ?? 0.0;

    final paymentColor = paymentStatus == 'paid' ? Colors.green
        : paymentStatus == 'pending' ? Colors.orange
        : Colors.grey;

    return Card(
      margin: const EdgeInsets.only(bottom: 8),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: ExpansionTile(
        tilePadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
        childrenPadding: const EdgeInsets.all(16),
        title: Row(
          children: [
            Container(
              padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
              decoration: BoxDecoration(
                color: _statusColor(status).withOpacity(0.1),
                borderRadius: BorderRadius.circular(8),
              ),
              child: Text(status.toUpperCase(), style: TextStyle(
                color: _statusColor(status),
                fontSize: 11,
                fontWeight: FontWeight.bold,
              )),
            ),
            const SizedBox(width: 8),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(orderId, style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 13)),
                  const SizedBox(height: 2),
                  Text(
                    '${customerName.isNotEmpty ? customerName : customerId}',
                    style: TextStyle(color: Colors.grey.shade600, fontSize: 11),
                  ),
                ],
              ),
            ),
          ],
        ),
        subtitle: Padding(
          padding: const EdgeInsets.only(top: 4),
          child: Row(
            children: [
              Text('PKR ${amount.toStringAsFixed(0)}', style: TextStyle(color: Colors.grey.shade600, fontSize: 12)),
              const SizedBox(width: 8),
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
                decoration: BoxDecoration(
                  color: paymentColor.withOpacity(0.1),
                  borderRadius: BorderRadius.circular(6),
                ),
                child: Text(paymentStatus, style: TextStyle(color: paymentColor, fontSize: 10, fontWeight: FontWeight.bold)),
              ),
            ],
          ),
        ),
        children: [
          // Customer info
          if (customerName.isNotEmpty || customerPhone.isNotEmpty)
            _infoSection('Customer', [
              if (customerName.isNotEmpty) _detailRow('Name', customerName),
              if (customerPhone.isNotEmpty) _detailRow('Phone', customerPhone),
              _detailRow('ID', customerId),
              if (customerLat != 0 && customerLng != 0)
                _detailRow('Delivery Location', '${customerLat.toStringAsFixed(4)}, ${customerLng.toStringAsFixed(4)}'),
            ]),
          // Store info
          if (storeName.isNotEmpty || storeLat != 0)
            _infoSection('Store (Pickup)', [
              if (storeName.isNotEmpty) _detailRow('Store', storeName),
              _detailRow('Store ID', storeId),
              _detailRow('Vendor ID', vendorId),
              if (storeLat != 0 && storeLng != 0)
                _detailRow('Store Location', '${storeLat.toStringAsFixed(4)}, ${storeLng.toStringAsFixed(4)}'),
            ]),
          // Rider info
          if (riderName.isNotEmpty || riderPhone.isNotEmpty || riderId.isNotEmpty)
            _infoSection('Rider (Delivery)', [
              if (riderName.isNotEmpty) _detailRow('Rider', riderName),
              if (riderPhone.isNotEmpty) _detailRow('Phone', riderPhone),
              _detailRow('Rider ID', riderId),
            ]),
          // Financial
          _infoSection('Financials', [
            _detailRow('Total', 'PKR ${amount.toStringAsFixed(0)}'),
            _detailRow('Admin Commission', 'PKR ${commission.toStringAsFixed(0)}'),
            _detailRow('Vendor Escrow', 'PKR ${((order['vendor_escrow'] as num?)?.toDouble() ?? 0).toStringAsFixed(0)}'),
            _detailRow('Delivery Escrow', 'PKR ${((order['delivery_escrow'] as num?)?.toDouble() ?? 0).toStringAsFixed(0)}'),
            _detailRow('Escrow Released', order['escrow_released'] == true ? 'Yes' : 'No'),
          ]),
          _detailRow('Created', createdAt),
          const SizedBox(height: 8),
          // Action buttons
          Row(
            children: [
              Expanded(
                child: OutlinedButton.icon(
                  onPressed: () => _showStatusEditor(order),
                  icon: const Icon(Icons.edit, size: 16),
                  label: const Text('Edit Status'),
                  style: OutlinedButton.styleFrom(foregroundColor: Colors.blue),
                ),
              ),
              const SizedBox(width: 8),
              Expanded(
                child: OutlinedButton.icon(
                  onPressed: status == 'cancelled' || status == 'refunded' ? null : () => _cancelOrder(orderId),
                  icon: const Icon(Icons.cancel, size: 16),
                  label: const Text('Cancel'),
                  style: OutlinedButton.styleFrom(foregroundColor: Colors.red),
                ),
              ),
              const SizedBox(width: 8),
              Expanded(
                child: OutlinedButton.icon(
                  onPressed: paymentStatus == 'paid' || paymentStatus == 'completed' ? () => _refundOrder(orderId) : null,
                  icon: const Icon(Icons.replay, size: 16),
                  label: const Text('Refund'),
                  style: OutlinedButton.styleFrom(foregroundColor: Colors.orange),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }

  Widget _infoSection(String title, List<Widget> children) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Padding(
          padding: const EdgeInsets.only(bottom: 4),
          child: Text(title, style: TextStyle(color: Colors.grey.shade800, fontSize: 13, fontWeight: FontWeight.bold)),
        ),
        ...children,
        const Divider(),
      ],
    );
  }

  Widget _detailRow(String label, String value) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 3),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceBetween,
        children: [
          Text(label, style: TextStyle(color: Colors.grey.shade600, fontSize: 12)),
          Text(value, style: const TextStyle(fontSize: 12, fontWeight: FontWeight.w500)),
        ],
      ),
    );
  }

  Color _statusColor(String status) {
    switch (status) {
      case 'pending': return Colors.orange;
      case 'paid': return Colors.blue;
      case 'accepted': return Colors.indigo;
      case 'shipped': return Colors.purple;
      case 'in_transit': return Colors.teal;
      case 'delivered': return Colors.green;
      case 'completed': return Colors.green.shade800;
      case 'cancelled': return Colors.red;
      case 'refunded': return Colors.orange.shade800;
      case 'failed': return Colors.red.shade800;
      default: return Colors.grey;
    }
  }

  Future<void> _showStatusEditor(Map<String, dynamic> order) async {
    final currentStatus = order['status']?.toString() ?? '';
    final orderId = order['order_tracking_id']?.toString() ?? '';
    String newStatus = currentStatus;

    final result = await showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: Text('Edit Status: $orderId'),
        content: DropdownButtonFormField<String>(
          value: newStatus,
          decoration: const InputDecoration(border: OutlineInputBorder()),
          items: const [
            DropdownMenuItem(value: 'pending', child: Text('Pending')),
            DropdownMenuItem(value: 'paid', child: Text('Paid')),
            DropdownMenuItem(value: 'accepted', child: Text('Accepted')),
            DropdownMenuItem(value: 'shipped', child: Text('Shipped')),
            DropdownMenuItem(value: 'in_transit', child: Text('In Transit')),
            DropdownMenuItem(value: 'delivered', child: Text('Delivered')),
            DropdownMenuItem(value: 'completed', child: Text('Completed')),
            DropdownMenuItem(value: 'cancelled', child: Text('Cancelled')),
            DropdownMenuItem(value: 'refunded', child: Text('Refunded')),
          ],
          onChanged: (v) => newStatus = v ?? currentStatus,
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('Cancel')),
          TextButton(
            onPressed: () => Navigator.pop(ctx, newStatus),
            child: const Text('Save', style: TextStyle(color: AppTheme.limeAccent)),
          ),
        ],
      ),
    );

    if (result != null && result != currentStatus) {
      await _updateOrderStatus(orderId, result);
    }
  }

  Future<void> _updateOrderStatus(String orderId, String status) async {
    try {
      await sl<ApiClient>().patch('/admin/orders/$orderId/status', {'status': status});
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Order $orderId status updated to $status'), backgroundColor: Colors.green),
        );
        _fetchOrders(refresh: true);
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed to update: $e'), backgroundColor: Colors.redAccent),
        );
      }
    }
  }

  Future<void> _cancelOrder(String orderId) async {
    final confirm = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Cancel Order?'),
        content: Text('Are you sure you want to cancel $orderId?'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('No')),
          TextButton(
            onPressed: () => Navigator.pop(ctx, true),
            style: TextButton.styleFrom(foregroundColor: Colors.red),
            child: const Text('Yes, Cancel'),
          ),
        ],
      ),
    );
    if (confirm == true) await _updateOrderStatus(orderId, 'cancelled');
  }

  Future<void> _refundOrder(String orderId) async {
    final confirm = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Refund Order?'),
        content: Text('Are you sure you want to refund $orderId?'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('No')),
          TextButton(
            onPressed: () => Navigator.pop(ctx, true),
            style: TextButton.styleFrom(foregroundColor: Colors.orange),
            child: const Text('Yes, Refund'),
          ),
        ],
      ),
    );
    if (confirm == true) await _updateOrderStatus(orderId, 'refunded');
  }
}
