import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';
import '../../../../core/network/api_endpoints.dart';
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
  List<dynamic> _payouts = [];
  bool _isLoading = true;
  bool _isWithdrawing = false;

  @override
  void initState() {
    super.initState();
    _fetchAll();
  }

  Future<void> _fetchAll() async {
    setState(() => _isLoading = true);
    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';
      final headers = {'Authorization': 'Bearer $token'};

      // Fetch wallet, escrow holds, and payouts in parallel
      // Payment Orchestrator runs on port 8092
      final paymentBase = ApiEndpoints.paymentBase;
      final results = await Future.wait([
        http.get(
          Uri.parse('$paymentBase/vendor/wallet/${widget.vendorTrackingId}'),
          headers: headers,
        ).timeout(const Duration(seconds: 8)),
        http.get(
          Uri.parse('$paymentBase/escrow/holds/${widget.vendorTrackingId}'),
          headers: headers,
        ).timeout(const Duration(seconds: 8)),
        http.get(
          Uri.parse('$paymentBase/vendor/payouts/${widget.vendorTrackingId}'),
          headers: headers,
        ).timeout(const Duration(seconds: 8)),
      ]);

      if (mounted) {
        if (results[0].statusCode == 200) {
          _wallet = jsonDecode(results[0].body) as Map<String, dynamic>;
        }
        if (results[1].statusCode == 200) {
          final escrowData = jsonDecode(results[1].body) as Map<String, dynamic>;
          _escrowHolds = (escrowData['holds'] as List<dynamic>?) ?? [];
        }
        if (results[2].statusCode == 200) {
          final payoutData = jsonDecode(results[2].body) as Map<String, dynamic>;
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
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      final response = await http.post(
        Uri.parse('${ApiEndpoints.paymentBase}/vendor/withdraw'),
        headers: {
          'Authorization': 'Bearer $token',
          'Content-Type': 'application/json',
        },
        body: jsonEncode({
          'vendor_tracking_id': widget.vendorTrackingId,
          'amount': amount,
          'method': method.toLowerCase(),
        }),
      ).timeout(const Duration(seconds: 8));

      if (mounted) {
        setState(() => _isWithdrawing = false);
        if (response.statusCode == 200) {
          ScaffoldMessenger.of(context).showSnackBar(
            const SnackBar(content: Text('Withdrawal request submitted successfully!')),
          );
          await _fetchAll();
        } else {
          final errorBody = jsonDecode(response.body) as Map<String, dynamic>;
          final errorMsg = errorBody['error'] ?? 'Withdrawal failed';
          ScaffoldMessenger.of(context).showSnackBar(
            SnackBar(content: Text(errorMsg.toString())),
          );
        }
      }
    } catch (e) {
      if (mounted) {
        setState(() => _isWithdrawing = false);
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Network error: $e')),
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
    final double balance = ((_wallet?['balance'] ?? 0) as num).toDouble();
    final double lifetime = ((_wallet?['lifetime_earnings'] ?? 0) as num).toDouble();
    final double totalPayouts = ((_wallet?['total_payouts'] ?? 0) as num).toDouble();

    // Prefer API's central transaction-checked pending escrow balance, fallback to holds sum.
    final double pendingEscrow = ((_wallet?['pending_balance'] ?? _escrowHolds
        .where((h) => h['status'] == 'held')
        .fold<double>(0.0, (sum, h) => sum + ((h['amount'] as num?)?.toDouble() ?? 0.0))) as num).toDouble();

    final double releasableAmount = _escrowHolds
        .where((h) => h['status'] == 'released')
        .fold<double>(0.0, (sum, h) => sum + ((h['amount'] as num?)?.toDouble() ?? 0.0));

    return Scaffold(
      backgroundColor: Colors.grey[100],
      appBar: AppBar(
        title: const Text('Vendor Wallet', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
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
                'Escrow Holds (48h)',
                style: TextStyle(
                  fontSize: 16,
                  fontWeight: FontWeight.bold,
                  color: Colors.orange[800],
                ),
              ),
            ],
          ),
          const SizedBox(height: 16),
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Text('Currently Held', style: TextStyle(color: Colors.grey, fontSize: 12)),
                  Text('PKR ${heldAmount.toStringAsFixed(2)}', style: TextStyle(color: Colors.orange[700], fontWeight: FontWeight.bold)),
                ],
              ),
              Column(
                crossAxisAlignment: CrossAxisAlignment.end,
                children: [
                  const Text('Ready to Release', style: TextStyle(color: Colors.grey, fontSize: 12)),
                  Text('PKR ${releasableAmount.toStringAsFixed(2)}', style: const TextStyle(color: Colors.green, fontWeight: FontWeight.bold)),
                ],
              ),
            ],
          ),
          const SizedBox(height: 12),
          Text(
            'Funds are held for 48 hours after delivery to allow for disputes. After the hold period, funds move to your withdrawable balance.',
            style: TextStyle(color: Colors.grey[600], fontSize: 11),
          ),
        ],
      ),
    );
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
