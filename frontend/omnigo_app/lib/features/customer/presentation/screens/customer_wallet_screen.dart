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
      }
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(
          content: Text('Top-up opened in browser. Tap refresh after payment.'),
          backgroundColor: Colors.blue,
        ),
      );
      await Future.delayed(const Duration(seconds: 2));
      await _fetch();
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
        title: const Text('Top-Up Amount'),
        content: TextField(
          controller: controller,
          keyboardType: const TextInputType.numberWithOptions(decimal: true),
          decoration: const InputDecoration(
            labelText: 'PKR',
            border: OutlineInputBorder(),
          ),
          autofocus: true,
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () {
              final v = double.tryParse(controller.text.trim());
              if (v == null || v < 100) {
                ScaffoldMessenger.of(ctx).showSnackBar(
                  const SnackBar(content: Text('Min top-up is PKR 100')),
                );
                return;
              }
              Navigator.pop(ctx, v);
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
    final balance = (_wallet?['balance'] as num?)?.toDouble() ?? 0.0;
    return Scaffold(
      backgroundColor: Colors.white,
      appBar: AppBar(
        backgroundColor: Colors.white,
        elevation: 0,
        foregroundColor: Colors.black,
        title: const Text('My Wallet'),
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh),
            onPressed: _isLoading ? null : _fetch,
          ),
        ],
      ),
      body: RefreshIndicator(
        onRefresh: _fetch,
        child: _isLoading && _wallet == null
            ? const Center(child: CircularProgressIndicator())
            : ListView(
                padding: const EdgeInsets.all(16),
                children: [
                  if (_error != null)
                    Container(
                      padding: const EdgeInsets.all(12),
                      margin: const EdgeInsets.only(bottom: 16),
                      decoration: BoxDecoration(
                        color: Colors.red.shade50,
                        borderRadius: BorderRadius.circular(8),
                      ),
                      child: Text(_error!, style: const TextStyle(color: Colors.red)),
                    ),

                  // Balance card
                  Container(
                    padding: const EdgeInsets.all(24),
                    decoration: BoxDecoration(
                      gradient: const LinearGradient(
                        colors: [Color(0xFF0a0a0a), Color(0xFF2a2a2a)],
                        begin: Alignment.topLeft,
                        end: Alignment.bottomRight,
                      ),
                      borderRadius: BorderRadius.circular(20),
                    ),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        const Text(
                          'Available Balance',
                          style: TextStyle(color: Colors.white70, fontSize: 14),
                        ),
                        const SizedBox(height: 8),
                        Text(
                          _fmt(balance),
                          style: const TextStyle(
                            color: Color(0xFFCAFF33),
                            fontSize: 36,
                            fontWeight: FontWeight.bold,
                          ),
                        ),
                        const SizedBox(height: 16),
                        const Text(
                          'Use this balance at checkout for instant payments.',
                          style: TextStyle(color: Colors.white60, fontSize: 12),
                        ),
                      ],
                    ),
                  ),
                  const SizedBox(height: 24),

                  // Top-up options
                  const Text(
                    'Top up your wallet',
                    style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold),
                  ),
                  const SizedBox(height: 12),
                  _buildTopUpTile(
                    title: 'PayFast (Card / Bank)',
                    subtitle: 'Debit/credit card, JazzCash, EasyPaisa via PayFast',
                    icon: Icons.account_balance,
                    color: Colors.deepOrange,
                    onTap: _topUpInFlight ? null : () => _topUp(gateway: 'payfast'),
                  ),
                  _buildTopUpTile(
                    title: 'JazzCash',
                    subtitle: 'Pakistan mobile wallet',
                    icon: Icons.phone_android,
                    color: Colors.red,
                    onTap: _topUpInFlight ? null : () => _topUp(gateway: 'jazzcash'),
                  ),
                  _buildTopUpTile(
                    title: 'EasyPaisa',
                    subtitle: 'Pakistan mobile wallet',
                    icon: Icons.account_balance_wallet,
                    color: Colors.green,
                    onTap: _topUpInFlight ? null : () => _topUp(gateway: 'easypaisa'),
                  ),

                  const SizedBox(height: 24),
                  // Transaction history
                  const Text(
                    'Recent transactions',
                    style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold),
                  ),
                  const SizedBox(height: 8),
                  ..._buildTransactionList(),
                ],
              ),
      ),
    );
  }

  Widget _buildTopUpTile({
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
