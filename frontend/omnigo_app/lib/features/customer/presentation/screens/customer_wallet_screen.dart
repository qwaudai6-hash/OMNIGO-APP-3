import 'dart:async';

import 'package:flutter/material.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../../../core/network/api_client.dart';
import '../../../../core/network/api_endpoints.dart';

/// CustomerWalletScreen is the customer-side mobile wallet. It lets
/// the customer load funds via PayFast (bank card / JazzCash /
/// EasyPaisa) and use the wallet balance at checkout.
///
/// Backend wiring:
///   - GET  /api/v1/wallet/customer/:tracking_id  → balance + history
///   - POST /api/v1/wallet/customer/load         → start PayFast redirect
///   - POST /api/v1/wallet/customer/load/callback → webhook (backend-only)
class CustomerWalletScreen extends StatefulWidget {
  const CustomerWalletScreen({super.key, required this.trackingId});
  final String trackingId;

  @override
  State<CustomerWalletScreen> createState() => _CustomerWalletScreenState();
}

class _CustomerWalletScreenState extends State<CustomerWalletScreen> {
  Map<String, dynamic>? _wallet;
  bool _isLoading = false;
  String? _error;
  bool _topUpInFlight = false;

  @override
  void initState() {
    super.initState();
    _fetch();
  }

  Future<void> _fetch() async {
    setState(() {
      _isLoading = true;
      _error = null;
    });
    try {
      final api = ApiClient();
      final data = await api.get(ApiEndpoints.customerWallet(widget.trackingId));
      if (!mounted) return;
      setState(() => _wallet = data as Map<String, dynamic>);
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = 'Failed to load wallet: $e');
    } finally {
      if (mounted) setState(() => _isLoading = false);
    }
  }

  Future<void> _topUp({required String gateway}) async {
    final amount = await _promptAmount();
    if (amount == null) return;
    setState(() => _topUpInFlight = true);
    try {
      final api = ApiClient();
      final resp = await api.post(
        ApiEndpoints.customerWalletLoad(),
        {
          'gateway': gateway,
          'amount': amount,
          'currency': 'PKR',
        },
      );
      final redirectUrl = (resp as Map<String, dynamic>)['redirect_url']?.toString();
      if (redirectUrl == null || redirectUrl.isEmpty) {
        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Wallet top-up failed: no redirect URL')),
        );
        return;
      }
      // Launch the gateway in an external browser. The gateway will
      // redirect back to /wallet/customer/load/callback on completion,
      // which the backend handles. After the user closes the browser
      // and returns to the app, we refetch the balance.
      final uri = Uri.parse(redirectUrl);
      if (await canLaunchUrl(uri)) {
        await launchUrl(uri, mode: LaunchMode.externalApplication);
        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
            content: Text('Top-up opened in browser. Tap refresh after payment.'),
            backgroundColor: Colors.blue,
          ),
        );
        await Future<void>.delayed(const Duration(seconds: 2));
        await _fetch();
      }
    } catch (e) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Top-up failed: $e')),
      );
    } finally {
      if (mounted) setState(() => _topUpInFlight = false);
    }
  }

  Future<double?> _promptAmount() async {
    final controller = TextEditingController();
    return showDialog<double>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Top Up Wallet'),
        content: TextField(
          controller: controller,
          keyboardType: const TextInputType.numberWithOptions(decimal: true),
          decoration: const InputDecoration(
            labelText: 'Amount (PKR)',
            hintText: 'e.g. 500',
            prefixText: 'PKR ',
          ),
          autofocus: true,
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(ctx).pop(),
            child: const Text('Cancel'),
          ),
          ElevatedButton(
            onPressed: () {
              final parsed = double.tryParse(controller.text.trim());
              if (parsed == null || parsed < 100) {
                ScaffoldMessenger.of(ctx).showSnackBar(
                  const SnackBar(content: Text('Minimum top-up is PKR 100')),
                );
                return;
              }
              Navigator.of(ctx).pop(parsed);
            },
            child: const Text('Continue'),
          ),
        ],
      ),
    );
  }

  String _fmt(double n) => 'PKR ${n.toStringAsFixed(2)}';

  @override
  Widget build(BuildContext context) {
    final balance = (_wallet?['balance'] as num?)?.toDouble() ?? 0;
    final spent = (_wallet?['lifetime_spent'] as num?)?.toDouble() ?? 0;

    return Scaffold(
      appBar: AppBar(
        title: const Text('My Wallet'),
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh),
            onPressed: _isLoading ? null : _fetch,
          ),
        ],
      ),
      body: _isLoading
          ? const Center(child: CircularProgressIndicator())
          : _error != null
              ? Center(
                  child: Padding(
                    padding: const EdgeInsets.all(16),
                    child: Column(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        Text(_error!, style: const TextStyle(color: Colors.red)),
                        const SizedBox(height: 8),
                        ElevatedButton(
                          onPressed: _fetch,
                          child: const Text('Retry'),
                        ),
                      ],
                    ),
                  ),
                )
              : RefreshIndicator(
                  onRefresh: _fetch,
                  child: ListView(
                    padding: const EdgeInsets.all(16),
                    children: [
                      _buildBalanceCard(balance: balance, spent: spent),
                      const SizedBox(height: 24),
                      const Text(
                        'Top Up Wallet',
                        style: TextStyle(
                          fontSize: 16,
                          fontWeight: FontWeight.bold,
                        ),
                      ),
                      const SizedBox(height: 8),
                      _buildTopUpOption(
                        title: 'PayFast (Cards & Bank)',
                        subtitle: 'Instant via Visa / Mastercard / UnionPay',
                        icon: Icons.credit_card,
                        color: Colors.indigo,
                        onTap: _topUpInFlight ? null : () => _topUp(gateway: 'payfast'),
                      ),
                      const SizedBox(height: 8),
                      _buildTopUpOption(
                        title: 'JazzCash',
                        subtitle: 'Mobile account or voucher',
                        icon: Icons.account_balance_wallet,
                        color: Colors.red,
                        onTap: _topUpInFlight ? null : () => _topUp(gateway: 'jazzcash'),
                      ),
                      const SizedBox(height: 8),
                      _buildTopUpOption(
                        title: 'EasyPaisa',
                        subtitle: 'Mobile account or voucher',
                        icon: Icons.phone_android,
                        color: Colors.green,
                        onTap: _topUpInFlight ? null : () => _topUp(gateway: 'easypaisa'),
                      ),
                      const SizedBox(height: 24),
                      const Text(
                        'Recent Activity',
                        style: TextStyle(
                          fontSize: 16,
                          fontWeight: FontWeight.bold,
                        ),
                      ),
                      const SizedBox(height: 8),
                      ..._buildTransactionList(),
                    ],
                  ),
                ),
    );
  }

  Widget _buildBalanceCard({required double balance, required double spent}) {
    return Container(
      padding: const EdgeInsets.all(20),
      decoration: BoxDecoration(
        gradient: LinearGradient(
          colors: [Colors.indigo.shade700, Colors.indigo.shade900],
          begin: Alignment.topLeft,
          end: Alignment.bottomRight,
        ),
        borderRadius: BorderRadius.circular(20),
        boxShadow: [
          BoxShadow(
            color: Colors.indigo.withOpacity(0.3),
            blurRadius: 12,
            offset: const Offset(0, 6),
          ),
        ],
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const Text(
            'Available Balance',
            style: TextStyle(color: Colors.white70, fontSize: 13),
          ),
          const SizedBox(height: 6),
          Text(
            _fmt(balance),
            style: const TextStyle(
              color: Colors.white,
              fontSize: 32,
              fontWeight: FontWeight.bold,
            ),
          ),
          const SizedBox(height: 16),
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              Text(
                'Lifetime spent: ${_fmt(spent)}',
                style: const TextStyle(color: Colors.white70, fontSize: 12),
              ),
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
                decoration: BoxDecoration(
                  color: Colors.white.withOpacity(0.15),
                  borderRadius: BorderRadius.circular(12),
                ),
                child: const Text(
                  'PKR',
                  style: TextStyle(
                    color: Colors.white,
                    fontWeight: FontWeight.bold,
                    fontSize: 11,
                  ),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }

  Widget _buildTopUpOption({
    required String title,
    required String subtitle,
    required IconData icon,
    required Color color,
    required VoidCallback? onTap,
  }) {
    return Card(
      elevation: 0,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(16),
        side: BorderSide(color: Colors.grey.shade200),
      ),
      child: ListTile(
        leading: CircleAvatar(
          backgroundColor: color.withOpacity(0.1),
          child: Icon(icon, color: color),
        ),
        title: Text(title, style: const TextStyle(fontWeight: FontWeight.bold)),
        subtitle: Text(subtitle, style: const TextStyle(fontSize: 12)),
        trailing: _topUpInFlight
            ? const SizedBox(
                width: 16,
                height: 16,
                child: CircularProgressIndicator(strokeWidth: 2),
              )
            : const Icon(Icons.arrow_forward_ios, size: 14),
        onTap: onTap,
      ),
    );
  }

  List<Widget> _buildTransactionList() {
    final txs = (_wallet?['transactions'] as List<dynamic>?) ?? const [];
    if (txs.isEmpty) {
      return [
        Padding(
          padding: const EdgeInsets.all(16),
          child: Text(
            'No transactions yet',
            style: TextStyle(color: Colors.grey.shade600),
          ),
        ),
      ];
    }
    return txs.take(20).map((tx) {
      final m = tx as Map<String, dynamic>;
      final amount = (m['amount'] as num?)?.toDouble() ?? 0;
      final isCredit = (m['type']?.toString() ?? '') == 'credit';
      return ListTile(
        leading: Icon(
          isCredit ? Icons.arrow_downward : Icons.arrow_upward,
          color: isCredit ? Colors.green : Colors.red,
        ),
        title: Text(m['description']?.toString() ?? m['type']?.toString() ?? 'tx'),
        subtitle: Text(m['created_at']?.toString() ?? ''),
        trailing: Text(
          (isCredit ? '+ ' : '- ') + _fmt(amount),
          style: TextStyle(
            color: isCredit ? Colors.green : Colors.red,
            fontWeight: FontWeight.bold,
          ),
        ),
      );
    }).toList();
  }
}
