import 'package:flutter/material.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';
import '../../../../core/theme/app_theme.dart';

class VendorWalletScreen extends StatefulWidget {
  const VendorWalletScreen({super.key, required this.vendorTrackingId});
  final String vendorTrackingId;

  @override
  State<VendorWalletScreen> createState() => _VendorWalletScreenState();
}

class _VendorWalletScreenState extends State<VendorWalletScreen> {
  Map<String, dynamic>? _wallet;
  List<dynamic> _escrowHolds = [];
  Map<String, dynamic> _escrowSummary = {};
  List<dynamic> _payouts = [];
  bool _isLoading = true;
  bool _isWithdrawing = false;
  bool _showAllEscrowHolds = false;

  @override
  void initState() {
    super.initState();
    _fetchAll();
  }

  Future<void> _fetchAll() async {
    setState(() => _isLoading = true);
    try {
      final api = sl<ApiClient>();

      // Fetch wallet, escrow holds (vendor-scoped endpoint), and payouts in parallel.
      // NOTE: We use the vendor-scoped endpoint /payments/vendor/escrow-holds/:vendor_id
      // which is authenticated for the vendor themselves. The previous
      // /payments/escrow/holds/:vendor_id endpoint was admin-only.
      final paymentBase = ApiEndpoints.paymentBase;
      final results = await Future.wait([
        api.get('$paymentBase/payments/vendor/wallet/${widget.vendorTrackingId}'),
        api.get('$paymentBase/payments/vendor/escrow-holds/${widget.vendorTrackingId}'),
        api.get('$paymentBase/payments/vendor/payouts/${widget.vendorTrackingId}'),
      ]);

      if (mounted) {
        if (results[0] is Map<String, dynamic>) {
          _wallet = results[0] as Map<String, dynamic>;
        }
        if (results[1] is Map<String, dynamic>) {
          final escrowData = results[1] as Map<String, dynamic>;
          _escrowHolds = (escrowData['holds'] as List<dynamic>?) ?? [];
          _escrowSummary = (escrowData['summary'] as Map<String, dynamic>?) ?? {};
        }
        if (results[2] is Map<String, dynamic>) {
          final payoutData = results[2] as Map<String, dynamic>;
          _payouts = (payoutData['payouts'] as List<dynamic>?) ?? [];
        }
        setState(() => _isLoading = false);
      }
    } catch (e) {
      if (mounted) {
        setState(() => _isLoading = false);
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Network error: $e')),
        );
      }
    }
  }

  Future<void> _requestWithdraw(double amount, String method) async {
    setState(() => _isWithdrawing = true);
    try {
      final api = sl<ApiClient>();
      await api.post(
        '${ApiEndpoints.paymentBase}/payments/vendor/withdraw',
        {
          'vendor_tracking_id': widget.vendorTrackingId,
          'amount': amount,
          'method': method.toLowerCase(),
        },
      );

      if (mounted) {
        setState(() => _isWithdrawing = false);
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Withdrawal request submitted successfully!')),
        );
        await _fetchAll();
      }
    } catch (e) {
      if (mounted) {
        setState(() => _isWithdrawing = false);
        final errorMsg = e.toString().contains('error')
            ? e.toString().replaceAll('Exception: ', '')
            : 'Network error: $e';
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text(errorMsg)),
        );
      }
    }
  }

  void _showWithdrawDialog(double availableBalance) {
    final amountController = TextEditingController();
    String selectedMethod = 'Bank Transfer';
    final formKey = GlobalKey<FormState>();

    showDialog<void>(
      context: context,
      builder: (context) {
        return StatefulBuilder(
          builder: (context, setModalState) {
            return AlertDialog(
              backgroundColor: Colors.white,
              shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(24)),
              title: const Text(
                'Request Withdrawal',
                style: TextStyle(fontWeight: FontWeight.bold, color: AppTheme.blackAccent),
              ),
              content: Form(
                key: formKey,
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      'Available: PKR ${availableBalance.toStringAsFixed(2)}',
                      style: TextStyle(color: Colors.grey.shade600, fontSize: 13),
                    ),
                    const SizedBox(height: 16),
                    TextFormField(
                      controller: amountController,
                      keyboardType: const TextInputType.numberWithOptions(decimal: true),
                      style: const TextStyle(color: AppTheme.blackAccent),
                      decoration: InputDecoration(
                        labelText: 'Amount (PKR)',
                        labelStyle: const TextStyle(color: Colors.grey),
                        focusedBorder: OutlineInputBorder(
                          borderSide: const BorderSide(color: AppTheme.limeAccent, width: 2),
                          borderRadius: BorderRadius.circular(16),
                        ),
                        enabledBorder: OutlineInputBorder(
                          borderSide: BorderSide(color: Colors.grey.shade300),
                          borderRadius: BorderRadius.circular(16),
                        ),
                        errorBorder: OutlineInputBorder(
                          borderSide: const BorderSide(color: Colors.red),
                          borderRadius: BorderRadius.circular(16),
                        ),
                        focusedErrorBorder: OutlineInputBorder(
                          borderSide: const BorderSide(color: Colors.red, width: 2),
                          borderRadius: BorderRadius.circular(16),
                        ),
                      ),
                      validator: (val) {
                        if (val == null || val.isEmpty) return 'Enter amount';
                        final numVal = double.tryParse(val);
                        if (numVal == null) return 'Invalid number';
                        if (numVal <= 0) return 'Must be greater than 0';
                        if (numVal > availableBalance) return 'Insufficient balance';
                        return null;
                      },
                    ),
                    const SizedBox(height: 16),
                    DropdownButtonFormField<String>(
                      key: ValueKey(selectedMethod),
                      value: selectedMethod,
                      dropdownColor: Colors.white,
                      style: const TextStyle(color: AppTheme.blackAccent),
                      decoration: InputDecoration(
                        labelText: 'Payout Method',
                        labelStyle: const TextStyle(color: Colors.grey),
                        enabledBorder: OutlineInputBorder(
                          borderSide: BorderSide(color: Colors.grey.shade300),
                          borderRadius: BorderRadius.circular(16),
                        ),
                        focusedBorder: OutlineInputBorder(
                          borderSide: const BorderSide(color: AppTheme.limeAccent, width: 2),
                          borderRadius: BorderRadius.circular(16),
                        ),
                      ),
                      items: ['Bank Transfer', 'JazzCash', 'EasyPaisa']
                          .map((m) => DropdownMenuItem(value: m, child: Text(m)))
                          .toList(),
                      onChanged: (val) {
                        if (val != null) {
                          setModalState(() {
                            selectedMethod = val;
                          });
                        }
                      },
                    ),
                  ],
                ),
              ),
              actions: [
                TextButton(
                  onPressed: () => Navigator.pop(context),
                  child: const Text('Cancel', style: TextStyle(color: Colors.grey)),
                ),
                ElevatedButton(
                  onPressed: _isWithdrawing
                      ? null
                      : () {
                          if (formKey.currentState!.validate()) {
                            final amt = double.parse(amountController.text.trim());
                            Navigator.pop(context);
                            _requestWithdraw(amt, selectedMethod);
                          }
                        },
                  style: ElevatedButton.styleFrom(
                    backgroundColor: AppTheme.blackAccent,
                    foregroundColor: Colors.white,
                    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
                  ),
                  child: _isWithdrawing
                      ? const SizedBox(
                          width: 20,
                          height: 20,
                          child: CircularProgressIndicator(color: Colors.white, strokeWidth: 2),
                        )
                      : const Text('Submit'),
                ),
              ],
            );
          },
        );
      },
    );
  }

  @override
  Widget build(BuildContext context) {
    final double balance = (num.tryParse(_wallet?['balance']?.toString() ?? '') ?? 0).toDouble();
    final double lifetime = (num.tryParse(_wallet?['lifetime_earnings']?.toString() ?? '') ?? 0).toDouble();
    final double totalPayouts = (num.tryParse(_wallet?['total_payouts']?.toString() ?? '') ?? 0).toDouble();

    // Prefer API's central transaction-checked pending escrow balance, fallback to holds sum.
    final double pendingEscrow = (num.tryParse(_wallet?['pending_balance']?.toString() ?? '') ??
        _escrowHolds
            .where((h) => h['status'] == 'held')
            .fold<double>(0.0, (sum, h) => sum + ((h['amount'] as num?)?.toDouble() ?? 0.0))).toDouble();

    final double releasableAmount = _escrowHolds
        .where((h) => h['status'] == 'released')
        .fold<double>(0.0, (sum, h) => sum + ((h['amount'] as num?)?.toDouble() ?? 0.0));

    return Scaffold(
      backgroundColor: Colors.grey[100],
      appBar: AppBar(
        title: const Text('Vendor Wallet', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
        leading: const SizedBox.shrink(), // No back button — this is a tab inside VendorDashboardScreen
        iconTheme: const IconThemeData(color: Colors.white),
        actions: [
          IconButton(
            onPressed: _fetchAll,
            icon: const Icon(Icons.refresh_outlined, color: Colors.white),
          ),
        ],
      ),
      body: _isLoading
          ? const Center(child: CircularProgressIndicator(color: Colors.black87))
          : RefreshIndicator(
              onRefresh: _fetchAll,
              color: Colors.black87,
              child: SingleChildScrollView(
                physics: const AlwaysScrollableScrollPhysics(),
                padding: const EdgeInsets.all(16),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    _buildSummaryCard(balance, lifetime, totalPayouts),
                    const SizedBox(height: 24),
                    _buildEscrowSection(pendingEscrow, releasableAmount),
                    const SizedBox(height: 24),
                    const Text('Payout History', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: Colors.black87)),
                    const SizedBox(height: 12),
                    if (_payouts.isEmpty)
                      const Center(
                        child: Padding(
                          padding: EdgeInsets.all(40),
                          child: Text('No payouts yet. Funds are released after 48h hold.', style: TextStyle(color: Colors.grey)),
                        ),
                      )
                    else
                      ..._payouts.map((p) => _buildPayoutCard(p as Map<String, dynamic>)),
                  ],
                ),
              ),
            ),
    );
  }

  Widget _buildSummaryCard(double balance, double lifetime, double totalPayouts) {
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(24),
      decoration: BoxDecoration(
        color: Colors.black87,
        borderRadius: BorderRadius.circular(24),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Text('Withdrawable Balance', style: TextStyle(color: Colors.white70, fontSize: 14)),
                  const SizedBox(height: 8),
                  Text('PKR ${balance.toStringAsFixed(2)}', style: const TextStyle(color: AppTheme.limeAccent, fontSize: 26, fontWeight: FontWeight.bold)),
                ],
              ),
              if (balance > 0)
                ElevatedButton(
                  onPressed: () => _showWithdrawDialog(balance),
                  style: ElevatedButton.styleFrom(
                    backgroundColor: AppTheme.limeAccent,
                    foregroundColor: AppTheme.blackAccent,
                    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                    padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
                  ),
                  child: const Text('Withdraw', style: TextStyle(fontWeight: FontWeight.bold)),
                ),
            ],
          ),
          const SizedBox(height: 20),
          const Divider(color: Colors.white24),
          const SizedBox(height: 12),
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Text('Lifetime Earnings', style: TextStyle(color: Colors.white70, fontSize: 12)),
                  Text('PKR ${lifetime.toStringAsFixed(2)}', style: const TextStyle(color: Colors.white, fontSize: 14, fontWeight: FontWeight.bold)),
                ],
              ),
              Column(
                crossAxisAlignment: CrossAxisAlignment.end,
                children: [
                  const Text('Total Paid Out', style: TextStyle(color: Colors.white70, fontSize: 12)),
                  Text('PKR ${totalPayouts.toStringAsFixed(2)}', style: const TextStyle(color: Colors.green, fontSize: 14, fontWeight: FontWeight.bold)),
                ],
              ),
            ],
          ),
        ],
      ),
    );
  }

  Widget _buildEscrowSection(double heldAmount, double releasableAmount) {
    final heldPaisa = (_escrowSummary['held_paisa'] as num?)?.toInt() ?? 0;
    final readyPaisa = (_escrowSummary['ready_to_release_paisa'] as num?)?.toInt() ?? 0;
    final paidOutPaisa = (_escrowSummary['paid_out_paisa'] as num?)?.toInt() ?? 0;
    final disputedPaisa = (_escrowSummary['disputed_paisa'] as num?)?.toInt() ?? 0;
    final refundedPaisa = (_escrowSummary['refunded_paisa'] as num?)?.toInt() ?? 0;

    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(20),
      decoration: BoxDecoration(
        color: Colors.orange[50],
        borderRadius: BorderRadius.circular(20),
        border: Border.all(color: Colors.orange[200]!),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(Icons.lock_clock, color: Colors.orange[700]),
              const SizedBox(width: 8),
              Text(
                'Escrow Holds',
                style: TextStyle(
                  fontSize: 16,
                  fontWeight: FontWeight.bold,
                  color: Colors.orange[800],
                ),
              ),
              const Spacer(),
              if (_escrowHolds.isNotEmpty)
                TextButton.icon(
                  onPressed: () {
                    setState(() => _showAllEscrowHolds = !_showAllEscrowHolds);
                  },
                  icon: Icon(
                    _showAllEscrowHolds
                        ? Icons.expand_less
                        : Icons.expand_more,
                    color: Colors.orange[800],
                  ),
                  label: Text(
                    _showAllEscrowHolds ? 'Hide' : 'Details',
                    style: TextStyle(color: Colors.orange[800]),
                  ),
                ),
            ],
          ),
          const SizedBox(height: 12),
          // Summary grid: 5 buckets
          _buildSummaryRow('Held (48h)', heldPaisa, Colors.orange[700]!),
          _buildSummaryRow('Ready to Release', readyPaisa, Colors.green),
          if (paidOutPaisa > 0) _buildSummaryRow('Paid Out', paidOutPaisa, Colors.blue),
          if (disputedPaisa > 0)
            _buildSummaryRow('On Dispute', disputedPaisa, Colors.red),
          if (refundedPaisa > 0)
            _buildSummaryRow('Refunded', refundedPaisa, Colors.grey),
          if (_showAllEscrowHolds && _escrowHolds.isNotEmpty) ...[
            const SizedBox(height: 16),
            const Divider(color: Colors.black26),
            const SizedBox(height: 8),
            const Text(
              'Per-Order Breakdown',
              style: TextStyle(
                fontSize: 14,
                fontWeight: FontWeight.bold,
                color: Colors.black87,
              ),
            ),
            const SizedBox(height: 8),
            ..._escrowHolds
                .map((h) => _buildEscrowHoldCard(h as Map<String, dynamic>)),
          ] else if (_escrowHolds.isNotEmpty) ...[
            const SizedBox(height: 8),
            Text(
              'Tap "Details" to see ${_escrowHolds.length} order${_escrowHolds.length == 1 ? '' : 's'}',
              style: TextStyle(color: Colors.grey[600], fontSize: 11, fontStyle: FontStyle.italic),
            ),
          ],
          const SizedBox(height: 8),
          Text(
            'Funds are held for 48 hours after delivery to allow for disputes. After the hold period, funds move to your withdrawable balance.',
            style: TextStyle(color: Colors.grey[600], fontSize: 11),
          ),
        ],
      ),
    );
  }

  Widget _buildSummaryRow(String label, int paisa, Color color) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceBetween,
        children: [
          Text(label, style: TextStyle(color: Colors.grey[700], fontSize: 12)),
          Text(
            'PKR ${(paisa / 100).toStringAsFixed(2)}',
            style: TextStyle(color: color, fontWeight: FontWeight.bold, fontSize: 13),
          ),
        ],
      ),
    );
  }

  Widget _buildEscrowHoldCard(Map<String, dynamic> h) {
    final amountPaisa = (h['amount_paisa'] as num?)?.toInt() ?? 0;
    final status = h['status']?.toString() ?? 'held';
    final orderId = h['order_tracking_id']?.toString() ?? '';
    final gateway = h['payment_gateway']?.toString() ?? '';
    final hint = h['release_hint']?.toString() ?? status;
    final holdUntil = h['hold_until']?.toString() ?? '';
    final releasedAt = h['released_at']?.toString();

    Color statusColor;
    IconData statusIcon;
    String statusLabel;
    switch (status) {
      case 'held':
        statusColor = Colors.orange;
        statusIcon = Icons.lock_clock;
        statusLabel = 'In Escrow';
        break;
      case 'released':
        statusColor = Colors.green;
        statusIcon = Icons.check_circle;
        statusLabel = 'Ready to Release';
        break;
      case 'paid_out':
        statusColor = Colors.blue;
        statusIcon = Icons.account_balance;
        statusLabel = 'Paid Out';
        break;
      case 'disputed':
        statusColor = Colors.red;
        statusIcon = Icons.gavel;
        statusLabel = 'On Dispute';
        break;
      case 'refunded':
        statusColor = Colors.grey;
        statusIcon = Icons.undo;
        statusLabel = 'Refunded';
        break;
      case 'cancelled':
        statusColor = Colors.grey;
        statusIcon = Icons.cancel;
        statusLabel = 'Cancelled';
        break;
      case 'releasing':
        statusColor = Colors.blue;
        statusIcon = Icons.hourglass_bottom;
        statusLabel = 'Releasing';
        break;
      default:
        statusColor = Colors.grey;
        statusIcon = Icons.help;
        statusLabel = status;
    }

    return Container(
      margin: const EdgeInsets.only(bottom: 8),
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(12),
        border: Border.all(color: statusColor.withOpacity(0.3)),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(statusIcon, color: statusColor, size: 20),
              const SizedBox(width: 8),
              Expanded(
                child: Text(
                  'Order #${orderId.length > 12 ? orderId.substring(0, 12) : orderId}…',
                  style: const TextStyle(
                    fontWeight: FontWeight.w600,
                    fontSize: 13,
                  ),
                  overflow: TextOverflow.ellipsis,
                ),
              ),
              Text(
                'PKR ${(amountPaisa / 100).toStringAsFixed(2)}',
                style: TextStyle(
                  color: statusColor,
                  fontWeight: FontWeight.bold,
                  fontSize: 14,
                ),
              ),
            ],
          ),
          const SizedBox(height: 6),
          Row(
            children: [
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
                decoration: BoxDecoration(
                  color: statusColor.withOpacity(0.1),
                  borderRadius: BorderRadius.circular(6),
                ),
                child: Text(
                  statusLabel,
                  style: TextStyle(
                    color: statusColor,
                    fontSize: 10,
                    fontWeight: FontWeight.bold,
                  ),
                ),
              ),
              const SizedBox(width: 6),
              if (gateway.isNotEmpty)
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
                  decoration: BoxDecoration(
                    color: Colors.grey[200],
                    borderRadius: BorderRadius.circular(6),
                  ),
                  child: Text(
                    gateway.toUpperCase(),
                    style: TextStyle(
                      color: Colors.grey[700],
                      fontSize: 10,
                      fontWeight: FontWeight.w600,
                    ),
                  ),
                ),
            ],
          ),
          const SizedBox(height: 6),
          Text(
            _formatHint(hint, holdUntil, releasedAt),
            style: TextStyle(color: Colors.grey[600], fontSize: 11),
          ),
        ],
      ),
    );
  }

  String _formatHint(String hint, String holdUntil, String? releasedAt) {
    if (hint.startsWith('held_remaining_')) {
      final remainder = hint.replaceFirst('held_remaining_', '');
      return 'Releases in $remainder';
    }
    if (hint == 'ready_to_release') {
      return 'Hold expired, awaiting payout worker';
    }
    if (hint == 'released') {
      if (releasedAt != null && releasedAt.length >= 10) {
        return 'Released on ${releasedAt.substring(0, 10)}';
      }
      return 'Released';
    }
    if (hint == 'refunded') {
      return 'Refunded to customer';
    }
    if (hint == 'disputed') {
      return 'Frozen due to open dispute';
    }
    if (hint == 'cancelled') {
      return 'Order cancelled';
    }
    if (hint == 'releasing') {
      return 'Currently being released';
    }
    return hint;
  }

  Widget _buildPayoutCard(Map<String, dynamic> p) {
    final amount = (p['amount'] ?? 0).toDouble();
    final status = p['status']?.toString() ?? 'pending';
    final method = p['method']?.toString() ?? 'N/A';
    final createdAt = p['created_at']?.toString() ?? '';

    Color statusColor;
    IconData statusIcon;
    switch (status) {
      case 'completed':
        statusColor = Colors.green;
        statusIcon = Icons.check_circle;
        break;
      case 'processing':
        statusColor = Colors.orange;
        statusIcon = Icons.hourglass_empty;
        break;
      case 'failed':
        statusColor = Colors.red;
        statusIcon = Icons.error;
        break;
      default:
        statusColor = Colors.grey;
        statusIcon = Icons.pending;
    }

    return Container(
      margin: const EdgeInsets.only(bottom: 12),
      padding: const EdgeInsets.all(16),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(16),
        border: Border.all(color: Colors.black.withOpacity(0.05)),
        boxShadow: [BoxShadow(color: Colors.black.withOpacity(0.03), blurRadius: 10, offset: const Offset(0, 4))],
      ),
      child: Row(
        children: [
          Icon(statusIcon, color: statusColor, size: 32),
          const SizedBox(width: 12),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text('PKR ${amount.toStringAsFixed(2)}', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 16)),
                Text('via $method', style: const TextStyle(color: Colors.grey, fontSize: 12)),
                if (createdAt.isNotEmpty)
                  Text(createdAt.substring(0, 10), style: const TextStyle(color: Colors.grey, fontSize: 11)),
              ],
            ),
          ),
          Container(
            padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
            decoration: BoxDecoration(
              color: statusColor.withOpacity(0.1),
              borderRadius: BorderRadius.circular(8),
            ),
            child: Text(status, style: TextStyle(color: statusColor, fontSize: 11, fontWeight: FontWeight.bold)),
          ),
        ],
      ),
    );
  }
}
