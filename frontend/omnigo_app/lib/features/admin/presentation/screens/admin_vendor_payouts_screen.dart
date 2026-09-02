import 'package:flutter/material.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';

class AdminVendorPayoutsScreen extends StatefulWidget {
  const AdminVendorPayoutsScreen({super.key});

  @override
  State<AdminVendorPayoutsScreen> createState() => _AdminVendorPayoutsScreenState();
}

class _AdminVendorPayoutsScreenState extends State<AdminVendorPayoutsScreen> {
  List<dynamic> _payouts = [];
  bool _isLoading = true;
  String _selectedStatus = 'all';

  @override
  void initState() {
    super.initState();
    _fetchPayouts();
  }

  Future<void> _fetchPayouts() async {
    setState(() => _isLoading = true);
    try {
      final data = await sl<ApiClient>().get(
        ApiEndpoints.adminVendorPayouts(status: _selectedStatus),
      );
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _payouts = data['payouts'] as List<dynamic>? ?? [];
          _isLoading = false;
        });
      }
    } catch (e) {
      if (mounted) setState(() => _isLoading = false);
    }
  }

  Future<void> _approvePayout(String id) async {
    try {
      await sl<ApiClient>().post(ApiEndpoints.adminVendorPayoutApprove(id), {});
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Payout approved'), backgroundColor: Colors.green),
        );
        _fetchPayouts();
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed: $e'), backgroundColor: Colors.red),
        );
      }
    }
  }

  Future<void> _rejectPayout(String id) async {
    try {
      await sl<ApiClient>().post(ApiEndpoints.adminVendorPayoutReject(id), {});
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Payout rejected'), backgroundColor: Colors.orange),
        );
        _fetchPayouts();
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed: $e'), backgroundColor: Colors.red),
        );
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.grey[100],
      appBar: AppBar(
        title: const Text('Vendor Payouts', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
        bottom: PreferredSize(
          preferredSize: const Size.fromHeight(50),
          child: Padding(
            padding: const EdgeInsets.all(8),
            child: Row(
              children: [
                _filterChip('All', ''),
                const SizedBox(width: 8),
                _filterChip('Pending', 'pending'),
                const SizedBox(width: 8),
                _filterChip('Approved', 'approved'),
                const SizedBox(width: 8),
                _filterChip('Rejected', 'rejected'),
                const SizedBox(width: 8),
                _filterChip('Paid', 'paid'),
              ],
            ),
          ),
        ),
      ),
      body: _isLoading
          ? const Center(child: CircularProgressIndicator(color: Colors.black87))
          : _payouts.isEmpty
              ? const Center(child: Text('No payout requests'))
              : RefreshIndicator(
                  onRefresh: _fetchPayouts,
                  child: ListView.builder(
                    padding: const EdgeInsets.all(12),
                    itemCount: _payouts.length,
                    itemBuilder: (context, index) => _buildPayoutCard(_payouts[index] as Map<String, dynamic>),
                  ),
                ),
    );
  }

  Widget _filterChip(String label, String value) {
    final isSelected = _selectedStatus == value;
    return FilterChip(
      label: Text(label, style: TextStyle(fontSize: 12, color: isSelected ? Colors.white : Colors.grey.shade700)),
      selected: isSelected,
      selectedColor: Colors.black87,
      onSelected: (_) {
        setState(() => _selectedStatus = value);
        _fetchPayouts();
      },
    );
  }

  Widget _buildPayoutCard(Map<String, dynamic> payout) {
    final id = payout['id']?.toString() ?? '';
    final vendorId = payout['vendor_tracking_id']?.toString() ?? '';
    final amount = (payout['amount'] as num?)?.toDouble() ?? 0;
    final method = payout['method']?.toString() ?? '';
    final status = payout['status']?.toString() ?? '';
    final createdAt = payout['created_at']?.toString() ?? '';

    final statusColor = status == 'pending' ? Colors.orange
        : status == 'approved' ? Colors.blue
        : status == 'paid' ? Colors.green
        : Colors.red;

    return Card(
      margin: const EdgeInsets.only(bottom: 8),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              mainAxisAlignment: MainAxisAlignment.spaceBetween,
              children: [
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(vendorId, style: const TextStyle(fontWeight: FontWeight.bold)),
                      const SizedBox(height: 2),
                      Text('ID: $id', style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
                    ],
                  ),
                ),
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
                  decoration: BoxDecoration(
                    color: statusColor.withOpacity(0.1),
                    borderRadius: BorderRadius.circular(8),
                  ),
                  child: Text(status.toUpperCase(), style: TextStyle(color: statusColor, fontSize: 11, fontWeight: FontWeight.bold)),
                ),
              ],
            ),
            const Divider(),
            Row(
              mainAxisAlignment: MainAxisAlignment.spaceBetween,
              children: [
                Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text('Amount', style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
                    Text('PKR ${amount.toStringAsFixed(0)}', style: const TextStyle(fontSize: 16, fontWeight: FontWeight.bold, color: Colors.green)),
                  ],
                ),
                Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text('Method', style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
                    Text(method.toUpperCase(), style: const TextStyle(fontSize: 14, fontWeight: FontWeight.bold)),
                  ],
                ),
                Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text('Created', style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
                    Text(createdAt.split('T').first, style: TextStyle(fontSize: 12, color: Colors.grey.shade700)),
                  ],
                ),
              ],
            ),
            if (status == 'pending') ...[
              const SizedBox(height: 12),
              Row(
                children: [
                  Expanded(
                    child: OutlinedButton.icon(
                      onPressed: () => _approvePayout(id),
                      icon: const Icon(Icons.check, size: 16),
                      label: const Text('Approve'),
                      style: OutlinedButton.styleFrom(foregroundColor: Colors.green),
                    ),
                  ),
                  const SizedBox(width: 8),
                  Expanded(
                    child: OutlinedButton.icon(
                      onPressed: () => _rejectPayout(id),
                      icon: const Icon(Icons.close, size: 16),
                      label: const Text('Reject'),
                      style: OutlinedButton.styleFrom(foregroundColor: Colors.red),
                    ),
                  ),
                ],
              ),
            ],
          ],
        ),
      ),
    );
  }
}
