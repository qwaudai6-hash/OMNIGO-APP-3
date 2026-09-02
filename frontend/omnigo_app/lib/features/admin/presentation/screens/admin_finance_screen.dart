import 'package:flutter/material.dart';
import 'package:fl_chart/fl_chart.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';

class AdminFinanceScreen extends StatefulWidget {
  const AdminFinanceScreen({super.key});

  @override
  State<AdminFinanceScreen> createState() => _AdminFinanceScreenState();
}

class _AdminFinanceScreenState extends State<AdminFinanceScreen> {
  Map<String, dynamic>? _kpis;
  List<dynamic> _payments = [];
  List<dynamic> _dailyRevenue = [];
  Map<String, dynamic>? _payfastSummary;
  List<dynamic> _payfastTransactions = [];
  String _payfastFilter = 'all';
  int _selectedFinanceTab = 0; // 0: TigerBeetle Ledger & GMV, 1: PayFast Gateway Transactions
  bool _isLoading = true;

  int _daysFilter = 7;
  String _paymentFilter = 'all';

  static String get _adminBase => ApiEndpoints.adminBase;

  @override
  void initState() {
    super.initState();
    _fetchFinanceData();
  }

  Future<void> _fetchFinanceData() async {
    setState(() => _isLoading = true);
    
    try {
      final api = sl<ApiClient>();
      
      // #15-16: Wrap each call in individual try/catch with mounted checks
      // Fetch KPIs
      try {
        final kpiRes = await api.get('$_adminBase/admin/finance/ledger-kpis');
        if (kpiRes is Map<String, dynamic>) {
          _kpis = kpiRes;
        }
      } catch (e) {
        debugPrint('Failed to fetch KPIs: $e');
      }

      // Fetch Daily Revenue for Chart
      try {
        final revenueRes = await api.get('$_adminBase/admin/finance/daily-revenue?days=$_daysFilter&payment_method=$_paymentFilter');
        if (revenueRes is Map<String, dynamic>) {
          _dailyRevenue = (revenueRes['daily_revenue'] as List<dynamic>?) ?? [];
        }
      } catch (e) {
        debugPrint('Failed to fetch daily revenue: $e');
      }

      // Fetch Payments
      try {
        final payRes = await api.get('$_adminBase/admin/finance/payments?limit=50');
        if (payRes is Map<String, dynamic>) {
          _payments = (payRes['payments'] as List<dynamic>?) ?? [];
        }
      } catch (e) {
        debugPrint('Failed to fetch payments: $e');
      }

      // Fetch PayFast Summary
      try {
        final pfSummaryRes = await api.get(ApiEndpoints.adminPayFastSummary());
        if (pfSummaryRes is Map<String, dynamic>) {
          _payfastSummary = pfSummaryRes;
        }
      } catch (e) {
        debugPrint('Failed to fetch PayFast summary: $e');
      }

      // Fetch PayFast Transactions
      try {
        final pfTxnRes = await api.get(ApiEndpoints.adminPayFastTransactions(status: _payfastFilter, limit: 50));
        if (pfTxnRes is Map<String, dynamic>) {
          _payfastTransactions = (pfTxnRes['transactions'] as List<dynamic>?) ?? [];
        }
      } catch (e) {
        debugPrint('Failed to fetch PayFast transactions: $e');
      }
      
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Network error fetching finance data: $e'), backgroundColor: Colors.redAccent),
        );
      }
    } finally {
      if (mounted) {
        setState(() => _isLoading = false);
      }
    }
  }

  void _showApiKeysPanel() {
    showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      shape: const RoundedRectangleBorder(borderRadius: BorderRadius.vertical(top: Radius.circular(20))),
      builder: (ctx) {
        return _ApiKeysPanel(
          onSaved: () {
            // After a successful save, refetch the screen so KPIs that
            // depend on gateway config (e.g. live Stripe health) stay fresh.
            _fetchFinanceData();
          },
        );
      },
    );
  }

  Widget _buildKPICard(String title, double amount, Color color, IconData icon, {String? subtitle}) {
    return Expanded(
      child: Container(
        padding: const EdgeInsets.all(16),
        decoration: BoxDecoration(
          color: Colors.white,
          borderRadius: BorderRadius.circular(16),
          border: Border.all(color: color.withOpacity(0.3)),
          boxShadow: [BoxShadow(color: color.withOpacity(0.08), blurRadius: 8, offset: const Offset(0, 3))],
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(icon, color: color, size: 20),
                const SizedBox(width: 8),
                Expanded(child: Text(title, style: TextStyle(color: Colors.grey.shade700, fontWeight: FontWeight.bold, fontSize: 12), overflow: TextOverflow.ellipsis)),
              ],
            ),
            const SizedBox(height: 10),
            Text('PKR ${amount.toStringAsFixed(0)}', style: TextStyle(color: color, fontWeight: FontWeight.w900, fontSize: 18)),
            if (subtitle != null) ...[
              const SizedBox(height: 4),
              Text(subtitle, style: TextStyle(color: Colors.grey.shade500, fontSize: 11, fontWeight: FontWeight.w600)),
            ],
          ],
        ),
      ),
    );
  }

  Widget _buildRevenueChart() {
    if (_dailyRevenue.isEmpty) {
      return Container(
        height: 250,
        decoration: BoxDecoration(color: Colors.white, borderRadius: BorderRadius.circular(16)),
        child: const Center(child: Text('No Data Available for Selected Filters', style: TextStyle(fontWeight: FontWeight.bold))),
      );
    }

    final List<FlSpot> spots = [];
    double maxY = 0;
    for (int i = 0; i < _dailyRevenue.length; i++) {
      final val = (_dailyRevenue[i]['gross_volume'] as num?)?.toDouble() ?? 0.0;
      spots.add(FlSpot(i.toDouble(), val));
      if (val > maxY) maxY = val;
    }

    return Container(
      height: 250,
      padding: const EdgeInsets.only(right: 24, left: 8, top: 24, bottom: 8),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(16),
        border: Border.all(color: Colors.grey.shade300),
        boxShadow: [BoxShadow(color: Colors.black.withOpacity(0.05), blurRadius: 10, offset: const Offset(0, 4))],
      ),
      child: LineChart(
        LineChartData(
          minX: 0,
          maxX: spots.length > 1 ? spots.length.toDouble() - 1 : 1,
          minY: 0,
          maxY: maxY * 1.2,
          lineBarsData: [
            LineChartBarData(
              spots: spots,
              isCurved: true,
              color: Colors.deepPurpleAccent,
              barWidth: 4,
              isStrokeCapRound: true,
              dotData: const FlDotData(show: true),
              belowBarData: BarAreaData(show: true, color: Colors.deepPurpleAccent.withOpacity(0.2)),
            ),
          ],
          titlesData: FlTitlesData(
            leftTitles: const AxisTitles(sideTitles: SideTitles(showTitles: true, reservedSize: 40)),
            bottomTitles: AxisTitles(
              sideTitles: SideTitles(
                showTitles: true,
                getTitlesWidget: (val, meta) {
                  final int idx = val.toInt();
                  if (idx >= 0 && idx < _dailyRevenue.length) {
                    final dateStr = _dailyRevenue[idx]['date'] as String;
                    final parts = dateStr.split('-');
                    if (parts.length == 3) {
                      return Padding(
                        padding: const EdgeInsets.only(top: 8),
                        child: Text('${parts[1]}/${parts[2]}', style: const TextStyle(fontSize: 10, fontWeight: FontWeight.bold)),
                      );
                    }
                  }
                  return const Text('');
                },
              ),
            ),
            rightTitles: const AxisTitles(sideTitles: SideTitles(showTitles: false)),
            topTitles: const AxisTitles(sideTitles: SideTitles(showTitles: false)),
          ),
          gridData: const FlGridData(show: true, drawVerticalLine: false),
          borderData: FlBorderData(show: false),
          lineTouchData: LineTouchData(
            touchTooltipData: LineTouchTooltipData(
              getTooltipItems: (touchedSpots) {
                return touchedSpots.map((spot) {
                  final rev = _dailyRevenue[spot.x.toInt()];
                  return LineTooltipItem(
                    'PKR ${spot.y.toStringAsFixed(2)}\n${rev['order_count']} orders',
                    const TextStyle(color: Colors.white, fontWeight: FontWeight.bold),
                  );
                }).toList();
              },
            ),
          ),
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.grey[50],
      appBar: AppBar(
        title: const Text('Financial Dashboard', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
        elevation: 0,
        actions: [
          TextButton.icon(
            onPressed: () => Navigator.pushNamed(context, '/admin-surveillance'),
            icon: const Icon(Icons.shield_outlined, color: Colors.white70),
            label: const Text('Surveillance', style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold)),
          ),
          TextButton.icon(
            onPressed: () => Navigator.pushNamed(context, '/admin-ai-control'),
            icon: const Icon(Icons.auto_fix_high, color: Colors.cyanAccent),
            label: const Text('AI Control', style: TextStyle(color: Colors.cyanAccent, fontWeight: FontWeight.bold)),
          ),
          IconButton(onPressed: _showApiKeysPanel, icon: const Icon(Icons.vpn_key)),
          IconButton(onPressed: _fetchFinanceData, icon: const Icon(Icons.refresh_outlined)),
        ],
      ),
      body: _isLoading
          ? const Center(child: CircularProgressIndicator(color: Colors.black87))
          : Padding(
              padding: const EdgeInsets.all(16.0),
              child: CustomScrollView(
                slivers: [
                  SliverToBoxAdapter(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        // Tab Selector
                        Container(
                          margin: const EdgeInsets.only(bottom: 16),
                          decoration: BoxDecoration(
                            color: Colors.grey.shade200,
                            borderRadius: BorderRadius.circular(12),
                          ),
                          child: Row(
                            children: [
                              Expanded(
                                child: GestureDetector(
                                  onTap: () => setState(() => _selectedFinanceTab = 0),
                                  child: Container(
                                    padding: const EdgeInsets.symmetric(vertical: 12),
                                    decoration: BoxDecoration(
                                      color: _selectedFinanceTab == 0 ? Colors.black87 : Colors.transparent,
                                      borderRadius: BorderRadius.circular(12),
                                    ),
                                    alignment: Alignment.center,
                                    child: Text(
                                      'Ledger & GMV',
                                      style: TextStyle(
                                        color: _selectedFinanceTab == 0 ? Colors.white : Colors.black87,
                                        fontWeight: FontWeight.bold,
                                      ),
                                    ),
                                  ),
                                ),
                              ),
                              Expanded(
                                child: GestureDetector(
                                  onTap: () => setState(() => _selectedFinanceTab = 1),
                                  child: Container(
                                    padding: const EdgeInsets.symmetric(vertical: 12),
                                    decoration: BoxDecoration(
                                      color: _selectedFinanceTab == 1 ? Colors.deepOrange : Colors.transparent,
                                      borderRadius: BorderRadius.circular(12),
                                    ),
                                    alignment: Alignment.center,
                                    child: Text(
                                      'PayFast Gateway Hub',
                                      style: TextStyle(
                                        color: _selectedFinanceTab == 1 ? Colors.white : Colors.black87,
                                        fontWeight: FontWeight.bold,
                                      ),
                                    ),
                                  ),
                                ),
                              ),
                            ],
                          ),
                        ),

                        // PayFast Realtime Status Banner (Always Visible)
                        const Text('PayFast Gateway Live Analytics', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: Colors.black87)),
                        const SizedBox(height: 12),
                        Row(
                          children: [
                            _buildKPICard('Passed / Captured', (_payfastSummary?['passed_volume'] as num?)?.toDouble() ?? 0.0, Colors.green, Icons.check_circle, subtitle: '${_payfastSummary?['passed_count'] ?? 0} txns'),
                            const SizedBox(width: 12),
                            _buildKPICard('Failed Payments', (_payfastSummary?['failed_volume'] as num?)?.toDouble() ?? 0.0, Colors.redAccent, Icons.cancel, subtitle: '${_payfastSummary?['failed_count'] ?? 0} txns'),
                            const SizedBox(width: 12),
                            _buildKPICard('In-Flight / Script', (_payfastSummary?['in_flight_volume'] as num?)?.toDouble() ?? 0.0, Colors.amber.shade800, Icons.hourglass_top, subtitle: '${_payfastSummary?['in_flight_count'] ?? 0} txns'),
                          ],
                        ),
                        const SizedBox(height: 24),

                        if (_selectedFinanceTab == 0) ...[
                          const Text('Global Ledger (TigerBeetle)', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: Colors.black87)),
                          const SizedBox(height: 12),
                          Row(
                            children: [
                              _buildKPICard('Platform Revenue', (_kpis?['admin_revenue'] as num?)?.toDouble() ?? 0.0, Colors.green, Icons.account_balance),
                              const SizedBox(width: 12),
                              _buildKPICard('Pending Escrow', (_kpis?['central_escrow'] as num?)?.toDouble() ?? 0.0, Colors.orange, Icons.lock_clock),
                              const SizedBox(width: 12),
                              _buildKPICard('Rider Cash Float', (_kpis?['rider_cash_debt'] as num?)?.toDouble() ?? 0.0, Colors.redAccent, Icons.money_off),
                            ],
                          ),
                          const SizedBox(height: 24),
                          Row(
                            mainAxisAlignment: MainAxisAlignment.spaceBetween,
                            children: [
                              const Text('Historical GMV', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: Colors.black87)),
                              Row(
                                children: [
                                  DropdownButton<int>(
                                    value: _daysFilter,
                                    underline: const SizedBox(),
                                    icon: const Icon(Icons.calendar_today, size: 16),
                                    items: const [
                                      DropdownMenuItem(value: 7, child: Text('7 Days')),
                                      DropdownMenuItem(value: 30, child: Text('30 Days')),
                                    ],
                                    onChanged: (val) {
                                      if (val != null) {
                                        setState(() => _daysFilter = val);
                                        _fetchFinanceData();
                                      }
                                    },
                                  ),
                                  const SizedBox(width: 16),
                                  DropdownButton<String>(
                                    value: _paymentFilter,
                                    underline: const SizedBox(),
                                    icon: const Icon(Icons.filter_list, size: 16),
                                    items: const [
                                      DropdownMenuItem(value: 'all', child: Text('All Methods')),
                                      DropdownMenuItem(value: 'cod', child: Text('Cash on Delivery')),
                                      DropdownMenuItem(value: 'card', child: Text('Credit Card')),
                                    ],
                                    onChanged: (val) {
                                      if (val != null) {
                                        setState(() => _paymentFilter = val);
                                        _fetchFinanceData();
                                      }
                                    },
                                  ),
                                ],
                              ),
                            ],
                          ),
                          const SizedBox(height: 12),
                          _buildRevenueChart(),
                          const SizedBox(height: 24),
                          const Text('Recent Orders & Payments', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: Colors.black87)),
                          const SizedBox(height: 12),
                        ] else ...[
                          Row(
                            mainAxisAlignment: MainAxisAlignment.spaceBetween,
                            children: [
                              const Text('PayFast Transactions', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: Colors.black87)),
                              DropdownButton<String>(
                                value: _payfastFilter,
                                underline: const SizedBox(),
                                icon: const Icon(Icons.filter_list, size: 16),
                                items: const [
                                  DropdownMenuItem(value: 'all', child: Text('All Transactions')),
                                  DropdownMenuItem(value: 'captured', child: Text('Passed (Captured)')),
                                  DropdownMenuItem(value: 'failed', child: Text('Failed')),
                                  DropdownMenuItem(value: 'in_flight', child: Text('In-Flight (Script/3DS)')),
                                ],
                                onChanged: (val) {
                                  if (val != null) {
                                    setState(() => _payfastFilter = val);
                                    _fetchFinanceData();
                                  }
                                },
                              ),
                            ],
                          ),
                          const SizedBox(height: 12),
                        ],
                      ],
                    ),
                  ),

                  // If Tab 0 -> Order payments list
                  if (_selectedFinanceTab == 0)
                    SliverList(
                      delegate: SliverChildBuilderDelegate(
                        (context, index) {
                          final pay = _payments[index];
                          final status = (pay['payment_status'] as String?) ?? 'unknown';
                          final method = (pay['payment_method'] as String?) ?? 'unknown';
                          final amount = (pay['total_amount'] as num?)?.toDouble() ?? 0.0;
                          final isCompleted = status == 'completed';

                          return Container(
                            margin: const EdgeInsets.only(bottom: 8),
                            decoration: BoxDecoration(
                              color: Colors.white,
                              borderRadius: BorderRadius.circular(12),
                              border: Border.all(color: Colors.grey.shade200),
                            ),
                            child: ListTile(
                              leading: CircleAvatar(
                                backgroundColor: isCompleted ? Colors.green.shade100 : Colors.orange.shade100,
                                child: Icon(isCompleted ? Icons.check_circle : Icons.pending, color: isCompleted ? Colors.green : Colors.orange),
                              ),
                              title: Text((pay['customer_name'] as String?) ?? 'N/A', style: const TextStyle(fontWeight: FontWeight.bold)),
                              subtitle: Text('Order: ${(pay['order_id'] as String?) ?? 'N/A'} • ${method.toUpperCase()}'),
                              trailing: Column(
                                mainAxisAlignment: MainAxisAlignment.center,
                                crossAxisAlignment: CrossAxisAlignment.end,
                                children: [
                                  Text('PKR ${amount.toStringAsFixed(2)}', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 15)),
                                  Text(status.toUpperCase(), style: TextStyle(color: isCompleted ? Colors.green : Colors.orange, fontSize: 10, fontWeight: FontWeight.bold)),
                                ],
                              ),
                            ),
                          );
                        },
                        childCount: _payments.length,
                      ),
                    )
                  else
                    // Tab 1 -> PayFast Detailed Gateway Transactions
                    _payfastTransactions.isEmpty
                        ? const SliverToBoxAdapter(
                            child: Padding(
                              padding: EdgeInsets.all(32.0),
                              child: Center(child: Text('No PayFast transactions found for this filter.', style: TextStyle(color: Colors.grey))),
                            ),
                          )
                        : SliverList(
                            delegate: SliverChildBuilderDelegate(
                              (context, index) {
                                final txn = _payfastTransactions[index];
                                final status = (txn['status'] as String?) ?? 'unknown';
                                final amount = (txn['amount'] as num?)?.toDouble() ?? 0.0;
                                final orderId = (txn['order_id'] as String?) ?? 'N/A';
                                final gatewayTxnId = (txn['gateway_txn_id'] as String?) ?? '';
                                final errorMsg = (txn['error_message'] as String?) ?? '';
                                final customerName = (txn['customer_name'] as String?) ?? 'Customer';
                                final createdAt = (txn['created_at'] as String?) ?? '';

                                Color statusColor = Colors.grey;
                                IconData statusIcon = Icons.help_outline;
                                if (status == 'captured') {
                                  statusColor = Colors.green;
                                  statusIcon = Icons.check_circle;
                                } else if (status == 'failed') {
                                  statusColor = Colors.redAccent;
                                  statusIcon = Icons.cancel;
                                } else if (status == '3ds_required') {
                                  statusColor = Colors.purple;
                                  statusIcon = Icons.security;
                                } else if (status == 'settlement_pending' || status == 'processing' || status == 'gateway_pending') {
                                  statusColor = Colors.orange;
                                  statusIcon = Icons.sync;
                                }

                                return Container(
                                  margin: const EdgeInsets.only(bottom: 10),
                                  padding: const EdgeInsets.all(12),
                                  decoration: BoxDecoration(
                                    color: Colors.white,
                                    borderRadius: BorderRadius.circular(12),
                                    border: Border.all(color: statusColor.withOpacity(0.3)),
                                    boxShadow: [
                                      BoxShadow(color: Colors.black.withOpacity(0.02), blurRadius: 4, offset: const Offset(0, 2)),
                                    ],
                                  ),
                                  child: Column(
                                    crossAxisAlignment: CrossAxisAlignment.start,
                                    children: [
                                      Row(
                                        mainAxisAlignment: MainAxisAlignment.spaceBetween,
                                        children: [
                                          Row(
                                            children: [
                                              Icon(statusIcon, color: statusColor, size: 18),
                                              const SizedBox(width: 6),
                                              Container(
                                                padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
                                                decoration: BoxDecoration(
                                                  color: statusColor.withOpacity(0.15),
                                                  borderRadius: BorderRadius.circular(6),
                                                ),
                                                child: Text(
                                                  status.toUpperCase(),
                                                  style: TextStyle(color: statusColor, fontSize: 11, fontWeight: FontWeight.bold),
                                                ),
                                              ),
                                            ],
                                          ),
                                          Text(
                                            'PKR ${amount.toStringAsFixed(2)}',
                                            style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 16),
                                          ),
                                        ],
                                      ),
                                      const SizedBox(height: 8),
                                      Text('Order: $orderId • $customerName', style: const TextStyle(fontWeight: FontWeight.w600, fontSize: 13)),
                                      if (gatewayTxnId.isNotEmpty)
                                        Text('PayFast Ref: $gatewayTxnId', style: TextStyle(color: Colors.grey.shade700, fontSize: 11)),
                                      if (errorMsg.isNotEmpty)
                                        Padding(
                                          padding: const EdgeInsets.only(top: 4),
                                          child: Text('Reason: $errorMsg', style: const TextStyle(color: Colors.redAccent, fontSize: 11, fontStyle: FontStyle.italic)),
                                        ),
                                      const SizedBox(height: 4),
                                      Text(createdAt, style: TextStyle(color: Colors.grey.shade500, fontSize: 10)),
                                    ],
                                  ),
                                );
                              },
                              childCount: _payfastTransactions.length,
                            ),
                          ),
                ],
              ),
            ),
    );
  }
}

// _ApiKeysPanel is a real, working admin modal for managing payment-gateway
// API keys. It talks to the backend POST /api/admin/finance/api-keys endpoint
// and shows the rotated fingerprint + version so the admin can confirm the
// write actually happened (not a fake snackbar like the previous dead UI).
class _ApiKeysPanel extends StatefulWidget {
  const _ApiKeysPanel({required this.onSaved});
  final VoidCallback onSaved;

  @override
  State<_ApiKeysPanel> createState() => _ApiKeysPanelState();
}

class _ApiKeysPanelState extends State<_ApiKeysPanel> {
  // Whitelist of providers the admin can configure. Must match the Go
  // service's allowedProviders map. Keeping it client-side too means a
  // typo in a provider name is caught immediately.
  static const List<Map<String, String>> _providers = [
    {
      'provider': 'stripe',
      'key_names': 'secret_key,webhook_secret,publishable_key',
    },
    {'provider': 'payfast', 'key_names': 'merchant_id,secured_key'},
    {
      'provider': 'jazzcash',
      'key_names': 'merchant_id,password,integerity_salt',
    },
    {'provider': 'easypaisa', 'key_names': 'merchant_id,store_id,hash_key'},
    {'provider': 'osrm', 'key_names': 'api_key'},
  ];

  String _selectedProvider = 'stripe';
  String _selectedKeyName = 'secret_key';
  final _valueController = TextEditingController();
  bool _obscure = true;
  bool _saving = false;
  List<dynamic> _existingKeys = [];
  bool _loadingExisting = true;

  @override
  void initState() {
    super.initState();
    _refreshExisting();
  }

  @override
  void dispose() {
    _valueController.dispose();
    super.dispose();
  }

  List<String> _keyNamesFor(String provider) {
    final p = _providers.firstWhere(
      (e) => e['provider'] == provider,
      orElse: () => {'key_names': ''},
    );
    return (p['key_names'] ?? '').split(',').where((s) => s.isNotEmpty).toList();
  }

  Future<void> _refreshExisting() async {
    setState(() => _loadingExisting = true);
    try {
      final data = await sl<ApiClient>().get('/admin/finance/api-keys');
      if (data is Map<String, dynamic>) {
        _existingKeys = (data['api_keys'] as List<dynamic>?) ?? [];
      }
    } catch (e) {
      // Non-fatal: the form is still usable, we just don't show existing keys.
      debugPrint('Failed to load existing API keys: $e');
    } finally {
      if (mounted) setState(() => _loadingExisting = false);
    }
  }

  Future<void> _save() async {
    final value = _valueController.text.trim();
    if (value.isEmpty) {
      _toast('Value must not be empty');
      return;
    }
    setState(() => _saving = true);
    try {
      final body = await sl<ApiClient>().post('/admin/finance/api-keys', {
        'provider': _selectedProvider,
        'key_name': _selectedKeyName,
        'value': value,
      });

      if (body is Map<String, dynamic>) {
        final record = body['record'] as Map<String, dynamic>?;
        final fp = record?['fingerprint'] as String? ?? 'unknown';
        final revealed = body['reveal_once'] as String? ?? '';
        _toast(
          'Saved. Fingerprint: $fp${revealed.isNotEmpty ? ' • copied: $revealed' : ''}',
          background: Colors.green,
        );
        _valueController.clear();
        widget.onSaved();
        await _refreshExisting();
      } else {
        _toast('Save succeeded but returned unexpected data');
      }
    } catch (e) {
      _toast('Network error: $e');
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  Future<void> _delete(String provider, String keyName) async {
    final confirm = await showDialog<bool>(
      context: context,
      builder: (dctx) => AlertDialog(
        title: const Text('Delete key?'),
        content: Text(
          'This will revoke the $provider / $keyName credential. Downstream '
          'services will fail to authenticate against $provider until a new '
          'key is set.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(dctx, false),
            child: const Text('Cancel'),
          ),
          TextButton(
            onPressed: () => Navigator.pop(dctx, true),
            style: TextButton.styleFrom(foregroundColor: Colors.red),
            child: const Text('Delete'),
          ),
        ],
      ),
    );
    if (confirm != true) return;

    try {
      await sl<ApiClient>().delete('/admin/finance/api-keys/$provider/$keyName');
      _toast('Deleted', background: Colors.orange);
      await _refreshExisting();
    } catch (e) {
      _toast('Network error: $e');
    }
  }

  void _toast(String msg, {Color? background}) {
    if (!mounted) return;
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(content: Text(msg), backgroundColor: background),
    );
  }

  Widget _existingKeysList() {
    if (_loadingExisting) {
      return const Padding(
        padding: EdgeInsets.symmetric(vertical: 8),
        child: Center(child: CircularProgressIndicator(strokeWidth: 2)),
      );
    }
    if (_existingKeys.isEmpty) {
      return const Padding(
        padding: EdgeInsets.symmetric(vertical: 8),
        child: Text(
          'No API keys configured yet.',
          style: TextStyle(color: Colors.black54, fontSize: 12),
        ),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: _existingKeys.map<Widget>((k) {
        final provider = (k['provider'] as String?) ?? '?';
        final keyName = (k['key_name'] as String?) ?? '?';
        final fp = (k['fingerprint'] as String?) ?? '?';
        final version = (k['version'] as num?)?.toInt() ?? 0;
        final rotatedAt = (k['rotated_at'] as String?) ?? '';
        return Container(
          margin: const EdgeInsets.symmetric(vertical: 3),
          padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
          decoration: BoxDecoration(
            color: Colors.grey.shade100,
            borderRadius: BorderRadius.circular(8),
          ),
          child: Row(
            children: [
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      '$provider / $keyName',
                      style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 13),
                    ),
                    Text(
                      'fp: $fp · v$version · $rotatedAt',
                      style: const TextStyle(fontSize: 11, color: Colors.black54),
                    ),
                  ],
                ),
              ),
              IconButton(
                icon: const Icon(Icons.delete_outline, color: Colors.red, size: 20),
                onPressed: () => _delete(provider, keyName),
                tooltip: 'Delete key',
              ),
            ],
          ),
        );
      }).toList(),
    );
  }

  @override
  Widget build(BuildContext context) {
    final keyNames = _keyNamesFor(_selectedProvider);
    return Padding(
      padding: EdgeInsets.only(
        bottom: MediaQuery.of(context).viewInsets.bottom,
        left: 16,
        right: 16,
        top: 24,
      ),
      child: SingleChildScrollView(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text(
              'Merchant Payment API Keys',
              style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold),
            ),
            const SizedBox(height: 4),
            const Text(
              'Stored encrypted at rest. Audit trail records every rotation.',
              style: TextStyle(fontSize: 11, color: Colors.black54),
            ),
            const SizedBox(height: 16),
            const Text('Currently configured:', style: TextStyle(fontWeight: FontWeight.w600)),
            const SizedBox(height: 6),
            _existingKeysList(),
            const Divider(height: 24),
            const Text('Set or rotate a key:', style: TextStyle(fontWeight: FontWeight.w600)),
            const SizedBox(height: 12),
            DropdownButtonFormField<String>(
              key: ValueKey(_selectedProvider),
              value: _selectedProvider,
              decoration: const InputDecoration(
                labelText: 'Provider',
                border: OutlineInputBorder(),
                isDense: true,
              ),
              items: _providers
                  .map((p) => DropdownMenuItem(
                        value: p['provider'],
                        child: Text(p['provider']!),
                      ),)
                  .toList(),
              onChanged: (v) {
                if (v == null) return;
                setState(() {
                  _selectedProvider = v;
                  _selectedKeyName = _keyNamesFor(v).first;
                });
              },
            ),
            const SizedBox(height: 12),
            DropdownButtonFormField<String>(
              key: ValueKey(_selectedKeyName),
              value: _selectedKeyName,
              decoration: const InputDecoration(
                labelText: 'Key name',
                border: OutlineInputBorder(),
                isDense: true,
              ),
              items: keyNames
                  .map((n) => DropdownMenuItem(value: n, child: Text(n)))
                  .toList(),
              onChanged: (v) {
                if (v != null) setState(() => _selectedKeyName = v);
              },
            ),
            const SizedBox(height: 12),
            TextField(
              controller: _valueController,
              obscureText: _obscure,
              decoration: InputDecoration(
                labelText: '$_selectedProvider / $_selectedKeyName value',
                border: const OutlineInputBorder(),
                suffixIcon: IconButton(
                  icon: Icon(_obscure ? Icons.visibility : Icons.visibility_off),
                  onPressed: () => setState(() => _obscure = !_obscure),
                ),
              ),
            ),
            const SizedBox(height: 16),
            SizedBox(
              width: double.infinity,
              child: ElevatedButton(
                onPressed: _saving ? null : _save,
                style: ElevatedButton.styleFrom(
                  backgroundColor: Colors.black87,
                  foregroundColor: Colors.white,
                ),
                child: _saving
                    ? const SizedBox(
                        height: 18,
                        width: 18,
                        child: CircularProgressIndicator(
                          strokeWidth: 2,
                          color: Colors.white,
                        ),
                      )
                    : const Text('Save Key'),
              ),
            ),
            const SizedBox(height: 16),
          ],
        ),
      ),
    );
  }
}
