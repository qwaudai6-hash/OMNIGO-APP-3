import 'package:flutter/material.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';
import '../../../../core/theme/app_theme.dart';

class AdminDisputesScreen extends StatefulWidget {
  const AdminDisputesScreen({super.key});

  @override
  State<AdminDisputesScreen> createState() => _AdminDisputesScreenState();
}

class _AdminDisputesScreenState extends State<AdminDisputesScreen> {
  List<dynamic> _disputes = [];
  bool _isLoading = true;
  String _selectedStatus = '';

  @override
  void initState() {
    super.initState();
    _fetchDisputes();
  }

  Future<void> _fetchDisputes() async {
    setState(() => _isLoading = true);
    try {
      final data = await sl<ApiClient>().get(
        ApiEndpoints.paymentDisputeList(status: _selectedStatus.isEmpty ? null : _selectedStatus),
      );
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _disputes = data['disputes'] as List<dynamic>? ?? [];
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
        title: const Text('Dispute Resolution', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
        actions: [
          Container(
            margin: const EdgeInsets.all(8),
            padding: const EdgeInsets.symmetric(horizontal: 12),
            decoration: BoxDecoration(
              color: Colors.white,
              borderRadius: BorderRadius.circular(12),
            ),
            child: DropdownButton<String>(
              value: _selectedStatus.isEmpty ? null : _selectedStatus,
              hint: const Text('All'),
              underline: const SizedBox(),
              items: const [
                DropdownMenuItem(value: '', child: Text('All')),
                DropdownMenuItem(value: 'open', child: Text('Open')),
                DropdownMenuItem(value: 'under_review', child: Text('Under Review')),
                DropdownMenuItem(value: 'resolved', child: Text('Resolved')),
                DropdownMenuItem(value: 'rejected', child: Text('Rejected')),
              ],
              onChanged: (v) {
                setState(() => _selectedStatus = v ?? '');
                _fetchDisputes();
              },
            ),
          ),
        ],
      ),
      body: _isLoading
          ? const Center(child: CircularProgressIndicator(color: Colors.black87))
          : _disputes.isEmpty
              ? const Center(child: Text('No disputes found', style: TextStyle(color: Colors.grey)))
              : RefreshIndicator(
                  onRefresh: _fetchDisputes,
                  child: ListView.builder(
                    padding: const EdgeInsets.all(12),
                    itemCount: _disputes.length,
                    itemBuilder: (context, index) => _buildDisputeCard(_disputes[index] as Map<String, dynamic>),
                  ),
                ),
    );
  }

  Widget _buildDisputeCard(Map<String, dynamic> dispute) {
    final disputeId = dispute['id']?.toString() ?? dispute['dispute_id']?.toString() ?? '';
    final orderId = dispute['order_tracking_id']?.toString() ?? '';
    final filedBy = dispute['filed_by']?.toString() ?? dispute['customer_name']?.toString() ?? '';
    final reason = dispute['reason']?.toString() ?? dispute['dispute_reason']?.toString() ?? '';
    final status = dispute['status']?.toString() ?? 'open';
    final amount = (dispute['total_amount'] as num?)?.toDouble() ?? 0.0;

    return Card(
      margin: const EdgeInsets.only(bottom: 12),
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
                  Text('Filed by: $filedBy', style: TextStyle(color: Colors.grey.shade600, fontSize: 11)),
                ],
              ),
            ),
            if (amount > 0)
              Text('PKR ${amount.toStringAsFixed(0)}', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 13)),
          ],
        ),
        subtitle: Padding(
          padding: const EdgeInsets.only(top: 4),
          child: Text('Reason: $reason', style: TextStyle(color: Colors.grey.shade700, fontSize: 12)),
        ),
        children: [
          _detailRow('Dispute ID', disputeId),
          _detailRow('Order ID', orderId),
          _detailRow('Filed By', filedBy),
          _detailRow('Amount', 'PKR ${amount.toStringAsFixed(0)}'),
          _detailRow('Reason', reason),
          _detailRow('Status', status),
          const Divider(),
          if (status == 'open' || status == 'under_review') ...[
            const Text('Resolution Actions:', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 13)),
            const SizedBox(height: 8),
            Row(
              children: [
                Expanded(
                  child: ElevatedButton.icon(
                    onPressed: () => _resolveDispute(disputeId, orderId, 'refund_customer'),
                    icon: const Icon(Icons.money_off, size: 16),
                    label: const Text('Refund Customer'),
                    style: ElevatedButton.styleFrom(backgroundColor: Colors.green, foregroundColor: Colors.white),
                  ),
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: ElevatedButton.icon(
                    onPressed: () => _resolveDispute(disputeId, orderId, 'release_vendor'),
                    icon: const Icon(Icons.check_circle, size: 16),
                    label: const Text('Release to Vendor'),
                    style: ElevatedButton.styleFrom(backgroundColor: Colors.blue, foregroundColor: Colors.white),
                  ),
                ),
              ],
            ),
            const SizedBox(height: 8),
            Row(
              children: [
                Expanded(
                  child: ElevatedButton.icon(
                    onPressed: () => _resolveDispute(disputeId, orderId, 'split'),
                    icon: const Icon(Icons.call_split, size: 16),
                    label: const Text('Split'),
                    style: ElevatedButton.styleFrom(backgroundColor: Colors.orange, foregroundColor: Colors.white),
                  ),
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: ElevatedButton.icon(
                    onPressed: () => _holdDispute(disputeId, orderId),
                    icon: const Icon(Icons.pause, size: 16),
                    label: const Text('Hold'),
                    style: ElevatedButton.styleFrom(backgroundColor: Colors.grey.shade700, foregroundColor: Colors.white),
                  ),
                ),
              ],
            ),
          ] else
            Text('Dispute already $status', style: TextStyle(color: Colors.grey.shade500)),
        ],
      ),
    );
  }

  Widget _detailRow(String label, String value) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 3),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceBetween,
        children: [
          Text(label, style: TextStyle(color: Colors.grey.shade600, fontSize: 12)),
          Flexible(child: Text(value, style: const TextStyle(fontSize: 12, fontWeight: FontWeight.w500), textAlign: TextAlign.end)),
        ],
      ),
    );
  }

  Color _statusColor(String status) {
    switch (status) {
      case 'open': return Colors.red;
      case 'under_review': return Colors.orange;
      case 'resolved': return Colors.green;
      case 'rejected': return Colors.grey;
      default: return Colors.grey;
    }
  }

  Future<void> _resolveDispute(String disputeId, String orderId, String resolution) async {
    final confirm = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: Text('Resolve: ${resolution.replaceAll('_', ' ').toUpperCase()}'),
        content: Text('Are you sure you want to resolve dispute for $orderId?'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Cancel')),
          TextButton(
            onPressed: () => Navigator.pop(ctx, true),
            child: const Text('Confirm', style: TextStyle(color: AppTheme.limeAccent)),
          ),
        ],
      ),
    );
    if (confirm != true) return;

    try {
      await sl<ApiClient>().patch(ApiEndpoints.adminDisputeResolve(disputeId), {
        'status': 'resolved',
        'resolution': resolution,
      });
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Dispute resolved: $resolution'), backgroundColor: Colors.green),
        );
        _fetchDisputes();
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed: $e'), backgroundColor: Colors.redAccent),
        );
      }
    }
  }

  Future<void> _holdDispute(String disputeId, String orderId) async {
    try {
      await sl<ApiClient>().patch(ApiEndpoints.adminDisputeResolve(disputeId), {
        'status': 'under_review',
      });
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Dispute put on hold'), backgroundColor: Colors.orange),
        );
        _fetchDisputes();
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed: $e'), backgroundColor: Colors.redAccent),
        );
      }
    }
  }
}
