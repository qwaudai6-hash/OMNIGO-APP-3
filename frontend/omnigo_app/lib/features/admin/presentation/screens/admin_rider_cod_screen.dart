import 'package:flutter/material.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';

class AdminRiderCodScreen extends StatefulWidget {
  const AdminRiderCodScreen({super.key});

  @override
  State<AdminRiderCodScreen> createState() => _AdminRiderCodScreenState();
}

class _AdminRiderCodScreenState extends State<AdminRiderCodScreen> {
  List<dynamic> _riders = [];
  bool _isLoading = true;
  int _totalCount = 0;

  @override
  void initState() {
    super.initState();
    _fetchCodCollection();
  }

  Future<void> _fetchCodCollection() async {
    setState(() => _isLoading = true);
    try {
      final data = await sl<ApiClient>().get(ApiEndpoints.adminRiderCodCollection());
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _riders = data['pending'] as List<dynamic>? ?? [];
          _totalCount = data['count'] as int? ?? 0;
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
        title: const Text('Rider COD Collection', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh, color: Colors.white),
            onPressed: _fetchCodCollection,
          ),
        ],
      ),
      body: _isLoading
          ? const Center(child: CircularProgressIndicator(color: Colors.black87))
          : RefreshIndicator(
              onRefresh: _fetchCodCollection,
              child: Column(
                children: [
                  _buildSummaryCard(),
                  Expanded(
                    child: _riders.isEmpty
                        ? const Center(child: Text('No pending COD collections'))
                        : ListView.builder(
                            padding: const EdgeInsets.all(12),
                            itemCount: _riders.length,
                            itemBuilder: (context, index) => _buildRiderCard(_riders[index] as Map<String, dynamic>),
                          ),
                  ),
                ],
              ),
            ),
    );
  }

  Widget _buildSummaryCard() {
    final totalCash = _riders.fold<double>(0, (sum, r) => sum + ((r['cash_in_hand'] as num?)?.toDouble() ?? 0));
    final totalDebts = _riders.fold<int>(0, (sum, r) => sum + ((r['pending_debts'] as num?)?.toInt() ?? 0));

    return Card(
      margin: const EdgeInsets.all(12),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      color: Colors.red.shade50,
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Row(
          mainAxisAlignment: MainAxisAlignment.spaceAround,
          children: [
            _summaryItem('Riders', '$_totalCount', Icons.delivery_dining, Colors.blue),
            _summaryItem('Total Cash', 'PKR ${totalCash.toStringAsFixed(0)}', Icons.money, Colors.green),
            _summaryItem('Pending Debts', '$totalDebts', Icons.warning, Colors.orange),
          ],
        ),
      ),
    );
  }

  Widget _summaryItem(String label, String value, IconData icon, Color color) {
    return Column(
      children: [
        Icon(icon, color: color, size: 28),
        const SizedBox(height: 4),
        Text(value, style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold, color: color)),
        Text(label, style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
      ],
    );
  }

  Widget _buildRiderCard(Map<String, dynamic> rider) {
    final riderId = rider['rider_tracking_id']?.toString() ?? '';
    final name = rider['name']?.toString() ?? '';
    final cashInHand = (rider['cash_in_hand'] as num?)?.toDouble() ?? 0;
    final pendingDebts = (rider['pending_debts'] as num?)?.toInt() ?? 0;

    return Card(
      margin: const EdgeInsets.only(bottom: 8),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                CircleAvatar(
                  backgroundColor: Colors.blue.shade100,
                  child: Text(name.isNotEmpty ? name[0].toUpperCase() : 'R', style: TextStyle(color: Colors.blue.shade700)),
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(name.isNotEmpty ? name : riderId, style: const TextStyle(fontWeight: FontWeight.bold)),
                      Text(riderId, style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
                    ],
                  ),
                ),
              ],
            ),
            const Divider(),
            Row(
              mainAxisAlignment: MainAxisAlignment.spaceBetween,
              children: [
                _detailChip('Cash in Hand', 'PKR ${cashInHand.toStringAsFixed(0)}', Colors.green),
                _detailChip('Pending Debts', '$pendingDebts', pendingDebts > 0 ? Colors.red : Colors.grey),
              ],
            ),
          ],
        ),
      ),
    );
  }

  Widget _detailChip(String label, String value, Color color) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(label, style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
        const SizedBox(height: 2),
        Text(value, style: TextStyle(fontSize: 14, fontWeight: FontWeight.bold, color: color)),
      ],
    );
  }
}
