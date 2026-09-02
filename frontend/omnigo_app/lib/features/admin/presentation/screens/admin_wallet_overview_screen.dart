import 'package:flutter/material.dart';
import 'package:fl_chart/fl_chart.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';

class AdminWalletOverviewScreen extends StatefulWidget {
  const AdminWalletOverviewScreen({super.key});

  @override
  State<AdminWalletOverviewScreen> createState() => _AdminWalletOverviewScreenState();
}

class _AdminWalletOverviewScreenState extends State<AdminWalletOverviewScreen> {
  Map<String, dynamic>? _walletData;
  List<dynamic> _escrowHolds = [];
  bool _isLoading = true;
  final _vendorController = TextEditingController();

  @override
  void initState() {
    super.initState();
    _fetchWalletOverview();
  }

  @override
  void dispose() {
    _vendorController.dispose();
    super.dispose();
  }

  Future<void> _fetchWalletOverview() async {
    setState(() => _isLoading = true);
    try {
      final data = await sl<ApiClient>().get(ApiEndpoints.adminWalletOverview());
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _walletData = data['wallet'] as Map<String, dynamic>?;
          _isLoading = false;
        });
      }
    } catch (e) {
      if (mounted) setState(() => _isLoading = false);
    }
  }

  Future<void> _fetchEscrowHolds(String vendorId) async {
    try {
      final data = await sl<ApiClient>().get(ApiEndpoints.adminEscrowHolds(vendorId));
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _escrowHolds = data['holds'] as List<dynamic>? ?? [];
        });
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed to load escrow holds: $e'), backgroundColor: Colors.red),
        );
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.grey[100],
      appBar: AppBar(
        title: const Text('Wallet Overview', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
      ),
      body: _isLoading
          ? const Center(child: CircularProgressIndicator(color: Colors.black87))
          : _walletData == null
              ? const Center(child: Text('No wallet data'))
              : RefreshIndicator(
                  onRefresh: _fetchWalletOverview,
                  child: ListView(
                    padding: const EdgeInsets.all(16),
                    children: [
                      // Net Exposure Card
                      _buildSummaryCard(
                        'Net Exposure',
                        'PKR ${((_walletData!['net_exposure'] as num?)?.toDouble() ?? 0.0).toStringAsFixed(0)}',
                        Colors.red,
                        Icons.trending_down,
                      ),
                      const SizedBox(height: 16),
                      // Liabilities Section
                      _buildSectionTitle('Liabilities (We Owe)'),
                      const SizedBox(height: 8),
                      _buildStatCard(
                        'Customer Refunds Owed',
                        (_walletData!['liabilities'] as Map<String, dynamic>?)?['customer_refunds_owed'] ?? 0,
                        Colors.orange,
                        Icons.person,
                      ),
                      _buildStatCard(
                        'Vendor Payouts Owed',
                        (_walletData!['liabilities'] as Map<String, dynamic>?)?['vendor_payouts_owed'] ?? 0,
                        Colors.blue,
                        Icons.store,
                      ),
                      _buildStatCard(
                        'Rider Payouts Owed',
                        (_walletData!['liabilities'] as Map<String, dynamic>?)?['rider_payouts_owed'] ?? 0,
                        Colors.teal,
                        Icons.delivery_dining,
                      ),
                      _buildStatCard(
                        'Total Liabilities',
                        (_walletData!['liabilities'] as Map<String, dynamic>?)?['total_liabilities'] ?? 0,
                        Colors.red.shade800,
                        Icons.account_balance,
                      ),
                      const SizedBox(height: 16),
                      // Assets Section
                      _buildSectionTitle('Assets (We Have)'),
                      const SizedBox(height: 8),
                      _buildStatCard(
                        'Rider Cash in Hand',
                        (_walletData!['assets'] as Map<String, dynamic>?)?['rider_cash_in_hand'] ?? 0,
                        Colors.green,
                        Icons.money,
                      ),
                      _buildStatCard(
                        'Total Assets',
                        (_walletData!['assets'] as Map<String, dynamic>?)?['total_assets'] ?? 0,
                        Colors.green.shade800,
                        Icons.account_balance_wallet,
                      ),
                      const SizedBox(height: 16),
                      // Pie Chart
                      if (_walletData!['liabilities'] != null)
                        _buildPieChart(),
                      const SizedBox(height: 16),
                      // Escrow Holds Section
                      _buildSectionTitle('Escrow Holds'),
                      const SizedBox(height: 8),
                      _buildEscrowSearchBar(),
                      if (_escrowHolds.isNotEmpty) ...[
                        const SizedBox(height: 8),
                        ..._escrowHolds.map((hold) => _buildEscrowHoldCard(hold as Map<String, dynamic>)),
                      ],
                    ],
                  ),
                ),
    );
  }

  Widget _buildEscrowSearchBar() {
    return Card(
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Row(
          children: [
            Expanded(
              child: TextField(
                controller: _vendorController,
                decoration: InputDecoration(
                  hintText: 'Vendor ID (e.g., VEND-xxx)',
                  hintStyle: TextStyle(color: Colors.grey.shade400),
                  prefixIcon: const Icon(Icons.store, color: Colors.grey),
                  filled: true,
                  fillColor: Colors.grey[100],
                  border: OutlineInputBorder(
                    borderRadius: BorderRadius.circular(8),
                    borderSide: BorderSide.none,
                  ),
                  contentPadding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
                ),
              ),
            ),
            const SizedBox(width: 8),
            ElevatedButton(
              onPressed: () {
                final vendorId = _vendorController.text.trim();
                if (vendorId.isNotEmpty) _fetchEscrowHolds(vendorId);
              },
              style: ElevatedButton.styleFrom(
                backgroundColor: Colors.black87,
                foregroundColor: Colors.white,
                shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(8)),
              ),
              child: const Text('Search'),
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildEscrowHoldCard(Map<String, dynamic> hold) {
    final orderId = hold['order_id']?.toString() ?? '';
    final amount = (hold['amount'] as num?)?.toDouble() ?? 0;
    final status = hold['status']?.toString() ?? '';
    final holdExpiresAt = hold['hold_expires_at']?.toString() ?? '';

    final statusColor = status == 'held' ? Colors.orange : Colors.green;

    return Card(
      margin: const EdgeInsets.only(bottom: 8),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Row(
          children: [
            Container(
              padding: const EdgeInsets.all(8),
              decoration: BoxDecoration(
                color: statusColor.withOpacity(0.1),
                borderRadius: BorderRadius.circular(8),
              ),
              child: Icon(Icons.lock, color: statusColor, size: 20),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(orderId, style: const TextStyle(fontWeight: FontWeight.bold)),
                  Text(holdExpiresAt.isNotEmpty ? 'Expires: $holdExpiresAt' : '', style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
                ],
              ),
            ),
            Column(
              crossAxisAlignment: CrossAxisAlignment.end,
              children: [
                Text('PKR ${amount.toStringAsFixed(0)}', style: TextStyle(fontWeight: FontWeight.bold, color: statusColor)),
                Text(status.toUpperCase(), style: TextStyle(fontSize: 10, color: statusColor)),
              ],
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildSummaryCard(String title, String value, Color color, IconData icon) {
    return Card(
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
      child: Container(
        padding: const EdgeInsets.all(20),
        decoration: BoxDecoration(
          gradient: LinearGradient(
            colors: [color.withOpacity(0.8), color],
            begin: Alignment.topLeft,
            end: Alignment.bottomRight,
          ),
          borderRadius: BorderRadius.circular(16),
        ),
        child: Row(
          children: [
            Icon(icon, color: Colors.white, size: 40),
            const SizedBox(width: 16),
            Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(title, style: const TextStyle(color: Colors.white70, fontSize: 14)),
                Text(value, style: const TextStyle(color: Colors.white, fontSize: 24, fontWeight: FontWeight.bold)),
              ],
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildSectionTitle(String title) {
    return Text(title, style: const TextStyle(fontSize: 16, fontWeight: FontWeight.bold, color: Colors.black87));
  }

  Widget _buildStatCard(String title, dynamic amount, Color color, IconData icon) {
    final value = (amount as num?)?.toDouble() ?? 0.0;
    return Card(
      margin: const EdgeInsets.only(bottom: 8),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: ListTile(
        leading: CircleAvatar(
          backgroundColor: color.withOpacity(0.1),
          child: Icon(icon, color: color, size: 20),
        ),
        title: Text(title, style: const TextStyle(fontSize: 13)),
        trailing: Text('PKR ${value.toStringAsFixed(0)}', style: TextStyle(
          fontWeight: FontWeight.bold,
          color: color,
          fontSize: 14,
        )),
      ),
    );
  }

  Widget _buildPieChart() {
    final liabilities = _walletData!['liabilities'] as Map<String, dynamic>;
    final customerRefunds = (liabilities['customer_refunds_owed'] as num?)?.toDouble() ?? 0.0;
    final vendorPayouts = (liabilities['vendor_payouts_owed'] as num?)?.toDouble() ?? 0.0;
    final riderPayouts = (liabilities['rider_payouts_owed'] as num?)?.toDouble() ?? 0.0;

    if (customerRefunds + vendorPayouts + riderPayouts == 0) return const SizedBox();

    return Card(
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text('Liability Distribution', style: TextStyle(fontWeight: FontWeight.bold)),
            const SizedBox(height: 16),
            SizedBox(
              height: 200,
              child: PieChart(
                PieChartData(
                  sections: [
                    PieChartSectionData(
                      value: customerRefunds,
                      title: 'Customer\nRefunds',
                      color: Colors.orange,
                      radius: 80,
                      titleStyle: const TextStyle(fontSize: 10, color: Colors.white),
                    ),
                    PieChartSectionData(
                      value: vendorPayouts,
                      title: 'Vendor\nPayouts',
                      color: Colors.blue,
                      radius: 80,
                      titleStyle: const TextStyle(fontSize: 10, color: Colors.white),
                    ),
                    PieChartSectionData(
                      value: riderPayouts,
                      title: 'Rider\nPayouts',
                      color: Colors.teal,
                      radius: 80,
                      titleStyle: const TextStyle(fontSize: 10, color: Colors.white),
                    ),
                  ],
                  sectionsSpace: 2,
                  centerSpaceRadius: 0,
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}
