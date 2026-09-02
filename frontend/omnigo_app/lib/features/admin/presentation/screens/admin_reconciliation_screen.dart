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
    final status = _result!['status']?.toString() ?? 'unknown';
    final mismatches = _result!['mismatches'] as List<dynamic>? ?? [];
    final reconciled = _result!['reconciled_count'] as int? ?? 0;
    final timestamp = _result!['timestamp']?.toString() ?? '';

    final isOk = status == 'ok' || status == 'reconciled';

    return Card(
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      color: isOk ? Colors.green.shade50 : Colors.orange.shade50,
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(
                  isOk ? Icons.check_circle : Icons.warning,
                  color: isOk ? Colors.green : Colors.orange,
                  size: 24,
                ),
                const SizedBox(width: 8),
                Text(
                  isOk ? 'Reconciled' : 'Mismatches Found',
                  style: TextStyle(
                    fontSize: 16,
                    fontWeight: FontWeight.bold,
                    color: isOk ? Colors.green : Colors.orange,
                  ),
                ),
              ],
            ),
            const SizedBox(height: 12),
            _detailRow('Status', status.toUpperCase()),
            _detailRow('Reconciled Entries', '$reconciled'),
            _detailRow('Mismatches', '${mismatches.length}'),
            if (timestamp.isNotEmpty) _detailRow('Timestamp', timestamp),
            if (mismatches.isNotEmpty) ...[
              const Divider(),
              const Text('Mismatches:', style: TextStyle(fontWeight: FontWeight.bold)),
              const SizedBox(height: 8),
              ...mismatches.map((m) => _buildMismatchItem(m as Map<String, dynamic>)),
            ],
          ],
        ),
      ),
    );
  }

  Widget _buildMismatchItem(Map<String, dynamic> mismatch) {
    final account = mismatch['account']?.toString() ?? '';
    final ledgerBalance = (mismatch['ledger_balance'] as num?)?.toDouble() ?? 0;
    final dbBalance = (mismatch['db_balance'] as num?)?.toDouble() ?? 0;
    final difference = ledgerBalance - dbBalance;

    return Card(
      margin: const EdgeInsets.only(bottom: 8),
      color: Colors.white,
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(account, style: const TextStyle(fontWeight: FontWeight.bold)),
            const SizedBox(height: 4),
            _detailRow('Ledger Balance', 'PKR ${ledgerBalance.toStringAsFixed(2)}'),
            _detailRow('DB Balance', 'PKR ${dbBalance.toStringAsFixed(2)}'),
            _detailRow('Difference', 'PKR ${difference.toStringAsFixed(2)}'),
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
