import 'dart:async';
import 'package:flutter/material.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';
import '../../../../core/theme/app_theme.dart';

class AdminSurveillanceScreen extends StatefulWidget {
  const AdminSurveillanceScreen({super.key});

  @override
  State<AdminSurveillanceScreen> createState() =>
      _AdminSurveillanceScreenState();
}

class _AdminSurveillanceScreenState extends State<AdminSurveillanceScreen> {
  // Lineage search
  final TextEditingController _orderIdController = TextEditingController();
  Map<String, dynamic>? _lineageReport;
  bool _isLoadingLineage = false;

  // Pending approvals (legacy manual list)
  List<dynamic> _pendingUsers = [];
  bool _isLoadingPending = false;

  // All users
  List<dynamic> _allUsers = [];
  bool _isLoadingUsers = false;

  // KYC/KYB verifications
  List<dynamic> _verifications = [];
  bool _isLoadingVerifications = false;


  @override
  void initState() {
    super.initState();
    _fetchPendingVerifications();
    _fetchAllUsers();
    _fetchVerifications();
  }

  // ── API calls ────────────────────────────────────────────────

  Future<void> _fetchLineage() async {
    final orderId = _orderIdController.text.trim();
    if (orderId.isEmpty) return;

    setState(() => _isLoadingLineage = true);

    final isRide = orderId.toUpperCase().startsWith('RIDE-');
    final path = isRide ? '/admin/lineage/ride/$orderId' : '/admin/lineage/$orderId/full';

    try {
      final data = await sl<ApiClient>().get(path);
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _lineageReport = data;
          _isLoadingLineage = false;
        });
      }
    } catch (e) {
      if (mounted) {
        setState(() => _isLoadingLineage = false);
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text(isRide ? 'Ride not found: $e' : 'Order not found: $e')),
        );
      }
    }
  }

  Future<void> _fetchPendingVerifications() async {
    if (_isLoadingPending) return;
    setState(() => _isLoadingPending = true);

    try {
      final data = await sl<ApiClient>().get('/admin/users/pending');
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _pendingUsers = data['pending_users'] as List<dynamic>? ?? [];
          _isLoadingPending = false;
        });
      }
    } catch (e) {
      if (mounted) setState(() => _isLoadingPending = false);
    }
  }

  Future<void> _approveUser(String trackingId) async {
    if (trackingId.isEmpty) return;
    try {
      await sl<ApiClient>().patch('/admin/users/$trackingId/approve', {});
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('User approved!'), backgroundColor: Colors.green),
        );
        unawaited(_fetchPendingVerifications());
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Approval failed: $e'), backgroundColor: Colors.redAccent),
        );
      }
    }
  }

  // ── KYC/KYB verification API calls ────────────────────────────

  Future<void> _fetchVerifications() async {
    if (_isLoadingVerifications) return;
    setState(() => _isLoadingVerifications = true);

    try {
      final data = await sl<ApiClient>().get('/admin/verifications/pending');
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _verifications = data['pending_users'] as List<dynamic>? ?? [];
          _isLoadingVerifications = false;
        });
      }
    } catch (e) {
      if (mounted) setState(() => _isLoadingVerifications = false);
    }
  }

  Future<void> _approveVerification(String trackingId) async {
    if (trackingId.isEmpty) return;
    try {
      await sl<ApiClient>().post('/admin/verifications/$trackingId/approve', {
        'reason': 'Manual admin approval',
      });
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Verification approved!'), backgroundColor: Colors.green),
        );
        unawaited(_fetchVerifications());
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Approval failed: $e'), backgroundColor: Colors.redAccent),
        );
      }
    }
  }

  Future<void> _rejectVerification(String trackingId) async {
    if (trackingId.isEmpty) return;
    final reasonController = TextEditingController();
    final reason = await showDialog<String>(
      context: context,
      builder: (context) => AlertDialog(
        backgroundColor: Colors.white,
        title: const Text('Reject Verification', style: TextStyle(fontWeight: FontWeight.bold)),
        content: TextField(
          controller: reasonController,
          decoration: const InputDecoration(hintText: 'Enter rejection reason'),
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(context), child: const Text('Cancel')),
          TextButton(
            onPressed: () => Navigator.pop(context, reasonController.text.trim()),
            child: const Text('Reject', style: TextStyle(color: Colors.redAccent)),
          ),
        ],
      ),
    );
    if (reason == null || reason.isEmpty) {
      reasonController.dispose();
      return;
    }

    try {
      await sl<ApiClient>().post('/admin/verifications/$trackingId/reject', {
        'reason': reason,
      });
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Verification rejected'), backgroundColor: Colors.orange),
        );
        unawaited(_fetchVerifications());
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Rejection failed: $e'), backgroundColor: Colors.redAccent),
        );
      }
    } finally {
      reasonController.dispose();
    }
  }

  Future<void> _fetchAllUsers() async {
    if (_isLoadingUsers) return;
    setState(() => _isLoadingUsers = true);

    try {
      final data = await sl<ApiClient>().get('/admin/users');
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _allUsers = data['users'] as List<dynamic>? ?? [];
          _isLoadingUsers = false;
        });
      }
    } catch (e) {
      if (mounted) setState(() => _isLoadingUsers = false);
    }
  }

  @override
  void dispose() {
    _orderIdController.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return DefaultTabController(
      length: 4,
      child: Scaffold(
        backgroundColor: Colors.grey[100],
        appBar: AppBar(
          title: const Text('Admin Surveillance Hub', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
          backgroundColor: Colors.black87,
          elevation: 0,
          actions: [
            TextButton.icon(
              onPressed: () => Navigator.pushNamed(context, '/admin-ai-control'),
              icon: const Icon(Icons.auto_fix_high, color: Colors.cyanAccent),
              label: const Text('AI Self-Healing', style: TextStyle(color: Colors.cyanAccent, fontWeight: FontWeight.bold)),
            ),
            TextButton.icon(
              onPressed: () => Navigator.pushNamed(context, '/admin-finance'),
              icon: const Icon(Icons.account_balance, color: AppTheme.limeAccent),
              label: const Text('Finance', style: TextStyle(color: AppTheme.limeAccent, fontWeight: FontWeight.bold)),
            ),
          ],
          bottom: TabBar(
            tabs: const [
              Tab(icon: Icon(Icons.search_outlined), text: 'Lineage'),
              Tab(icon: Icon(Icons.pending_actions_outlined), text: 'Pending'),
              Tab(icon: Icon(Icons.people_outline), text: 'Users'),
              Tab(icon: Icon(Icons.verified_user_outlined), text: 'KYC/KYB'),
            ],
            indicatorColor: AppTheme.limeAccent,
            labelColor: AppTheme.limeAccent,
            unselectedLabelColor: Colors.white54,
            onTap: (i) {},
          ),
        ),
        body: TabBarView(
          children: [
            _buildLineageTab(),
            _buildPendingTab(),
            _buildUsersTab(),
            _buildVerificationsTab(),
          ],
        ),
      ),
    );
  }

  // ── Lineage Tab ──────────────────────────────────────────────

  Widget _buildLineageTab() {
    return Padding(
      padding: const EdgeInsets.all(16.0),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const Text('Order Lineage Search', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: Colors.black87)),
          const SizedBox(height: 16),
          Row(
            children: [
              Expanded(
                child: TextField(
                  controller: _orderIdController,
                  decoration: InputDecoration(
                    hintText: 'Enter Order Tracking ID (e.g. ORDR-...)',
                    border: OutlineInputBorder(borderRadius: BorderRadius.circular(16)),
                    prefixIcon: const Icon(Icons.search),
                  ),
                  onSubmitted: (_) => _fetchLineage(),
                ),
              ),
              const SizedBox(width: 12),
              ElevatedButton(
                onPressed: _isLoadingLineage ? null : _fetchLineage,
                style: ElevatedButton.styleFrom(backgroundColor: Colors.black87, padding: const EdgeInsets.symmetric(vertical: 16)),
                child: const Text('Search', style: TextStyle(color: AppTheme.limeAccent)),
              ),
            ],
          ),
          const SizedBox(height: 20),
          if (_isLoadingLineage)
            const Center(child: CircularProgressIndicator(color: Colors.black87))
          else if (_lineageReport != null)
            if (_lineageReport!.containsKey('ride_id'))
              _buildRideLineageCard(_lineageReport!)
            else
              _buildLineageCard(_lineageReport!)
          else
            const Center(child: Padding(padding: EdgeInsets.all(40), child: Text('Search for an order to view its tracking lineage.', style: TextStyle(color: Colors.grey)))),
        ],
      ),
    );
  }

  Widget _buildRideLineageCard(Map<String, dynamic> report) {
    final rideId = (report['ride_id'] as String?) ?? 'N/A';
    final rideStatus = (report['ride_status'] as String?) ?? 'N/A';
    final vehicleType = (report['vehicle_type'] as String?) ?? 'N/A';
    final fareAmount = (report['fare_amount'] ?? 0).toString();
    final adminCommission = (report['admin_commission'] ?? 0).toString();

    final customerID = (report['customer_id'] as String?) ?? 'N/A';
    final customerName = (report['customer_name'] as String?) ?? 'N/A';

    final riderId = (report['rider_id'] as String?) ?? 'UNASSIGNED';
    final riderName = (report['rider_name'] as String?) ?? '';

    final bids = report['bids'] as List<dynamic>? ?? [];
    final ledgerEntries = report['ledger_entries'] as List<dynamic>? ?? [];

    return Container(
      padding: const EdgeInsets.all(20),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(20),
        border: Border.all(color: Colors.black.withOpacity(0.05)),
        boxShadow: [BoxShadow(color: Colors.black.withOpacity(0.03), blurRadius: 10, offset: const Offset(0, 4))],
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              Text('RIDE: $rideId', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 14)),
              Chip(label: Text(rideStatus, style: const TextStyle(fontSize: 12, fontWeight: FontWeight.bold)), backgroundColor: Colors.blue.withOpacity(0.1)),
            ],
          ),
          const Divider(height: 30),
          _buildAuditRow(Icons.person_outline, 'Customer', '$customerName ($customerID)'),
          _buildAuditRow(Icons.delivery_dining_outlined, 'Rider', riderName.isNotEmpty ? '$riderName ($riderId)' : riderId.toString()),
          _buildAuditRow(Icons.two_wheeler_outlined, 'Vehicle', vehicleType),
          _buildAuditRow(Icons.attach_money, 'Fare Amount', 'PKR $fareAmount'),
          _buildAuditRow(Icons.receipt_long_outlined, 'Admin Commission', 'PKR $adminCommission'),
          _buildAuditRow(Icons.gavel_outlined, 'Bids Received', '${bids.length}'),
          if (bids.isNotEmpty) ...[
            const SizedBox(height: 20),
            const Text('Bid Trail', style: TextStyle(fontWeight: FontWeight.w900, fontSize: 16, color: Colors.black87)),
            const SizedBox(height: 10),
            ...bids.map<Widget>((b) => Padding(
                  padding: const EdgeInsets.only(bottom: 6),
                  child: Text(
                    '• ${b['rider_name'] ?? b['rider_id']} — PKR ${b['bid_amount']} (${b['status']})',
                    style: const TextStyle(fontSize: 13, color: Colors.black87),
                  ),
                ),),
          ],
          if (ledgerEntries.isNotEmpty) ...[
            const SizedBox(height: 20),
            const Text('Fare-Split Ledger Entries', style: TextStyle(fontWeight: FontWeight.w900, fontSize: 16, color: Colors.black87)),
            const SizedBox(height: 10),
            ...ledgerEntries.map<Widget>((e) => Padding(
                  padding: const EdgeInsets.only(bottom: 6),
                  child: Text(
                    '• ${e['account']}: PKR ${e['amount']}',
                    style: const TextStyle(fontSize: 13, color: Colors.black87),
                  ),
                ),),
          ],
        ],
      ),
    );
  }

  Widget _buildLineageCard(Map<String, dynamic> report) {
    final orderId = (report['order_id'] as String?) ?? 'N/A';
    final orderStatus = (report['order_status'] as String?) ?? 'N/A';
    final totalAmount = (report['total_amount'] ?? 0).toString();
    
    final customerID = (report['customer_id'] as String?) ?? 'N/A';
    final customerName = (report['customer_name'] as String?) ?? 'N/A';
    
    final storeID = (report['store_id'] as String?) ?? 'N/A';
    final storeName = (report['store_name'] as String?) ?? 'N/A';
    
    final riderId = (report['rider_id'] as String?) ?? 'UNASSIGNED';
    final deliveryStatus = (report['delivery_status'] as String?) ?? 'PENDING';
    
    final items = report['items'] as List<dynamic>? ?? [];

    return Container(
      padding: const EdgeInsets.all(20),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(20),
        border: Border.all(color: Colors.black.withOpacity(0.05)),
        boxShadow: [BoxShadow(color: Colors.black.withOpacity(0.03), blurRadius: 10, offset: const Offset(0, 4))],
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              Text('ORDER: $orderId', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 14)),
              Chip(label: Text(orderStatus, style: const TextStyle(fontSize: 12, fontWeight: FontWeight.bold)), backgroundColor: Colors.blue.withOpacity(0.1)),
            ],
          ),
          const Divider(height: 30),
          _buildAuditRow(Icons.person_outline, 'Customer', '$customerName ($customerID)'),
          _buildAuditRow(Icons.storefront_outlined, 'Store', '$storeName ($storeID)'),
          _buildAuditRow(Icons.delivery_dining_outlined, 'Rider', riderId.toString()),
          _buildAuditRow(Icons.local_shipping_outlined, 'Delivery Status', deliveryStatus.toString()),
          _buildAuditRow(Icons.attach_money, 'Total Amount', 'PKR $totalAmount'),
          const SizedBox(height: 20),
          const Text('Order Items', style: TextStyle(fontWeight: FontWeight.w900, fontSize: 16, color: Colors.black87)),
          const SizedBox(height: 10),
          if (items.isEmpty)
            const Text('No items found in this order.', style: TextStyle(color: Colors.grey))
          else
            Container(
              constraints: const BoxConstraints(maxHeight: 250), // Makes it scrollable and compact
              decoration: BoxDecoration(
                color: Colors.grey[50],
                borderRadius: BorderRadius.circular(12),
                border: Border.all(color: Colors.grey.shade300),
              ),
              child: ListView.separated(
                shrinkWrap: true,
                padding: const EdgeInsets.all(8),
                itemCount: items.length,
                separatorBuilder: (context, index) => const Divider(),
                itemBuilder: (context, index) {
                  final item = items[index];
                  final pName = item['product_name'] ?? 'Unknown Product';
                  final pId = item['product_id'] ?? 'N/A';
                  final qty = item['quantity'] ?? 0;
                  final unitPrice = item['unit_price'] ?? 0.0;
                  final subtotal = item['subtotal'] ?? 0.0;
                  final batchId = item['batch_tracking'] ?? '';
                  
                  return Padding(
                    padding: const EdgeInsets.symmetric(vertical: 4),
                    child: Row(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Container(
                          padding: const EdgeInsets.all(8),
                          decoration: BoxDecoration(color: Colors.black87, borderRadius: BorderRadius.circular(8)),
                          child: Text('${qty}x', style: const TextStyle(color: Colors.white, fontWeight: FontWeight.bold)),
                        ),
                        const SizedBox(width: 12),
                        Expanded(
                          child: Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              Text(pName.toString(), style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 14)),
                              Text('ID: $pId', style: const TextStyle(color: Colors.grey, fontSize: 12)),
                              if (batchId.toString().isNotEmpty)
                                Text('Batch: $batchId', style: const TextStyle(color: Colors.orange, fontSize: 11)),
                            ],
                          ),
                        ),
                        Column(
                          crossAxisAlignment: CrossAxisAlignment.end,
                          children: [
                            Text('PKR $subtotal', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 14, color: Colors.green)),
                            Text('@ PKR $unitPrice', style: const TextStyle(color: Colors.grey, fontSize: 11)),
                          ],
                        ),
                      ],
                    ),
                  );
                },
              ),
            ),
        ],
      ),
    );
  }

  Widget _buildAuditRow(IconData icon, String label, String value) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Row(
        children: [
          Icon(icon, size: 18, color: Colors.black54),
          const SizedBox(width: 10),
          Text('$label: ', style: const TextStyle(color: Colors.black54, fontSize: 13)),
          Expanded(child: Text(value, style: const TextStyle(fontWeight: FontWeight.w700, fontSize: 13, color: Colors.black87), overflow: TextOverflow.ellipsis)),
        ],
      ),
    );
  }

  // ── Pending Tab ──────────────────────────────────────────────

  Widget _buildPendingTab() {
    return Padding(
      padding: const EdgeInsets.all(16.0),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              const Text('Pending Verifications', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: Colors.black87)),
              IconButton(onPressed: _fetchPendingVerifications, icon: const Icon(Icons.refresh_outlined)),
            ],
          ),
          const SizedBox(height: 8),
          if (_isLoadingPending)
            const Center(child: CircularProgressIndicator(color: Colors.black87))
          else if (_pendingUsers.isEmpty)
            const Center(child: Padding(padding: EdgeInsets.all(40), child: Text('No pending approvals. All riders and vendors are verified.', style: TextStyle(color: Colors.grey))))
          else
            Expanded(
              child: ListView.builder(
                itemCount: _pendingUsers.length,
                itemBuilder: (context, index) {
                  final user = _pendingUsers[index];
                  final trackingId = (user['tracking_id'] as String?) ?? '';
                  final role = (user['role'] as String?) ?? 'unknown';
                  final name = (user['full_name'] as String?) ?? 'N/A';
                  final email = (user['email'] as String?) ?? 'N/A';
                  final businessName = (user['business_name'] as String?) ?? '';
                  final phone = (user['phone'] as String?) ?? 'N/A';

                  return Container(
                    margin: const EdgeInsets.only(bottom: 12),
                    padding: const EdgeInsets.all(16),
                    decoration: BoxDecoration(
                      color: Colors.white,
                      borderRadius: BorderRadius.circular(16),
                      border: Border.all(color: Colors.orange.withOpacity(0.3)),
                    ),
                    child: Row(
                      children: [
                        CircleAvatar(
                          backgroundColor: role == 'rider' ? Colors.blue.shade100 : Colors.green.shade100,
                          child: Icon(role == 'rider' ? Icons.delivery_dining : Icons.storefront, color: Colors.black54),
                        ),
                        const SizedBox(width: 12),
                        Expanded(
                          child: Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              Text(name, style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 14)),
                              Text(email, style: const TextStyle(color: Colors.grey, fontSize: 12)),
                              if (businessName.isNotEmpty)
                                Text(businessName, style: const TextStyle(color: Colors.grey, fontSize: 12)),
                              Text('$role • $phone', style: TextStyle(color: Colors.grey.shade400, fontSize: 11)),
                            ],
                          ),
                        ),
                        ElevatedButton(
                          onPressed: () => _approveUser(trackingId),
                          style: ElevatedButton.styleFrom(backgroundColor: Colors.green, foregroundColor: Colors.white),
                          child: const Text('Approve'),
                        ),
                      ],
                    ),
                  );
                },
              ),
            ),
        ],
      ),
    );
  }

  // ── Verifications Tab ─────────────────────────────────────────

  Widget _buildVerificationsTab() {
    return Padding(
      padding: const EdgeInsets.all(16.0),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              const Text('KYC / KYB Verifications', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: Colors.black87)),
              IconButton(onPressed: _fetchVerifications, icon: const Icon(Icons.refresh_outlined)),
            ],
          ),
          const SizedBox(height: 8),
          if (_isLoadingVerifications)
            const Center(child: CircularProgressIndicator(color: Colors.black87))
          else if (_verifications.isEmpty)
            const Center(child: Padding(padding: EdgeInsets.all(40), child: Text('No verifications pending review.', style: TextStyle(color: Colors.grey))))
          else
            Expanded(
              child: ListView.builder(
                itemCount: _verifications.length,
                itemBuilder: (context, index) {
                  final user = _verifications[index];
                  final trackingId = (user['tracking_id'] as String?) ?? '';
                  final role = (user['role'] as String?) ?? 'unknown';
                  final name = (user['full_name'] as String?) ?? 'N/A';
                  final email = (user['email'] as String?) ?? 'N/A';
                  final businessName = (user['business_name'] as String?) ?? '';
                  final riskScore = (user['risk_score'] as num?)?.toInt() ?? 0;
                  final status = (user['verification_status'] as String?) ?? 'pending';
                  final cnicUrl = user['cnic_url']?.toString() ?? '';
                  final licenseUrl = user['license_url']?.toString() ?? '';

                  Color riskColor;
                  if (riskScore >= 60) {
                    riskColor = Colors.red;
                  } else if (riskScore >= 20) {
                    riskColor = Colors.orange;
                  } else {
                    riskColor = Colors.green;
                  }

                  return Container(
                    margin: const EdgeInsets.only(bottom: 12),
                    padding: const EdgeInsets.all(16),
                    decoration: BoxDecoration(
                      color: Colors.white,
                      borderRadius: BorderRadius.circular(16),
                      border: Border.all(color: Colors.grey.shade300),
                    ),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Row(
                          children: [
                            CircleAvatar(
                              backgroundColor: role == 'rider' ? Colors.blue.shade100 : Colors.green.shade100,
                              child: Icon(role == 'rider' ? Icons.delivery_dining : Icons.storefront, color: Colors.black54),
                            ),
                            const SizedBox(width: 12),
                            Expanded(
                              child: Column(
                                crossAxisAlignment: CrossAxisAlignment.start,
                                children: [
                                  Text(name, style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 14)),
                                  Text(email, style: const TextStyle(color: Colors.grey, fontSize: 12)),
                                  if (businessName.isNotEmpty)
                                    Text(businessName, style: const TextStyle(color: Colors.grey, fontSize: 12)),
                                ],
                              ),
                            ),
                            Chip(
                              label: Text('$riskScore', style: const TextStyle(color: Colors.white, fontSize: 12, fontWeight: FontWeight.bold)),
                              backgroundColor: riskColor,
                            ),
                          ],
                        ),
                        const SizedBox(height: 8),
                        Text('Status: ${status.toUpperCase()}', style: TextStyle(color: Colors.grey.shade600, fontSize: 12)),
                        if (cnicUrl.isNotEmpty || licenseUrl.isNotEmpty) ...[
                          const SizedBox(height: 8),
                          Wrap(
                            spacing: 8,
                            children: [
                              if (cnicUrl.isNotEmpty)
                                ActionChip(
                                  avatar: const Icon(Icons.image, size: 16),
                                  label: const Text('CNIC'),
                                  onPressed: () {},
                                ),
                              if (licenseUrl.isNotEmpty)
                                ActionChip(
                                  avatar: const Icon(Icons.image, size: 16),
                                  label: const Text('License'),
                                  onPressed: () {},
                                ),
                            ],
                          ),
                        ],
                        const SizedBox(height: 12),
                        Row(
                          mainAxisAlignment: MainAxisAlignment.end,
                          children: [
                            ElevatedButton(
                              onPressed: () => _approveVerification(trackingId),
                              style: ElevatedButton.styleFrom(backgroundColor: Colors.green, foregroundColor: Colors.white),
                              child: const Text('Approve'),
                            ),
                            const SizedBox(width: 8),
                            ElevatedButton(
                              onPressed: () => _rejectVerification(trackingId),
                              style: ElevatedButton.styleFrom(backgroundColor: Colors.redAccent, foregroundColor: Colors.white),
                              child: const Text('Reject'),
                            ),
                          ],
                        ),
                      ],
                    ),
                  );
                },
              ),
            ),
        ],
      ),
    );
  }

  // ── Users Tab ─────────────────────────────────────────────────

  Widget _buildUsersTab() {
    return Padding(
      padding: const EdgeInsets.all(16.0),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              const Text('All Users', style: TextStyle(fontSize: 18, fontWeight: FontWeight.w900, color: Colors.black87)),
              IconButton(onPressed: _fetchAllUsers, icon: const Icon(Icons.refresh_outlined)),
            ],
          ),
          const SizedBox(height: 8),
          if (_isLoadingUsers)
            const Center(child: CircularProgressIndicator(color: Colors.black87))
          else if (_allUsers.isEmpty)
            const Center(child: Padding(padding: EdgeInsets.all(40), child: Text('No users found.', style: TextStyle(color: Colors.grey))))
          else
            Expanded(
              child: ListView.builder(
                itemCount: _allUsers.length,
                itemBuilder: (context, index) {
                  final user = _allUsers[index];
                  final name = (user['full_name'] as String?) ?? 'N/A';
                  final email = (user['email'] as String?) ?? 'N/A';
                  final role = (user['role'] as String?) ?? 'unknown';

                  return Container(
                    margin: const EdgeInsets.only(bottom: 8),
                    padding: const EdgeInsets.all(12),
                    decoration: BoxDecoration(
                      color: Colors.white,
                      borderRadius: BorderRadius.circular(12),
                      border: Border.all(color: Colors.grey.shade200),
                    ),
                    child: Row(
                      children: [
                        Icon(role == 'rider' ? Icons.delivery_dining : (role == 'vendor' ? Icons.storefront : Icons.person), color: Colors.grey, size: 24),
                        const SizedBox(width: 12),
                        Expanded(
                          child: Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              Text(name, style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 13)),
                              Text(email, style: const TextStyle(color: Colors.grey, fontSize: 11)),
                            ],
                          ),
                        ),
                        Chip(label: Text(role.toUpperCase(), style: const TextStyle(fontSize: 10)), backgroundColor: Colors.grey.shade100),
                      ],
                    ),
                  );
                },
              ),
            ),
        ],
      ),
    );
  }
}