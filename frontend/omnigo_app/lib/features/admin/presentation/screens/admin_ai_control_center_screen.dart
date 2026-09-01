import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/theme/app_theme.dart';

class AdminAiControlCenterScreen extends StatefulWidget {
  const AdminAiControlCenterScreen({super.key});

  @override
  State<AdminAiControlCenterScreen> createState() => _AdminAiControlCenterScreenState();
}

class _AdminAiControlCenterScreenState extends State<AdminAiControlCenterScreen> {
  bool _isLoading = true;
  bool _isAutoHealing = false;
  Map<String, dynamic>? _auditData;
  List<dynamic> _executionLogs = [];

  @override
  void initState() {
    super.initState();
    _fetchAuditData();
  }

  Future<void> _fetchAuditData() async {
    setState(() => _isLoading = true);
    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      final response = await http.get(
        Uri.parse(ApiEndpoints.adminAiAuditOverview()),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
      ).timeout(const Duration(seconds: 6));

      if (response.statusCode == 200 && mounted) {
        setState(() {
          _auditData = jsonDecode(response.body) as Map<String, dynamic>;
          _isLoading = false;
        });
      } else if (mounted) {
        setState(() => _isLoading = false);
      }
    } catch (e) {
      if (mounted) setState(() => _isLoading = false);
    }
  }

  Future<void> _triggerAutoHeal(String component) async {
    setState(() => _isAutoHealing = true);
    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      final response = await http.post(
        Uri.parse(ApiEndpoints.adminAiAutoHeal()),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
        body: jsonEncode({'target_component': component}),
      ).timeout(const Duration(seconds: 8));

      if (response.statusCode == 200 && mounted) {
        final res = jsonDecode(response.body) as Map<String, dynamic>;
        setState(() {
          _executionLogs = (res['execution_logs'] as List<dynamic>?) ?? [];
          _isAutoHealing = false;
        });
        _showExecutionLogsSheet();
        await _fetchAuditData();
      } else if (mounted) {
        setState(() => _isAutoHealing = false);
      }
    } catch (e) {
      if (mounted) setState(() => _isAutoHealing = false);
    }
  }

  void _showExecutionLogsSheet() {
    showModalBottomSheet<void>(
      context: context,
      backgroundColor: const Color(0xFF1E1E2C),
      shape: const RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(24)),
      ),
      builder: (ctx) => Padding(
        padding: const EdgeInsets.all(24.0),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Row(
              children: [
                Icon(Icons.check_circle_rounded, color: Colors.greenAccent, size: 24),
                SizedBox(width: 10),
                Text(
                  'AI Auto-Heal Execution Logs',
                  style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold, color: Colors.white),
                ),
              ],
            ),
            const SizedBox(height: 16),
            ..._executionLogs.map((log) => Container(
                  margin: const EdgeInsets.only(bottom: 10),
                  padding: const EdgeInsets.all(12),
                  decoration: BoxDecoration(
                    color: Colors.white.withValues(alpha: 0.05),
                    borderRadius: BorderRadius.circular(12),
                    border: Border.all(color: Colors.white.withValues(alpha: 0.1)),
                  ),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Row(
                        mainAxisAlignment: MainAxisAlignment.spaceBetween,
                        children: [
                          Text(
                            log['step']?.toString() ?? 'STEP',
                            style: const TextStyle(color: AppTheme.limeAccent, fontWeight: FontWeight.bold, fontSize: 13),
                          ),
                          Container(
                            padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
                            decoration: BoxDecoration(
                              color: Colors.green.withValues(alpha: 0.2),
                              borderRadius: BorderRadius.circular(8),
                            ),
                            child: Text(
                              log['status']?.toString() ?? 'UNKNOWN',
                              style: const TextStyle(color: Colors.greenAccent, fontSize: 10, fontWeight: FontWeight.bold),
                            ),
                          ),
                        ],
                      ),
                      const SizedBox(height: 4),
                      Text(
                        log['action']?.toString() ?? '',
                        style: const TextStyle(color: Colors.white, fontSize: 12),
                      ),
                    ],
                  ),
                ),),
            const SizedBox(height: 16),
            SizedBox(
              width: double.infinity,
              child: ElevatedButton(
                onPressed: () => Navigator.pop(ctx),
                style: ElevatedButton.styleFrom(backgroundColor: AppTheme.limeAccent, foregroundColor: Colors.black),
                child: const Text('Close Logs'),
              ),
            ),
          ],
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final fraudLogs = (_auditData?['fraud_logs'] as List<dynamic>?) ?? [];
    final missingItems = (_auditData?['missing_tracking_items'] as List<dynamic>?) ?? [];
    final payout = _auditData?['payout_integrity'] as Map<String, dynamic>?;

    return Scaffold(
      backgroundColor: const Color(0xFF0F0F1A),
      appBar: AppBar(
        backgroundColor: const Color(0xFF1E1E2C),
        elevation: 0,
        title: const Text('AI Self-Healing Control Center', style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontSize: 18)),
        actions: [
          TextButton.icon(
            onPressed: () => Navigator.pushNamed(context, '/admin-surveillance'),
            icon: const Icon(Icons.shield_outlined, color: Colors.white70),
            label: const Text('Surveillance', style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold)),
          ),
          TextButton.icon(
            onPressed: () => Navigator.pushNamed(context, '/admin-finance'),
            icon: const Icon(Icons.account_balance, color: AppTheme.limeAccent),
            label: const Text('Finance', style: TextStyle(color: AppTheme.limeAccent, fontWeight: FontWeight.bold)),
          ),
          IconButton(
            icon: const Icon(Icons.refresh, color: AppTheme.limeAccent),
            onPressed: _fetchAuditData,
          ),
        ],
      ),
      body: _isLoading
          ? const Center(child: CircularProgressIndicator(color: AppTheme.limeAccent))
          : SingleChildScrollView(
              padding: const EdgeInsets.all(20),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  // System Health Banner Card
                  Container(
                    width: double.infinity,
                    padding: const EdgeInsets.all(20),
                    decoration: BoxDecoration(
                      gradient: const LinearGradient(
                        colors: [
                          Color(0xFF1E1E2C),
                          Color(0xFF2A2A3D),
                        ],
                      ),
                      borderRadius: BorderRadius.circular(24),
                      border: Border.all(color: AppTheme.limeAccent.withValues(alpha: 0.3)),
                      boxShadow: [
                        BoxShadow(color: AppTheme.limeAccent.withValues(alpha: 0.05), blurRadius: 15, spreadRadius: 2),
                      ],
                    ),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Row(
                          mainAxisAlignment: MainAxisAlignment.spaceBetween,
                          children: [
                            const Row(
                              children: [
                                Icon(Icons.shield_outlined, color: AppTheme.limeAccent, size: 28),
                                SizedBox(width: 10),
                                Text('System Security Score', style: TextStyle(color: Colors.white, fontSize: 16, fontWeight: FontWeight.bold)),
                              ],
                            ),
                            Container(
                              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
                              decoration: BoxDecoration(
                                color: Colors.greenAccent.withValues(alpha: 0.2),
                                borderRadius: BorderRadius.circular(16),
                                border: Border.all(color: Colors.greenAccent),
                              ),
                              child: Text(
                                _auditData?['security_score']?.toString() ?? '—',
                                style: const TextStyle(color: Colors.greenAccent, fontWeight: FontWeight.bold, fontSize: 12),
                              ),
                            ),
                          ],
                        ),
                        const SizedBox(height: 12),
                        Text(
                          'Active Engine: ${_auditData?['active_ai_engine']?.toString() ?? 'unknown'}',
                          style: TextStyle(color: Colors.grey.shade400, fontSize: 12),
                        ),
                      ],
                    ),
                  ),

                  const SizedBox(height: 24),

                  // Action Header Row
                  Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    children: [
                      const Text(
                        'AI Fraud & Risk Stream',
                        style: TextStyle(color: Colors.white, fontSize: 18, fontWeight: FontWeight.bold),
                      ),
                      ElevatedButton.icon(
                        onPressed: _isAutoHealing ? null : () => _triggerAutoHeal('ALL'),
                        icon: _isAutoHealing
                            ? const SizedBox(width: 14, height: 14, child: CircularProgressIndicator(strokeWidth: 2, color: Colors.black))
                            : const Icon(Icons.auto_fix_high, size: 16),
                        label: Text(_isAutoHealing ? 'Healing...' : 'AI Auto-Fix All'),
                        style: ElevatedButton.styleFrom(
                          backgroundColor: AppTheme.limeAccent,
                          foregroundColor: Colors.black,
                          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(14)),
                        ),
                      ),
                    ],
                  ),

                  const SizedBox(height: 12),

                  // Fraud Logs List
                  ...fraudLogs.map((log) => Container(
                        margin: const EdgeInsets.only(bottom: 12),
                        padding: const EdgeInsets.all(16),
                        decoration: BoxDecoration(
                          color: const Color(0xFF1E1E2C),
                          borderRadius: BorderRadius.circular(16),
                          border: Border.all(color: Colors.redAccent.withValues(alpha: 0.3)),
                        ),
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Row(
                              mainAxisAlignment: MainAxisAlignment.spaceBetween,
                              children: [
                                Row(
                                  children: [
                                    const Icon(Icons.warning_amber_rounded, color: Colors.redAccent, size: 20),
                                    const SizedBox(width: 8),
                                    Text(log['type']?.toString() ?? 'EVENT',
                                        style: const TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontSize: 14),),
                                  ],
                                ),
                                Container(
                                  padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
                                  decoration: BoxDecoration(
                                    color: Colors.redAccent.withValues(alpha: 0.2),
                                    borderRadius: BorderRadius.circular(8),
                                  ),
                                  child: Text('HGNN Risk: ${(log['risk_score'] as num?)?.toStringAsFixed(2) ?? "0.90"}',
                                      style: const TextStyle(color: Colors.redAccent, fontWeight: FontWeight.bold, fontSize: 11),),
                                ),
                              ],
                            ),
                            const SizedBox(height: 8),
                            Text('User: ${log['user_id']} | Device: ${log['device_id']}', style: const TextStyle(color: Colors.grey, fontSize: 12)),
                            const SizedBox(height: 4),
                            Text(log['detail']?.toString() ?? '', style: TextStyle(color: Colors.grey.shade300, fontSize: 12)),
                          ],
                        ),
                      ),),

                  const SizedBox(height: 24),

                  // Missing Tracking ID Remediation Section
                  const Text('Missing Tracking & Order Remediation', style: TextStyle(color: Colors.white, fontSize: 18, fontWeight: FontWeight.bold)),
                  const SizedBox(height: 12),

                  if (missingItems.isEmpty)
                    Container(
                      width: double.infinity,
                      padding: const EdgeInsets.all(16),
                      decoration: BoxDecoration(
                        color: const Color(0xFF1E1E2C),
                        borderRadius: BorderRadius.circular(16),
                      ),
                      child: const Row(
                        children: [
                          Icon(Icons.check_circle_outline, color: Colors.greenAccent, size: 20),
                          SizedBox(width: 10),
                          Text('All order tracking IDs are verified and valid!', style: TextStyle(color: Colors.white, fontSize: 13)),
                        ],
                      ),
                    )
                  else
                    ...missingItems.map((item) => Container(
                          margin: const EdgeInsets.only(bottom: 12),
                          padding: const EdgeInsets.all(16),
                          decoration: BoxDecoration(
                            color: const Color(0xFF1E1E2C),
                            borderRadius: BorderRadius.circular(16),
                            border: Border.all(color: Colors.amber.withValues(alpha: 0.4)),
                          ),
                          child: Row(
                            mainAxisAlignment: MainAxisAlignment.spaceBetween,
                            children: [
                              Column(
                                crossAxisAlignment: CrossAxisAlignment.start,
                                children: [
                                  Text('Order #${item['order_id']}', style: const TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontSize: 14)),
                                  const SizedBox(height: 4),
                                  Text(item['issue']?.toString() ?? '', style: const TextStyle(color: Colors.amber, fontSize: 12)),
                                ],
                              ),
                              OutlinedButton(
                                onPressed: () => _triggerAutoHeal('TRACKING_IDS'),
                                style: OutlinedButton.styleFrom(foregroundColor: AppTheme.limeAccent, side: const BorderSide(color: AppTheme.limeAccent)),
                                child: const Text('Repair ID'),
                              ),
                            ],
                          ),
                        ),),

                  const SizedBox(height: 24),

                  // Transaction & Payout Integrity Section
                  const Text('Financial & Payout Integrity Audit', style: TextStyle(color: Colors.white, fontSize: 18, fontWeight: FontWeight.bold)),
                  const SizedBox(height: 12),

                  Container(
                    width: double.infinity,
                    padding: const EdgeInsets.all(16),
                    decoration: BoxDecoration(
                      color: const Color(0xFF1E1E2C),
                      borderRadius: BorderRadius.circular(16),
                      border: Border.all(color: Colors.blueAccent.withValues(alpha: 0.3)),
                    ),
                    child: Column(
                      children: [
                        Row(
                          mainAxisAlignment: MainAxisAlignment.spaceBetween,
                          children: [
                            const Text('Audited Transactions', style: TextStyle(color: Colors.grey, fontSize: 13)),
                            Text(payout?['total_audited_transactions']?.toString() ?? '—', style: const TextStyle(color: Colors.white, fontWeight: FontWeight.bold, fontSize: 13)),
                          ],
                        ),
                        const Divider(color: Colors.white10),
                        Row(
                          mainAxisAlignment: MainAxisAlignment.spaceBetween,
                          children: [
                            const Text('Vendor Payouts Matched', style: TextStyle(color: Colors.grey, fontSize: 13)),
                            Text(payout?['vendor_payouts_matched']?.toString() ?? '—', style: const TextStyle(color: Colors.greenAccent, fontWeight: FontWeight.bold, fontSize: 13)),
                          ],
                        ),
                        const Divider(color: Colors.white10),
                        Row(
                          mainAxisAlignment: MainAxisAlignment.spaceBetween,
                          children: [
                            const Text('Admin Commission Reconciled', style: TextStyle(color: Colors.grey, fontSize: 13)),
                            Text(payout?['admin_commission_reconciled']?.toString() ?? '—', style: const TextStyle(color: Colors.greenAccent, fontWeight: FontWeight.bold, fontSize: 13)),
                          ],
                        ),
                      ],
                    ),
                  ),

                  const SizedBox(height: 40),
                ],
              ),
            ),
    );
  }
}
