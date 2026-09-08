import 'package:flutter/material.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../shared/presentation/screens/chat_room_screen.dart';

class PreOrderChatScreen extends StatefulWidget {
  const PreOrderChatScreen({super.key});

  @override
  State<PreOrderChatScreen> createState() => _PreOrderChatScreenState();
}

class _PreOrderChatScreenState extends State<PreOrderChatScreen> {
  final _searchController = TextEditingController();
  List<Map<String, dynamic>> _stores = [];
  List<Map<String, dynamic>> _filteredStores = [];
  bool _isLoading = false;
  String? _error;

  @override
  void initState() {
    super.initState();
    _fetchStores();
  }

  @override
  void dispose() {
    _searchController.dispose();
    super.dispose();
  }

  Future<void> _fetchStores() async {
    setState(() {
      _isLoading = true;
      _error = null;
    });

    try {
      final data = await ApiClient().get('/stores') as Map<String, dynamic>;
      final List<dynamic> storesData = data['stores'] as List<dynamic>? ?? data['data'] as List<dynamic>? ?? [];
      setState(() {
        _stores = storesData.cast<Map<String, dynamic>>();
        _filteredStores = _stores;
        _isLoading = false;
      });
    } catch (e) {
      setState(() {
        _error = e.toString();
        _isLoading = false;
        _stores = [];
        _filteredStores = [];
      });
    }
  }

  void _filterStores(String query) {
    setState(() {
      if (query.isEmpty) {
        _filteredStores = _stores;
      } else {
        _filteredStores = _stores.where((store) {
          final name = (store['store_name'] ?? store['name'] ?? '').toString().toLowerCase();
          final address = (store['address'] ?? '').toString().toLowerCase();
          return name.contains(query.toLowerCase()) || address.contains(query.toLowerCase());
        }).toList();
      }
    });
  }

  String _getStoreName(Map<String, dynamic> store) {
    return (store['store_name'] ?? store['name'] ?? 'Store').toString();
  }

  String _getStoreAddress(Map<String, dynamic> store) {
    return (store['address'] ?? store['store_address'] ?? '').toString();
  }

  String _getVendorId(Map<String, dynamic> store) {
    return (store['vendor_tracking_id'] ?? store['vendor_id'] ?? '').toString();
  }

  String _getStoreId(Map<String, dynamic> store) {
    return (store['store_tracking_id'] ?? store['id'] ?? '').toString();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: AppTheme.bgColor,
      appBar: AppBar(
        title: const Text(
          'Chat with Vendor',
          style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold),
        ),
        backgroundColor: AppTheme.blackAccent,
        foregroundColor: Colors.white,
        leading: IconButton(
          icon: const Icon(Icons.arrow_back_rounded, color: Colors.white),
          onPressed: () => Navigator.pop(context),
        ),
      ),
      body: Column(
        children: [
          Container(
            padding: const EdgeInsets.all(16),
            color: Colors.white,
            child: TextField(
              controller: _searchController,
              decoration: InputDecoration(
                hintText: 'Search stores...',
                prefixIcon: const Icon(Icons.search),
                border: OutlineInputBorder(borderRadius: BorderRadius.circular(12)),
                filled: true,
                fillColor: Colors.grey.shade100,
              ),
              onChanged: _filterStores,
            ),
          ),
          Expanded(child: _buildBody()),
        ],
      ),
    );
  }

  Widget _buildBody() {
    if (_isLoading && _stores.isEmpty) {
      return const Center(child: CircularProgressIndicator(color: AppTheme.limeAccent));
    }

    if (_error != null && _stores.isEmpty) {
      return Center(
        child: Column(
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            Icon(Icons.error_outline, size: 48, color: Colors.red.shade300),
            const SizedBox(height: 12),
            Text(
              'Failed to load stores',
              style: TextStyle(color: Colors.grey.shade600, fontSize: 16),
            ),
            const SizedBox(height: 8),
            TextButton(
              onPressed: _fetchStores,
              child: const Text('Retry'),
            ),
          ],
        ),
      );
    }

    if (_filteredStores.isEmpty) {
      return Center(
        child: Column(
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            Icon(Icons.store_outlined, size: 64, color: Colors.grey.shade400),
            const SizedBox(height: 16),
            Text(
              _searchController.text.isEmpty ? 'No stores available' : 'No stores found',
              style: TextStyle(fontSize: 18, color: Colors.grey.shade600),
            ),
          ],
        ),
      );
    }

    return RefreshIndicator(
      color: AppTheme.limeAccent,
      onRefresh: _fetchStores,
      child: ListView.builder(
        padding: const EdgeInsets.all(12),
        itemCount: _filteredStores.length,
        itemBuilder: (context, index) {
          final store = _filteredStores[index];
          return _buildStoreCard(store);
        },
      ),
    );
  }

  Widget _buildStoreCard(Map<String, dynamic> store) {
    final vendorId = _getVendorId(store);
    final storeId = _getStoreId(store);
    final storeName = _getStoreName(store);
    final storeAddress = _getStoreAddress(store);

    return Card(
      margin: const EdgeInsets.only(bottom: 10),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
      elevation: 1,
      child: InkWell(
        borderRadius: BorderRadius.circular(12),
        onTap: () async {
          if (vendorId.isEmpty) {
            ScaffoldMessenger.of(context).showSnackBar(
              const SnackBar(content: Text('Store info not available')),
            );
            return;
          }
          await Navigator.push(
            context,
            MaterialPageRoute<void>(
              builder: (_) => ChatRoomScreen(
                orderId: 'PRE_ORDER_$storeId',
                otherUserId: vendorId,
                otherUserName: storeName,
                otherUserRole: 'vendor',
              ),
            ),
          );
        },
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Row(
            children: [
              Container(
                width: 48,
                height: 48,
                decoration: BoxDecoration(
                  color: AppTheme.limeAccent.withOpacity(0.15),
                  borderRadius: BorderRadius.circular(10),
                ),
                child: const Icon(Icons.store, color: AppTheme.blackAccent, size: 24),
              ),
              const SizedBox(width: 12),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      storeName,
                      style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 15),
                      overflow: TextOverflow.ellipsis,
                      maxLines: 1,
                    ),
                    if (storeAddress.isNotEmpty) ...[
                      const SizedBox(height: 4),
                      Text(
                        storeAddress,
                        style: TextStyle(fontSize: 12, color: Colors.grey.shade600),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                      ),
                    ],
                  ],
                ),
              ),
              Icon(Icons.chat_bubble_outline, size: 20, color: Colors.grey.shade400),
            ],
          ),
        ),
      ),
    );
  }
}
