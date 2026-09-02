import 'package:flutter/material.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';

class AdminReconciliationScreen extends StatefulWidget {
  const AdminReconciliationScreen({super.key});

  @override
  State<AdminReconciliationScreen> createState() => _AdminReconciliationScreenState();
}

class _AdminReconciliationScreenState extends State<AdminReconciliationScreen> {
  bool _isRunning = false;
  Map<String, dynamic>? _result;

  Future<void> _runReconciliation() async {
    setState(() { _isRunning = true; _result = null; });
    try {
      final data = await sl<ApiClient>().post(ApiEndpoints.adminReconcile(), {});
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _result = data;
          _isRunning = false;
        });
      }
    } catch (e) {
      if (mounted) setState(() => _isRunning = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.grey[100],
      appBar: AppBar(
        title: const Text('Financial Reconciliation', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
      ),
      body: SingleChildScrollView(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Card(
              shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
              child: Padding(
                padding: const EdgeInsets.all(16),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    const Text('Ledger vs Database Reconciliation', style: TextStyle(fontSize: 16, fontWeight: FontWeight.bold)),
                    const SizedBox(height: 8),
                    Text(
                      'Compare TigerBeetle ledger entries with database records to find discrepancies.',
                      style: TextStyle(color: Colors.grey.shade600),
                    ),
                    const SizedBox(height: 16),
                    SizedBox(
                      width: double.infinity,
                      child: ElevatedButton.icon(
                        onPressed: _isRunning ? null : _runReconciliation,
                        icon: _isRunning
                            ? const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2, color: Colors.white))
                            : const Icon(Icons.play_arrow),
                        label: Text(_isRunning ? 'Running...' : 'Run Reconciliation'),
                        style: ElevatedButton.styleFrom(
                          backgroundColor: Colors.black87,
                          foregroundColor: Colors.white,
                          padding: const EdgeInsets.symmetric(vertical: 14),
                          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            ),
            if (_result != null) ...[
              const SizedBox(height: 16),
              _buildResultCard(),
            ],
          ],
        ),
      ),
    );
  }

  Widget _buildResultCard() {
    final isReconciled = _result!['is_reconciled'] as bool? ?? false;
    final maxDiscrepancy = (_result!['max_discrepancy'] as num?)?.toDouble() ?? 0;
    final threshold = (_result!['threshold'] as num?)?.toDouble() ?? 0;
    final timestamp = _result!['timestamp']?.toString() ?? '';
    final totalOrders = _result!['total_orders_count'] as int? ?? 0;
    final totalVolume = (_result!['total_orders_volume'] as num?)?.toDouble() ?? 0;

    final vendorEscrowDisc = (_result!['vendor_escrow_discrepancy'] as num?)?.toDouble() ?? 0;
    final vendorWalletDisc = (_result!['vendor_wallet_discrepancy'] as num?)?.toDouble() ?? 0;
    final adminRevenueDisc = (_result!['admin_revenue_discrepancy'] as num?)?.toDouble() ?? 0;
    final codDebtDisc = (_result!['cod_debt_discrepancy'] as num?)?.toDouble() ?? 0;
    final centralEscrowDisc = (_result!['central_escrow_discrepancy'] as num?)?.toDouble() ?? 0;

    final hasMismatches = vendorEscrowDisc != 0 || vendorWalletDisc != 0 || adminRevenueDisc != 0 || codDebtDisc != 0 || centralEscrowDisc != 0;

    return Card(
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      color: isReconciled ? Colors.green.shade50 : Colors.orange.shade50,
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(
                  isReconciled ? Icons.check_circle : Icons.warning,
                  color: isReconciled ? Colors.green : Colors.orange,
                  size: 24,
                ),
                const SizedBox(width: 8),
                Text(
                  isReconciled ? 'Reconciled' : 'Mismatches Found',
                  style: TextStyle(
                    fontSize: 16,
                    fontWeight: FontWeight.bold,
                    color: isReconciled ? Colors.green : Colors.orange,
                  ),
                ),
              ],
            ),
            const SizedBox(height: 12),
            _detailRow('Status', isReconciled ? 'RECONCILED' : 'DISCREPANCY'),
            _detailRow('Total Orders', '$totalOrders'),
            _detailRow('Total Volume', 'PKR ${totalVolume.toStringAsFixed(2)}'),
            _detailRow('Max Discrepancy', 'PKR ${maxDiscrepancy.toStringAsFixed(2)}'),
            _detailRow('Threshold', 'PKR ${threshold.toStringAsFixed(2)}'),
            if (timestamp.isNotEmpty) _detailRow('Timestamp', timestamp),
            if (hasMismatches) ...[
              const Divider(),
              const Text('Discrepancies:', style: TextStyle(fontWeight: FontWeight.bold)),
              const SizedBox(height: 8),
              if (vendorEscrowDisc != 0) _buildDiscrepancyItem('Vendor Escrow', vendorEscrowDisc),
              if (vendorWalletDisc != 0) _buildDiscrepancyItem('Vendor Wallet', vendorWalletDisc),
              if (adminRevenueDisc != 0) _buildDiscrepancyItem('Admin Revenue', adminRevenueDisc),
              if (codDebtDisc != 0) _buildDiscrepancyItem('COD Debt', codDebtDisc),
              if (centralEscrowDisc != 0) _buildDiscrepancyItem('Central Escrow', centralEscrowDisc),
            ],
          ],
        ),
      ),
    );
  }

  Widget _buildDiscrepancyItem(String account, double difference) {
    return Card(
      margin: const EdgeInsets.only(bottom: 8),
      color: Colors.white,
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Row(
          mainAxisAlignment: MainAxisAlignment.spaceBetween,
          children: [
            Text(account, style: const TextStyle(fontWeight: FontWeight.bold)),
            Text(
              'PKR ${difference.toStringAsFixed(2)}',
              style: TextStyle(
                fontWeight: FontWeight.bold,
                color: difference > 0 ? Colors.green : Colors.red,
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _detailRow(String label, String value) {
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
}
