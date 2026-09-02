import 'package:flutter/material.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';

class AdminSavedCardsScreen extends StatefulWidget {
  const AdminSavedCardsScreen({super.key});

  @override
  State<AdminSavedCardsScreen> createState() => _AdminSavedCardsScreenState();
}

class _AdminSavedCardsScreenState extends State<AdminSavedCardsScreen> {
  final _customerController = TextEditingController();
  List<dynamic> _cards = [];
  bool _isLoading = false;
  bool _hasSearched = false;

  @override
  void dispose() {
    _customerController.dispose();
    super.dispose();
  }

  Future<void> _fetchCards() async {
    final customerId = _customerController.text.trim();
    if (customerId.isEmpty) return;

    setState(() => _isLoading = true);
    try {
      final data = await sl<ApiClient>().get(ApiEndpoints.adminSavedCards(customerId));
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _cards = data['cards'] as List<dynamic>? ?? [];
          _isLoading = false;
          _hasSearched = true;
        });
      }
    } catch (e) {
      if (mounted) setState(() { _isLoading = false; _hasSearched = true; });
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.grey[100],
      appBar: AppBar(
        title: const Text('Saved Cards Audit', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
      ),
      body: Column(
        children: [
          _buildSearchBar(),
          Expanded(
            child: _isLoading
                ? const Center(child: CircularProgressIndicator(color: Colors.black87))
                : !_hasSearched
                    ? const Center(child: Text('Enter Customer ID to view saved cards'))
                    : _cards.isEmpty
                        ? const Center(child: Text('No saved cards found'))
                        : _buildCardsList(),
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
              controller: _customerController,
              decoration: InputDecoration(
                hintText: 'Enter Customer ID (e.g., CUST-xxx)',
                hintStyle: TextStyle(color: Colors.grey.shade400),
                prefixIcon: const Icon(Icons.person, color: Colors.grey),
                filled: true,
                fillColor: Colors.grey[100],
                border: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(12),
                  borderSide: BorderSide.none,
                ),
              ),
              onSubmitted: (_) => _fetchCards(),
            ),
          ),
          const SizedBox(width: 8),
          ElevatedButton(
            onPressed: _fetchCards,
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

  Widget _buildCardsList() {
    return ListView.builder(
      padding: const EdgeInsets.all(12),
      itemCount: _cards.length,
      itemBuilder: (context, index) => _buildCardTile(_cards[index] as Map<String, dynamic>),
    );
  }

  Widget _buildCardTile(Map<String, dynamic> card) {
    final cardId = card['card_id']?.toString() ?? '';
    final brand = card['card_brand']?.toString() ?? '';
    final last4 = card['last_four']?.toString() ?? '';
    final expMonth = card['expiry_month']?.toString() ?? '';
    final expYear = card['expiry_year']?.toString() ?? '';
    final isDefault = card['is_default'] as bool? ?? false;
    final gateway = card['gateway']?.toString() ?? '';

    final brandColor = brand.toLowerCase() == 'visa' ? Colors.blue
        : brand.toLowerCase() == 'mastercard' ? Colors.orange
        : Colors.grey;

    final brandIcon = brand.toLowerCase() == 'visa' ? Icons.credit_card
        : brand.toLowerCase() == 'mastercard' ? Icons.credit_card
        : Icons.payment;

    return Card(
      margin: const EdgeInsets.only(bottom: 8),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Row(
          children: [
            Container(
              padding: const EdgeInsets.all(12),
              decoration: BoxDecoration(
                color: brandColor.withOpacity(0.1),
                borderRadius: BorderRadius.circular(8),
              ),
              child: Icon(brandIcon, color: brandColor, size: 28),
            ),
            const SizedBox(width: 16),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    children: [
                      Text(brand.toUpperCase(), style: TextStyle(fontWeight: FontWeight.bold, color: brandColor)),
                      if (isDefault) ...[
                        const SizedBox(width: 8),
                        Container(
                          padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
                          decoration: BoxDecoration(
                            color: Colors.green.withOpacity(0.1),
                            borderRadius: BorderRadius.circular(4),
                          ),
                          child: const Text('DEFAULT', style: TextStyle(fontSize: 9, fontWeight: FontWeight.bold, color: Colors.green)),
                        ),
                      ],
                    ],
                  ),
                  const SizedBox(height: 4),
                  Text('•••• •••• •••• $last4', style: const TextStyle(fontSize: 16, fontWeight: FontWeight.w500, letterSpacing: 2)),
                  const SizedBox(height: 4),
                  Text('Expires $expMonth/$expYear', style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
                ],
              ),
            ),
            Column(
              crossAxisAlignment: CrossAxisAlignment.end,
              children: [
                Text('Gateway', style: TextStyle(fontSize: 10, color: Colors.grey.shade600)),
                Text(gateway, style: TextStyle(fontSize: 10, color: Colors.grey.shade500)),
                const SizedBox(height: 4),
                Text('ID', style: TextStyle(fontSize: 10, color: Colors.grey.shade600)),
                Text(cardId.length > 8 ? cardId.substring(0, 8) : cardId, style: TextStyle(fontSize: 10, color: Colors.grey.shade500)),
              ],
            ),
          ],
        ),
      ),
    );
  }
}
