import 'package:flutter/material.dart';
import 'package:fl_chart/fl_chart.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';
import '../../../../core/theme/app_theme.dart';

class AdminAnalyticsScreen extends StatefulWidget {
  const AdminAnalyticsScreen({super.key});

  @override
  State<AdminAnalyticsScreen> createState() => _AdminAnalyticsScreenState();
}

class _AdminAnalyticsScreenState extends State<AdminAnalyticsScreen> {
  Map<String, dynamic>? _analytics;
  List<dynamic> _dailyRevenue = [];
  List<dynamic> _orders = [];
  bool _isLoading = true;
  int _selectedDays = 7;
  int _selectedTab = 0;

  @override
  void initState() {
    super.initState();
    _fetchAllData();
  }

  Future<void> _fetchAllData() async {
    setState(() => _isLoading = true);
    try {
      final api = sl<ApiClient>();

      final analytics = await api.get(ApiEndpoints.adminAnalyticsOverview(days: _selectedDays));
      final revenue = await api.get(ApiEndpoints.adminFinanceDailyRevenue(days: _selectedDays));
      final orders = await api.get(ApiEndpoints.adminOrders(limit: 20));

      if (mounted) {
        setState(() {
          _analytics = analytics is Map<String, dynamic> ? analytics : null;
          _dailyRevenue = (revenue is Map<String, dynamic> ? revenue['daily_revenue'] : null) as List<dynamic>? ?? [];
          _orders = (orders is Map<String, dynamic> ? orders['orders'] : null) as List<dynamic>? ?? [];
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
        title: const Text('Analytics Dashboard', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
        bottom: PreferredSize(
          preferredSize: const Size.fromHeight(50),
          child: Row(
            mainAxisAlignment: MainAxisAlignment.center,
            children: [7, 14, 30, 90].map((days) {
              final isSelected = _selectedDays == days;
              return Padding(
                padding: const EdgeInsets.symmetric(horizontal: 4, vertical: 8),
                child: ChoiceChip(
                  label: Text('${days}D', style: TextStyle(color: isSelected ? Colors.white : Colors.black87)),
                  selected: isSelected,
                  selectedColor: Colors.black87,
                  onSelected: (_) {
                    setState(() => _selectedDays = days);
                    _fetchAllData();
                  },
                ),
              );
            }).toList(),
          ),
        ),
      ),
      body: _isLoading
          ? const Center(child: CircularProgressIndicator(color: Colors.black87))
          : RefreshIndicator(
              onRefresh: _fetchAllData,
              child: ListView(
                padding: const EdgeInsets.all(12),
                children: [
                  // KPI Cards
                  if (_analytics != null) _buildKPICards(),
                  const SizedBox(height: 16),
                  // Revenue Chart
                  _buildRevenueChart(),
                  const SizedBox(height: 16),
                  // Tab selector
                  _buildTabSelector(),
                  const SizedBox(height: 12),
                  // Tab content
                  if (_selectedTab == 0) _buildOrderTrackingList(),
                  if (_selectedTab == 1) _buildPaymentHistoryList(),
                  if (_selectedTab == 2) _buildHeatmapsSection(),
                ],
              ),
            ),
    );
  }

  Widget _buildKPICards() {
    final totalOrders = _analytics!['total_orders'] ?? 0;
    final totalRevenue = (_analytics!['total_revenue'] as num?)?.toDouble() ?? 0.0;
    final newUsers = _analytics!['new_users'] ?? 0;
    final activeRiders = _analytics!['active_riders'] ?? 0;

    return GridView.count(
      crossAxisCount: 2,
      shrinkWrap: true,
      physics: const NeverScrollableScrollPhysics(),
      childAspectRatio: 1.5,
      children: [
        _kpiCard('Total Orders', '$totalOrders', Icons.shopping_cart, Colors.blue),
        _kpiCard('Revenue', 'PKR ${totalRevenue.toStringAsFixed(0)}', Icons.attach_money, Colors.green),
        _kpiCard('New Users', '$newUsers', Icons.person_add, Colors.orange),
        _kpiCard('Active Riders', '$activeRiders', Icons.delivery_dining, Colors.teal),
      ],
    );
  }

  Widget _kpiCard(String title, String value, IconData icon, Color color) {
    return Card(
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            Icon(icon, color: color, size: 24),
            const SizedBox(height: 4),
            Text(title, style: TextStyle(color: Colors.grey.shade600, fontSize: 11)),
            Text(value, style: TextStyle(fontWeight: FontWeight.bold, fontSize: 16, color: color)),
          ],
        ),
      ),
    );
  }

  Widget _buildRevenueChart() {
    if (_dailyRevenue.isEmpty) return const SizedBox();

    final spots = <FlSpot>[];
    for (int i = 0; i < _dailyRevenue.length; i++) {
      final amount = (_dailyRevenue[i]['gross_volume'] as num?)?.toDouble() ?? 0.0;
      spots.add(FlSpot(i.toDouble(), amount));
    }

    return Card(
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text('Revenue Trend', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 16)),
            const SizedBox(height: 16),
            SizedBox(
              height: 200,
              child: LineChart(
                LineChartData(
                  gridData: const FlGridData(show: true),
                  titlesData: const FlTitlesData(show: false),
                  borderData: FlBorderData(show: false),
                  lineBarsData: [
                    LineChartBarData(
                      spots: spots,
                      isCurved: true,
                      color: AppTheme.limeAccent,
                      barWidth: 3,
                      isStrokeCapRound: true,
                      belowBarData: BarAreaData(
                        show: true,
                        color: AppTheme.limeAccent.withOpacity(0.1),
                      ),
                    ),
                  ],
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildTabSelector() {
    return Row(
      children: [
        Expanded(
          child: GestureDetector(
            onTap: () => setState(() => _selectedTab = 0),
            child: Container(
              padding: const EdgeInsets.symmetric(vertical: 12),
              decoration: BoxDecoration(
                color: _selectedTab == 0 ? Colors.black87 : Colors.white,
                borderRadius: BorderRadius.circular(12),
              ),
              child: Center(
                child: Text('Orders', style: TextStyle(
                  color: _selectedTab == 0 ? Colors.white : Colors.black87,
                  fontWeight: FontWeight.bold,
                )),
              ),
            ),
          ),
        ),
        const SizedBox(width: 8),
        Expanded(
          child: GestureDetector(
            onTap: () => setState(() => _selectedTab = 1),
            child: Container(
              padding: const EdgeInsets.symmetric(vertical: 12),
              decoration: BoxDecoration(
                color: _selectedTab == 1 ? Colors.black87 : Colors.white,
                borderRadius: BorderRadius.circular(12),
              ),
              child: Center(
                child: Text('Payments', style: TextStyle(
                  color: _selectedTab == 1 ? Colors.white : Colors.black87,
                  fontWeight: FontWeight.bold,
                )),
              ),
            ),
          ),
        ),
        const SizedBox(width: 8),
        Expanded(
          child: GestureDetector(
            onTap: () => setState(() => _selectedTab = 2),
            child: Container(
              padding: const EdgeInsets.symmetric(vertical: 12),
              decoration: BoxDecoration(
                color: _selectedTab == 2 ? Colors.black87 : Colors.white,
                borderRadius: BorderRadius.circular(12),
              ),
              child: Center(
                child: Text('Heatmaps', style: TextStyle(
                  color: _selectedTab == 2 ? Colors.white : Colors.black87,
                  fontWeight: FontWeight.bold,
                )),
              ),
            ),
          ),
        ),
      ],
    );
  }

  Widget _buildOrderTrackingList() {
    if (_orders.isEmpty) return const Center(child: Text('No orders found'));

    return Column(
      children: _orders.map((order) => _buildOrderTrackingCard(order as Map<String, dynamic>)).toList(),
    );
  }

  Widget _buildOrderTrackingCard(Map<String, dynamic> order) {
    final orderId = order['order_tracking_id']?.toString() ?? '';
    final status = order['status']?.toString() ?? '';
    final amount = (order['total_amount'] as num?)?.toDouble() ?? 0.0;
    final paymentGateway = order['payment_gateway']?.toString() ?? '';
    final paymentStatus = order['payment_status']?.toString() ?? '';
    final escrowReleased = order['escrow_released'] == true;
    final disputeStatus = order['dispute_status']?.toString() ?? '';
    final customerId = order['customer_tracking_id']?.toString() ?? '';
    final vendorId = order['vendor_tracking_id']?.toString() ?? '';
    final storeId = order['store_tracking_id']?.toString() ?? '';
    final riderId = order['rider_tracking_id']?.toString() ?? '';
    final commission = (order['admin_commission'] as num?)?.toDouble() ?? 0.0;
    final vendorEscrow = (order['vendor_escrow'] as num?)?.toDouble() ?? 0.0;
    final deliveryEscrow = (order['delivery_escrow'] as num?)?.toDouble() ?? 0.0;
    final createdAt = order['created_at']?.toString() ?? '';
    final deliveredAt = order['delivered_at']?.toString() ?? '';

    final isCOD = paymentGateway.toLowerCase() == 'cod';

    return Card(
      margin: const EdgeInsets.only(bottom: 8),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: ExpansionTile(
        tilePadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 4),
        childrenPadding: const EdgeInsets.all(16),
        title: Row(
          children: [
            // Status badge
            Container(
              padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 3),
              decoration: BoxDecoration(
                color: _statusColor(status).withOpacity(0.1),
                borderRadius: BorderRadius.circular(6),
              ),
              child: Text(status.toUpperCase(), style: TextStyle(
                color: _statusColor(status),
                fontSize: 9,
                fontWeight: FontWeight.bold,
              )),
            ),
            const SizedBox(width: 6),
            // Payment method badge
            Container(
              padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 3),
              decoration: BoxDecoration(
                color: _paymentMethodColor(paymentGateway).withOpacity(0.1),
                borderRadius: BorderRadius.circular(6),
              ),
              child: Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Icon(_paymentMethodIcon(paymentGateway), size: 10, color: _paymentMethodColor(paymentGateway)),
                  const SizedBox(width: 3),
                  Text(paymentGateway.toUpperCase(), style: TextStyle(
                    color: _paymentMethodColor(paymentGateway),
                    fontSize: 9,
                    fontWeight: FontWeight.bold,
                  )),
                ],
              ),
            ),
            const SizedBox(width: 6),
            // Payment status
            Container(
              padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 3),
              decoration: BoxDecoration(
                color: _paymentStatusColor(paymentStatus).withOpacity(0.1),
                borderRadius: BorderRadius.circular(6),
              ),
              child: Text(paymentStatus.toUpperCase(), style: TextStyle(
                color: _paymentStatusColor(paymentStatus),
                fontSize: 9,
                fontWeight: FontWeight.bold,
              )),
            ),
            if (disputeStatus.isNotEmpty && disputeStatus != 'none') ...[
              const SizedBox(width: 6),
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 3),
                decoration: BoxDecoration(
                  color: Colors.red.withOpacity(0.1),
                  borderRadius: BorderRadius.circular(6),
                ),
                child: Text('DISPUTED', style: TextStyle(color: Colors.red.shade700, fontSize: 9, fontWeight: FontWeight.bold)),
              ),
            ],
          ],
        ),
        subtitle: Padding(
          padding: const EdgeInsets.only(top: 4),
          child: Text('PKR ${amount.toStringAsFixed(0)} • $orderId', style: TextStyle(color: Colors.grey.shade600, fontSize: 11)),
        ),
        children: [
          // Order & IDs
          _trackingRow('Order ID', orderId),
          _trackingRow('Customer ID', customerId),
          _trackingRow('Vendor ID', vendorId),
          _trackingRow('Store ID', storeId),
          _trackingRow('Rider ID', riderId),
          const Divider(),
          // Payment Details
          const Text('Payment Details', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 12)),
          const SizedBox(height: 4),
          _trackingRow('Amount', 'PKR ${amount.toStringAsFixed(0)}'),
          _trackingRow('Payment Method', paymentGateway),
          _trackingRow('Payment Status', paymentStatus),
          if (isCOD) ...[
            _trackingRow('COD Order', 'Yes — Rider collects cash'),
          ],
          const Divider(),
          // Vendor Payment Status
          const Text('Vendor Payment', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 12)),
          const SizedBox(height: 4),
          if (escrowReleased) ...[
            _trackingRow('Status', '✅ Escrow Released → Wallet'),
            _trackingRow('In Wallet', 'PKR ${vendorEscrow.toStringAsFixed(0)}'),
            _trackingRow('Note', 'Vendor can withdraw anytime'),
          ] else ...[
            _trackingRow('Status', '🔒 In Escrow Hold'),
            _trackingRow('Held Amount', 'PKR ${vendorEscrow.toStringAsFixed(0)}'),
            _trackingRow('Note', 'Released after delivery confirmation'),
          ],
          const Divider(),
          // Rider COD Status (only for COD orders)
          if (isCOD) ...[
            const Text('Rider COD Status', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 12)),
            const SizedBox(height: 4),
            _trackingRow('Delivery Escrow', 'PKR ${deliveryEscrow.toStringAsFixed(0)}'),
            if (status == 'completed' || status == 'delivered') ...[
              _trackingRow('Cash Collected', 'Yes — delivered'),
              _trackingRow('Platform Share', 'PKR ${deliveryEscrow.toStringAsFixed(0)}'),
              _trackingRow('Note', 'Rider deposits via JazzCash/EasyPaisa'),
            ] else if (status == 'in_transit') ...[
              _trackingRow('Cash Collected', 'Pending — in transit'),
              _trackingRow('Note', 'Rider will collect on delivery'),
            ] else ...[
              _trackingRow('Cash Collected', 'Not yet'),
            ],
          ],
          const Divider(),
          // Admin Commission
          const Text('Finance', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 12)),
          const SizedBox(height: 4),
          _trackingRow('Admin Commission', 'PKR ${commission.toStringAsFixed(0)}'),
          if (disputeStatus.isNotEmpty && disputeStatus != 'none')
            _trackingRow('Dispute Status', disputeStatus),
          const Divider(),
          // Timeline
          _trackingRow('Created', createdAt),
          if (deliveredAt.isNotEmpty && deliveredAt != '0001-01-01T00:00:00Z')
            _trackingRow('Delivered', deliveredAt),
          const SizedBox(height: 8),
          SizedBox(
            width: double.infinity,
            child: OutlinedButton.icon(
              onPressed: () => _showOrderDetail(orderId),
              icon: const Icon(Icons.timeline, size: 16),
              label: const Text('View Full Lineage'),
            ),
          ),
        ],
      ),
    );
  }

  Widget _trackingRow(String label, String value) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceBetween,
        children: [
          Text(label, style: TextStyle(color: Colors.grey.shade600, fontSize: 12)),
          Text(value, style: const TextStyle(fontSize: 12, fontWeight: FontWeight.w500)),
        ],
      ),
    );
  }

  Widget _buildPaymentHistoryList() {
    if (_dailyRevenue.isEmpty) return const Center(child: Text('No revenue data'));

    return Column(
      children: _dailyRevenue.map((day) => _buildPaymentDayCard(day as Map<String, dynamic>)).toList(),
    );
  }

  Widget _buildPaymentDayCard(Map<String, dynamic> day) {
    final date = day['date']?.toString() ?? '';
    final grossVolume = (day['gross_volume'] as num?)?.toDouble() ?? 0.0;
    final platformRevenue = (day['platform_revenue'] as num?)?.toDouble() ?? 0.0;
    final orderCount = day['order_count'] ?? 0;

    return Card(
      margin: const EdgeInsets.only(bottom: 8),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: ListTile(
        leading: CircleAvatar(
          backgroundColor: Colors.green.withOpacity(0.1),
          child: const Icon(Icons.attach_money, color: Colors.green, size: 20),
        ),
        title: Text(date, style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 13)),
        subtitle: Text('$orderCount orders', style: TextStyle(color: Colors.grey.shade600, fontSize: 11)),
        trailing: Column(
          mainAxisAlignment: MainAxisAlignment.center,
          crossAxisAlignment: CrossAxisAlignment.end,
          children: [
            Text('PKR ${grossVolume.toStringAsFixed(0)}', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 13)),
            Text('Revenue: PKR ${platformRevenue.toStringAsFixed(0)}', style: TextStyle(color: Colors.green.shade700, fontSize: 10)),
          ],
        ),
      ),
    );
  }

  Color _paymentStatusColor(String status) {
    switch (status) {
      case 'completed': return Colors.green;
      case 'paid': return Colors.blue;
      case 'pending': return Colors.orange;
      case 'failed': return Colors.red;
      default: return Colors.grey;
    }
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

  Color _paymentMethodColor(String method) {
    switch (method.toLowerCase()) {
      case 'cod': return Colors.orange;
      case 'payfast': return Colors.blue;
      case 'jazzcash': return Colors.red;
      case 'easypaisa': return Colors.green;
      case 'stripe': return Colors.purple;
      case 'wallet': return Colors.teal;
      default: return Colors.grey;
    }
  }

  IconData _paymentMethodIcon(String method) {
    switch (method.toLowerCase()) {
      case 'cod': return Icons.money;
      case 'payfast': return Icons.credit_card;
      case 'jazzcash': return Icons.phone_android;
      case 'easypaisa': return Icons.phone_android;
      case 'stripe': return Icons.credit_card;
      case 'wallet': return Icons.account_balance_wallet;
      default: return Icons.payment;
    }
  }

  Future<void> _showOrderDetail(String orderId) async {
    try {
      final data = await sl<ApiClient>().get(ApiEndpoints.adminLineageFull(orderId));
      if (data is Map<String, dynamic> && mounted) {
        _showLineageDialog(data);
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed to load lineage: $e'), backgroundColor: Colors.redAccent),
        );
      }
    }
  }

  void _showLineageDialog(Map<String, dynamic> lineage) {
    showDialog(
      context: context,
      builder: (ctx) => Dialog(
        child: ConstrainedBox(
          constraints: BoxConstraints(
            maxWidth: MediaQuery.of(context).size.width * 0.9,
            maxHeight: MediaQuery.of(context).size.height * 0.8,
          ),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              AppBar(
                title: const Text('Order Lineage', style: TextStyle(color: Colors.white)),
                backgroundColor: Colors.black87,
                leading: IconButton(
                  icon: const Icon(Icons.close, color: Colors.white),
                  onPressed: () => Navigator.pop(ctx),
                ),
              ),
              Expanded(
                child: ListView(
                  padding: const EdgeInsets.all(16),
                  children: [
                    _lineageSection('Order Info', [
                      _lineageRow('Order ID', lineage['order_id']?.toString() ?? ''),
                      _lineageRow('Status', lineage['order_status']?.toString() ?? ''),
                      _lineageRow('Total Amount', 'PKR ${((lineage['total_amount'] as num?)?.toDouble() ?? 0).toStringAsFixed(0)}'),
                    ]),
                    _lineageSection('Customer', [
                      _lineageRow('Customer ID', lineage['customer_id']?.toString() ?? ''),
                      if (lineage['customer_name']?.toString().isNotEmpty == true)
                        _lineageRow('Name', lineage['customer_name']?.toString() ?? ''),
                      if (lineage['customer_phone']?.toString().isNotEmpty == true)
                        _lineageRow('Phone', lineage['customer_phone']?.toString() ?? ''),
                      if (((lineage['customer_lat'] as num?)?.toDouble() ?? 0) != 0)
                        _lineageRow('Delivery Location', '${((lineage['customer_lat'] as num?)?.toDouble() ?? 0).toStringAsFixed(4)}, ${((lineage['customer_lng'] as num?)?.toDouble() ?? 0).toStringAsFixed(4)}'),
                    ]),
                    _lineageSection('Store (Pickup Location)', [
                      _lineageRow('Store ID', lineage['store_id']?.toString() ?? ''),
                      if (lineage['store_name']?.toString().isNotEmpty == true)
                        _lineageRow('Store Name', lineage['store_name']?.toString() ?? ''),
                      if (((lineage['store_lat'] as num?)?.toDouble() ?? 0) != 0)
                        _lineageRow('Store Location', '${((lineage['store_lat'] as num?)?.toDouble() ?? 0).toStringAsFixed(4)}, ${((lineage['store_lng'] as num?)?.toDouble() ?? 0).toStringAsFixed(4)}'),
                    ]),
                    _lineageSection('Rider (Delivery)', [
                      _lineageRow('Rider ID', lineage['rider_id']?.toString() ?? ''),
                      if (lineage['rider_name']?.toString().isNotEmpty == true)
                        _lineageRow('Rider Name', lineage['rider_name']?.toString() ?? ''),
                      if (lineage['rider_phone']?.toString().isNotEmpty == true)
                        _lineageRow('Phone', lineage['rider_phone']?.toString() ?? ''),
                      _lineageRow('Delivery Status', lineage['delivery_status']?.toString() ?? ''),
                    ]),
                    if (lineage['items'] != null) ...[
                      _lineageSection('Items', (lineage['items'] as List<dynamic>).map((item) {
                        return _lineageRow(
                          '${item['product_name'] ?? ''} x${item['quantity'] ?? 0}',
                          'PKR ${((item['subtotal'] as num?)?.toDouble() ?? 0).toStringAsFixed(0)}',
                        );
                      }).toList()),
                    ],
                    if (lineage['timeline'] != null) ...[
                      _lineageSection('Timeline', (lineage['timeline'] as List<dynamic>).map((event) {
                        return _lineageRow(
                          '${event['entity'] ?? ''} (${event['entity_id'] ?? ''})',
                          '${event['status'] ?? ''}',
                        );
                      }).toList()),
                    ],
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }

  Widget _lineageSection(String title, List<Widget> children) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        const Divider(),
        Text(title, style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 14)),
        const SizedBox(height: 4),
        ...children,
      ],
    );
  }

  Widget _lineageRow(String label, String value) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceBetween,
        children: [
          Text(label, style: TextStyle(color: Colors.grey.shade600, fontSize: 12)),
          Flexible(child: Text(value, style: const TextStyle(fontSize: 12, fontWeight: FontWeight.w500), textAlign: TextAlign.end)),
        ],
      ),
    );
  }

  Widget _buildHeatmapsSection() {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _buildHeatmapCard(
          'Delivery Heatmap',
          'Delivery locations concentration',
          ApiEndpoints.adminAnalyticsDeliveryHeatmap(days: _selectedDays),
          Colors.green,
          Icons.delivery_dining,
        ),
        const SizedBox(height: 12),
        _buildHeatmapCard(
          'Vendor Heatmap',
          'Vendor store locations',
          ApiEndpoints.adminAnalyticsVendorHeatmap(days: _selectedDays),
          Colors.blue,
          Icons.store,
        ),
        const SizedBox(height: 12),
        _buildHeatmapCard(
          'Demand Heatmap',
          'Order demand concentration',
          ApiEndpoints.adminDemandHeatmap(),
          Colors.orange,
          Icons.trending_up,
        ),
      ],
    );
  }

  Widget _buildHeatmapCard(String title, String subtitle, String endpoint, Color color, IconData icon) {
    return Card(
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Container(
                  padding: const EdgeInsets.all(8),
                  decoration: BoxDecoration(
                    color: color.withOpacity(0.1),
                    borderRadius: BorderRadius.circular(8),
                  ),
                  child: Icon(icon, color: color, size: 24),
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(title, style: const TextStyle(fontWeight: FontWeight.bold)),
                      Text(subtitle, style: TextStyle(fontSize: 12, color: Colors.grey.shade600)),
                    ],
                  ),
                ),
              ],
            ),
            const SizedBox(height: 12),
            Container(
              height: 150,
              width: double.infinity,
              decoration: BoxDecoration(
                color: Colors.grey[200],
                borderRadius: BorderRadius.circular(8),
              ),
              child: Center(
                child: Column(
                  mainAxisAlignment: MainAxisAlignment.center,
                  children: [
                    Icon(Icons.map, color: Colors.grey.shade400, size: 40),
                    const SizedBox(height: 8),
                    Text('Map View', style: TextStyle(color: Colors.grey.shade600)),
                    const SizedBox(height: 4),
                    Text('Google Maps integration', style: TextStyle(color: Colors.grey.shade500, fontSize: 11)),
                  ],
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}
