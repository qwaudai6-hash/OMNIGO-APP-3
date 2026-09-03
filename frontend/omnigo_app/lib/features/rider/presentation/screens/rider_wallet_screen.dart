import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';
import 'package:url_launcher/url_launcher.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/theme/app_theme.dart';

class RiderWalletScreen extends StatefulWidget {
  const RiderWalletScreen({super.key, required this.trackingId});
  final String trackingId;

  @override
  State<RiderWalletScreen> createState() => _RiderWalletScreenState();
}

class _RiderWalletScreenState extends State<RiderWalletScreen> {
  Map<String, dynamic>? _wallet;
  List<dynamic> _codDebts = [];
  bool _isLoading = true;
  String? _errorMessage;

  @override
  void initState() {
    super.initState();
    _fetchWallet();
  }

  Future<void> _fetchWallet() async {
    setState(() { _isLoading = true; _errorMessage = null; });
    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      // Fetch wallet and COD debts in parallel with retry
      final walletFuture = _fetchWithRetry(ApiEndpoints.riderWallet(widget.trackingId), token);
      final codDebtsFuture = _fetchWithRetry(ApiEndpoints.codDebts(widget.trackingId), token);

      final responses = await Future.wait([walletFuture, codDebtsFuture]);

      if (mounted) {
        if (responses[0].statusCode == 200) {
          final decoded = jsonDecode(responses[0].body);
          if (decoded is Map<String, dynamic>) _wallet = decoded;
        }
        if (responses[1].statusCode == 200) {
          final decoded = jsonDecode(responses[1].body);
          if (decoded is Map<String, dynamic>) {
            _codDebts = (decoded['debts'] as List<dynamic>?) ?? <dynamic>[];
          }
        }
        setState(() => _isLoading = false);
      }
    } catch (e) {
      if (mounted) {
        setState(() {
          _isLoading = false;
          _errorMessage = 'Failed to load wallet data.';
        });
      }
    }
  }

  Future<http.Response> _fetchWithRetry(String url, String token, {int maxRetries = 2}) async {
    for (int attempt = 0; attempt <= maxRetries; attempt++) {
      try {
        final res = await http.get(
          Uri.parse(url),
          headers: {'Authorization': 'Bearer $token'},
        ).timeout(const Duration(seconds: 8));
        if (res.statusCode < 500 || attempt == maxRetries) return res;
      } catch (e) {
        if (attempt == maxRetries) rethrow;
        await Future<void>.delayed(Duration(seconds: attempt + 1));
      }
    }
    throw Exception('unreachable');
  }

  Future<void> _payNow(String codDebtId, String gateway) async {
    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      final response = await http.post(
        Uri.parse(ApiEndpoints.codPayNow()),
        headers: {
          'Authorization': 'Bearer $token',
          'Content-Type': 'application/json',
        },
        body: jsonEncode({
          'cod_debt_id': codDebtId,
          'gateway': gateway,
        }),
      ).timeout(const Duration(seconds: 8));

      if (response.statusCode == 200 && mounted) {
        final data = jsonDecode(response.body) as Map<String, dynamic>;
        final deepLink = (data['deep_link'] as String?) ?? '';

        if (deepLink.isNotEmpty) {
          // Auto-launch the gateway via the system handler. Fall back to a
          // snackbar with a manual Open action if the device cannot resolve
          // the URL (no browser, no app, no handler).
          final uri = Uri.parse(deepLink);
          bool launched = false;
          try {
            launched = await launchUrl(uri, mode: LaunchMode.externalApplication);
          } catch (e) {
            debugPrint('launchUrl threw: $e');
            launched = false;
          }
          if (!launched && mounted) {
            ScaffoldMessenger.of(context).showSnackBar(
              SnackBar(
                content: Text('Could not open $gateway automatically. Tap to retry.'),
                action: SnackBarAction(
                  label: 'Open',
                  onPressed: () {
                    launchUrl(uri, mode: LaunchMode.externalApplication);
                  },
                ),
                duration: const Duration(seconds: 6),
              ),
            );
          }
        } else {
          // Backend returned 200 but no deep link (e.g. JazzCash/EasyPaisa
          // failure simulated in dev) — refresh wallet and tell the user.
          // Capture the messenger BEFORE the await so the analyzer is
          // satisfied we aren't using a stale context after the gap.
          if (mounted) {
            final messenger = ScaffoldMessenger.of(context);
            await _fetchWallet();
            if (!mounted) return;
            messenger.showSnackBar(
              SnackBar(content: Text('$gateway session created. Complete payment in the gateway app or contact support if amount is not reflected.')),
            );
          }
        }
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Payment failed: $e')),
        );
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final balance = ((_wallet?['balance'] as num?) ?? 0).toDouble();
    final lifetime = ((_wallet?['lifetime_earnings'] as num?) ?? 0).toDouble();
    // UX FIX: surface the COD cash-holding counter. Backend blocks new COD
    // gigs at >= PKR 5,000 — riders could never see WHY they were blocked.
    final cashInHand = ((_wallet?['cash_in_hand'] as num?) ?? 0).toDouble();
    const codCashLimit = 5000.0;
    final codBlocked = cashInHand >= codCashLimit;
    final credits = (_wallet?['recent_credits'] ?? <dynamic>[]) as List<dynamic>;

    return Scaffold(
      backgroundColor: Colors.grey[100],
      appBar: AppBar(
        title: const Text('Earnings Wallet', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
        iconTheme: const IconThemeData(color: Colors.white),
        actions: [
          IconButton(
            onPressed: _fetchWallet,
            icon: const Icon(Icons.refresh_outlined, color: Colors.white),
          ),
        ],
      ),
      body: _isLoading
          ? const Center(child: CircularProgressIndicator(color: Colors.black87))
          : _errorMessage != null
              ? Center(
                  child: Padding(
                    padding: const EdgeInsets.all(32),
                    child: Column(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        const Icon(Icons.error_outline, size: 64, color: Colors.redAccent),
                        const SizedBox(height: 16),
                        Text(_errorMessage!, textAlign: TextAlign.center, style: const TextStyle(fontSize: 16, color: Colors.grey)),
                        const SizedBox(height: 24),
                        ElevatedButton.icon(
                          onPressed: _fetchWallet,
                          icon: const Icon(Icons.refresh, color: Colors.white),
                          label: const Text('Retry', style: TextStyle(color: Colors.white)),
                          style: ElevatedButton.styleFrom(backgroundColor: Colors.black87),
                        ),
                      ],
                    ),
                  ),
                )
              : RefreshIndicator(
              onRefresh: _fetchWallet,
              color: Colors.black87,
              child: SingleChildScrollView(
                physics: const AlwaysScrollableScrollPhysics(),
                padding: const EdgeInsets.all(16),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    _buildSummaryCard(balance, lifetime),
                    const SizedBox(height: 16),
                    _buildCashInHandCard(cashInHand, codCashLimit, codBlocked),
                    if (_codDebts.isNotEmpty) ...[
                      const SizedBox(height: 24),
                      _buildCODDebtsSection(),
                    ],
                    const SizedBox(height: 24),
                    const Text('Recent Credits', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: Colors.black87)),
                    const SizedBox(height: 12),
                    if (credits.isEmpty)
                      const Center(
                        child: Padding(
                          padding: EdgeInsets.all(40),
                          child: Text('No completed deliveries yet. Complete a gig to see earnings.', style: TextStyle(color: Colors.grey)),
                        ),
                      )
                    else
                      ...credits.map((c) => _buildCreditCard(c as Map<String, dynamic>)),
                  ],
                ),
              ),
            ),
    );
  }

  Widget _buildSummaryCard(double balance, double lifetime) {
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
          const Text('Available Balance', style: TextStyle(color: Colors.white70, fontSize: 14)),
          const SizedBox(height: 8),
          Text('PKR ${balance.toStringAsFixed(2)}', style: const TextStyle(color: AppTheme.limeAccent, fontSize: 32, fontWeight: FontWeight.bold)),
          const SizedBox(height: 20),
          const Divider(color: Colors.white24),
          const SizedBox(height: 12),
          Row(
            children: [
              const Icon(Icons.trending_up, color: Colors.white70),
              const SizedBox(width: 8),
              Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const Text('Lifetime Earnings', style: TextStyle(color: Colors.white70, fontSize: 12)),
                  Text('PKR ${lifetime.toStringAsFixed(2)}', style: const TextStyle(color: Colors.white, fontSize: 16, fontWeight: FontWeight.bold)),
                ],
              ),
            ],
          ),
        ],
      ),
    );
  }

  Widget _buildCODDebtsSection() {
    final totalOwed = _codDebts.fold<double>(0, (sum, d) => sum + ((d['amount_owed'] as num?) ?? 0).toDouble());

    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(20),
      decoration: BoxDecoration(
        color: Colors.red[50],
        borderRadius: BorderRadius.circular(20),
        border: Border.all(color: Colors.red[200]!),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(Icons.warning_amber_rounded, color: Colors.red[700]),
              const SizedBox(width: 8),
              Text(
                'COD Debt — You owe the platform',
                style: TextStyle(
                  fontSize: 16,
                  fontWeight: FontWeight.bold,
                  color: Colors.red[800],
                ),
              ),
            ],
          ),
          const SizedBox(height: 12),
          Text(
            'PKR ${totalOwed.toStringAsFixed(2)}',
            style: TextStyle(
              fontSize: 28,
              fontWeight: FontWeight.bold,
              color: Colors.red[700],
            ),
          ),
          const SizedBox(height: 16),
          ...(_codDebts.where((d) => d['status'] == 'pending').map((debt) {
            final debtId = debt['id']?.toString() ?? '';
            final amount = ((debt['amount_owed'] as num?) ?? 0).toDouble();
            final orderId = debt['order_tracking_id']?.toString() ?? '';

            return Container(
              margin: const EdgeInsets.only(bottom: 12),
              padding: const EdgeInsets.all(12),
              decoration: BoxDecoration(
                color: Colors.white,
                borderRadius: BorderRadius.circular(12),
              ),
              child: Row(
                children: [
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text('Order: $orderId', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 12)),
                        Text('PKR ${amount.toStringAsFixed(2)}', style: const TextStyle(color: Colors.red, fontWeight: FontWeight.bold)),
                      ],
                    ),
                  ),
                  ElevatedButton.icon(
                    onPressed: () => _showPaymentDialog(debtId, amount),
                    icon: const Icon(Icons.payment, size: 16),
                    label: const Text('Pay Now', style: TextStyle(fontSize: 12)),
                    style: ElevatedButton.styleFrom(
                      backgroundColor: Colors.red[700],
                      foregroundColor: Colors.white,
                      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
                    ),
                  ),
                ],
              ),
            );
          })),
        ],
      ),
    );
  }

  void _showPaymentDialog(String codDebtId, double amount) {
    showDialog<void>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Pay COD Debt'),
        content: Text('Pay PKR ${amount.toStringAsFixed(2)} via:'),
        actions: [
          TextButton(
            onPressed: () {
              Navigator.pop(ctx);
              _payNow(codDebtId, 'jazzcash');
            },
            child: const Text('JazzCash'),
          ),
          TextButton(
            onPressed: () {
              Navigator.pop(ctx);
              _payNow(codDebtId, 'easypaisa');
            },
            child: const Text('EasyPaisa'),
          ),
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Cancel'),
          ),
        ],
      ),
    );
  }

  Widget _buildCreditCard(Map<String, dynamic> c) {
    final orderId = c['order_id']?.toString() ?? 'N/A';
    final net = num.tryParse(c['net_credit']?.toString() ?? '0')?.toDouble() ?? 0.0;
    final fee = num.tryParse(c['delivery_fee']?.toString() ?? '0')?.toDouble() ?? 0.0;
    final commission = num.tryParse(c['admin_commission']?.toString() ?? '0')?.toDouble() ?? 0.0;

    return Container(
      margin: const EdgeInsets.only(bottom: 12),
      padding: const EdgeInsets.all(16),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(16),
        border: Border.all(color: Colors.black.withOpacity(0.05)),
        boxShadow: [BoxShadow(color: Colors.black.withOpacity(0.03), blurRadius: 10, offset: const Offset(0, 4))],
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              Text('Order: $orderId', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 13)),
              Text('+ PKR ${net.toStringAsFixed(2)}', style: const TextStyle(color: Colors.green, fontWeight: FontWeight.bold, fontSize: 13)),
            ],
          ),
          const SizedBox(height: 8),
          Text('Delivery fee: PKR ${fee.toStringAsFixed(2)}', style: const TextStyle(color: Colors.grey, fontSize: 12)),
          Text('Admin commission: PKR ${commission.toStringAsFixed(2)}', style: const TextStyle(color: Colors.grey, fontSize: 12)),
        ],
      ),
    );
  }

  /// UX FIX: COD cash-holding meter. Shows how close the rider is to the
  /// PKR 5,000 deposit threshold and warns when new COD gigs are blocked.
  Widget _buildCashInHandCard(double cashInHand, double limit, bool blocked) {
    final progress = (cashInHand / limit).clamp(0.0, 1.0);
    return Container(
      padding: const EdgeInsets.all(16),
      decoration: BoxDecoration(
        color: blocked ? const Color(0xFFFFF3F0) : Colors.white,
        borderRadius: BorderRadius.circular(16),
        border: Border.all(color: blocked ? Colors.redAccent : Colors.grey.shade300),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(
                blocked ? Icons.block_rounded : Icons.account_balance_wallet_outlined,
                color: blocked ? Colors.redAccent : Colors.black87,
                size: 20,
              ),
              const SizedBox(width: 8),
              Text(
                'COD Cash in Hand',
                style: TextStyle(
                  fontSize: 13,
                  fontWeight: FontWeight.w700,
                  color: blocked ? Colors.redAccent : Colors.black87,
                ),
              ),
              const Spacer(),
              Text(
                'PKR ${cashInHand.toStringAsFixed(0)} / ${limit.toStringAsFixed(0)}',
                style: TextStyle(
                  fontSize: 12,
                  fontWeight: FontWeight.w800,
                  color: blocked ? Colors.redAccent : Colors.black54,
                ),
              ),
            ],
          ),
          const SizedBox(height: 10),
          ClipRRect(
            borderRadius: BorderRadius.circular(6),
            child: LinearProgressIndicator(
              value: progress,
              minHeight: 8,
              backgroundColor: Colors.grey.shade200,
              valueColor: AlwaysStoppedAnimation<Color>(
                blocked ? Colors.redAccent : (progress > 0.75 ? Colors.orange : Colors.green),
              ),
            ),
          ),
          const SizedBox(height: 8),
          Text(
            blocked
                ? 'Limit reached \u2014 new COD gigs are blocked. Deposit your collected cash to unlock them.'
                : 'Deposit collected cash before this reaches PKR ${limit.toStringAsFixed(0)} to keep accepting COD orders.',
            style: TextStyle(fontSize: 11.5, color: blocked ? Colors.redAccent : Colors.black54),
          ),
        ],
      ),
    );
  }
}
