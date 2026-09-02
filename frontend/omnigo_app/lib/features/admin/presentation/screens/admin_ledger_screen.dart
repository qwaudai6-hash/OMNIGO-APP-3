import 'package:flutter/material.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';

class AdminLedgerScreen extends StatefulWidget {
  const AdminLedgerScreen({super.key});

  @override
  State<AdminLedgerScreen> createState() => _AdminLedgerScreenState();
}

class _AdminLedgerScreenState extends State<AdminLedgerScreen> {
  final _accountController = TextEditingController();
  double _balance = 0;
  List<dynamic> _entries = [];
  bool _isLoading = false;
  bool _hasSearched = false;
  String _selectedAccount = '';

  @override
  void dispose() {
    _accountController.dispose();
    super.dispose();
  }

  Future<void> _fetchBalance() async {
    final account = _accountController.text.trim();
    if (account.isEmpty) return;

    setState(() { _isLoading = true; _selectedAccount = account; });
    try {
      final data = await sl<ApiClient>().get(ApiEndpoints.adminLedgerBalance(account));
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _balance = (data['balance'] as num?)?.toDouble() ?? 0;
          _hasSearched = true;
        });
        await _fetchEntries();
      }
    } catch (e) {
      if (mounted) setState(() { _isLoading = false; _hasSearched = true; });
    }
  }

  Future<void> _fetchEntries() async {
    try {
      final data = await sl<ApiClient>().get(
        ApiEndpoints.adminLedgerEntries('account', _selectedAccount),
      );
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _entries = data['entries'] as List<dynamic>? ?? [];
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
        title: const Text('Ledger Balance & Entries', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
      ),
      body: Column(
        children: [
          _buildSearchBar(),
          if (_hasSearched) _buildBalanceCard(),
          Expanded(
            child: _isLoading
                ? const Center(child: CircularProgressIndicator(color: Colors.black87))
                : !_hasSearched
                    ? const Center(child: Text('Enter account name to view balance'))
                    : _entries.isEmpty
                        ? const Center(child: Text('No ledger entries found'))
                        : _buildEntriesList(),
          ),
        ],
      ),
    );
  }

  Widget _buildSearchBar() {
    return Container(
      padding: const EdgeInsets.all(12),
      color: Colors.white,
      child: Row(
        children: [
          Expanded(
            child: TextField(
              controller: _accountController,
              decoration: InputDecoration(
                hintText: 'Account (e.g., escrow, wallet, revenue)',
                hintStyle: TextStyle(color: Colors.grey.shade400),
                prefixIcon: const Icon(Icons.account_balance, color: Colors.grey),
                filled: true,
                fillColor: Colors.grey[100],
                border: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(12),
                  borderSide: BorderSide.none,
                ),
              ),
              onSubmitted: (_) => _fetchBalance(),
            ),
          ),
          const SizedBox(width: 8),
          ElevatedButton(
            onPressed: _fetchBalance,
            style: ElevatedButton.styleFrom(
              backgroundColor: Colors.black87,
              foregroundColor: Colors.white,
              shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
              padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 14),
            ),
            child: const Text('Search'),
          ),
        ],
      ),
    );
  }

  Widget _buildBalanceCard() {
    final isPositive = _balance >= 0;
    return Card(
      margin: const EdgeInsets.all(12),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      color: isPositive ? Colors.green.shade50 : Colors.red.shade50,
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Row(
          mainAxisAlignment: MainAxisAlignment.spaceBetween,
          children: [
            Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text('Account', style: TextStyle(fontSize: 12, color: Colors.grey.shade600)),
                Text(_selectedAccount, style: const TextStyle(fontWeight: FontWeight.bold)),
              ],
            ),
            Column(
              crossAxisAlignment: CrossAxisAlignment.end,
              children: [
                Text('Balance', style: TextStyle(fontSize: 12, color: Colors.grey.shade600)),
                Text(
                  'PKR ${_balance.toStringAsFixed(2)}',
                  style: TextStyle(
                    fontSize: 20,
                    fontWeight: FontWeight.bold,
                    color: isPositive ? Colors.green : Colors.red,
                  ),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildEntriesList() {
    return ListView.builder(
      padding: const EdgeInsets.all(12),
      itemCount: _entries.length,
      itemBuilder: (context, index) => _buildEntryCard(_entries[index] as Map<String, dynamic>),
    );
  }

  Widget _buildEntryCard(Map<String, dynamic> entry) {
    final transactionId = entry['transaction_id']?.toString() ?? '';
    final account = entry['account']?.toString() ?? '';
    final amount = (entry['amount'] as num?)?.toDouble() ?? 0;
    final description = entry['description']?.toString() ?? '';
    final createdAt = entry['created_at']?.toString() ?? '';

    final isCredit = amount >= 0;

    return Card(
      margin: const EdgeInsets.only(bottom: 8),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Row(
          children: [
            Container(
              padding: const EdgeInsets.all(8),
              decoration: BoxDecoration(
                color: isCredit ? Colors.green.withOpacity(0.1) : Colors.red.withOpacity(0.1),
                borderRadius: BorderRadius.circular(8),
              ),
              child: Icon(
                isCredit ? Icons.arrow_downward : Icons.arrow_upward,
                color: isCredit ? Colors.green : Colors.red,
                size: 20,
              ),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(description.isNotEmpty ? description : account, style: const TextStyle(fontWeight: FontWeight.bold)),
                  const SizedBox(height: 2),
                  Text(transactionId, style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
                  if (createdAt.isNotEmpty)
                    Text(createdAt.split('T').first, style: TextStyle(fontSize: 10, color: Colors.grey.shade500)),
                ],
              ),
            ),
            Text(
              '${isCredit ? '+' : ''}PKR ${amount.toStringAsFixed(2)}',
              style: TextStyle(
                fontWeight: FontWeight.bold,
                color: isCredit ? Colors.green : Colors.red,
              ),
            ),
          ],
        ),
      ),
    );
  }
}
