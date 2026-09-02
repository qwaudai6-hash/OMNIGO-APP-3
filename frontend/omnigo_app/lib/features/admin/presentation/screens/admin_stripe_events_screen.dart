import 'package:flutter/material.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/di/service_locator.dart';

class AdminStripeEventsScreen extends StatefulWidget {
  const AdminStripeEventsScreen({super.key});

  @override
  State<AdminStripeEventsScreen> createState() => _AdminStripeEventsScreenState();
}

class _AdminStripeEventsScreenState extends State<AdminStripeEventsScreen> {
  List<dynamic> _events = [];
  bool _isLoading = true;
  String _selectedType = '';

  @override
  void initState() {
    super.initState();
    _fetchEvents();
  }

  Future<void> _fetchEvents() async {
    setState(() => _isLoading = true);
    try {
      final data = await sl<ApiClient>().get(
        ApiEndpoints.adminStripeEvents(type: _selectedType.isEmpty ? null : _selectedType),
      );
      if (data is Map<String, dynamic> && mounted) {
        setState(() {
          _events = data['events'] as List<dynamic>? ?? [];
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
        title: const Text('Stripe Events Audit', style: TextStyle(fontWeight: FontWeight.bold, color: Colors.white)),
        backgroundColor: Colors.black87,
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh, color: Colors.white),
            onPressed: _fetchEvents,
          ),
        ],
      ),
      body: Column(
        children: [
          _buildFilterBar(),
          Expanded(
            child: _isLoading
                ? const Center(child: CircularProgressIndicator(color: Colors.black87))
                : _events.isEmpty
                    ? const Center(child: Text('No Stripe events found'))
                    : _buildEventsList(),
          ),
        ],
      ),
    );
  }

  Widget _buildFilterBar() {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
      color: Colors.white,
      child: Row(
        children: [
          Text('Filter: ', style: TextStyle(color: Colors.grey.shade700, fontSize: 13)),
          Expanded(
            child: TextField(
              decoration: InputDecoration(
                hintText: 'Event type (e.g., payment_intent.succeeded)',
                hintStyle: TextStyle(color: Colors.grey.shade400),
                prefixIcon: const Icon(Icons.search, color: Colors.grey),
                filled: true,
                fillColor: Colors.grey[100],
                border: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(8),
                  borderSide: BorderSide.none,
                ),
                contentPadding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
              ),
              onSubmitted: (_) => _fetchEvents(),
              onChanged: (v) => _selectedType = v,
            ),
          ),
        ],
      ),
    );
  }

  Widget _buildEventsList() {
    return ListView.builder(
      padding: const EdgeInsets.all(12),
      itemCount: _events.length,
      itemBuilder: (context, index) => _buildEventCard(_events[index] as Map<String, dynamic>),
    );
  }

  Widget _buildEventCard(Map<String, dynamic> event) {
    final stripeEventId = event['stripe_event_id']?.toString() ?? '';
    final eventType = event['event_type']?.toString() ?? '';
    final orderId = event['order_id']?.toString() ?? '';
    final isUnprocessed = event['is_unprocessed'] as bool? ?? false;
    final createdAt = event['created_at']?.toString() ?? '';

    final typeColor = isUnprocessed ? Colors.red : Colors.green;
    final typeIcon = isUnprocessed ? Icons.error : Icons.check_circle;

    return Card(
      margin: const EdgeInsets.only(bottom: 8),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(typeIcon, color: typeColor, size: 20),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(eventType, style: TextStyle(fontWeight: FontWeight.bold, color: typeColor)),
                ),
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
                  decoration: BoxDecoration(
                    color: typeColor.withOpacity(0.1),
                    borderRadius: BorderRadius.circular(8),
                  ),
                  child: Text(
                    isUnprocessed ? 'UNPROCESSED' : 'PROCESSED',
                    style: TextStyle(fontSize: 10, fontWeight: FontWeight.bold, color: typeColor),
                  ),
                ),
              ],
            ),
            const SizedBox(height: 8),
            _detailRow('Event ID', stripeEventId),
            if (orderId.isNotEmpty) _detailRow('Order', orderId),
            _detailRow('Created', createdAt),
          ],
        ),
      ),
    );
  }

  Widget _detailRow(String label, String value) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceBetween,
        children: [
          Text(label, style: TextStyle(color: Colors.grey.shade600, fontSize: 12)),
          Flexible(child: Text(value, style: const TextStyle(fontSize: 12, fontWeight: FontWeight.w500), textAlign: TextAlign.end)),
        ],
      ),
    );
  }
}
