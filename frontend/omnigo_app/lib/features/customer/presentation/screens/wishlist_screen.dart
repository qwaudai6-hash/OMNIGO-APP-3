import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../data/models/product.dart';
import 'product_details_screen.dart';

/// WishlistScreen displays all products the customer has favorited.
/// Fetches the favorite product IDs from the backend, then fetches
/// each product's details to display in a grid.
class WishlistScreen extends StatefulWidget {

  const WishlistScreen({
    super.key,
    required this.customerTrackingId,
    this.onNavigateToCatalog,
  });
  final String customerTrackingId;
  final void Function(int)? onNavigateToCatalog;

  @override
  WishlistScreenState createState() => WishlistScreenState();
}

class WishlistScreenState extends State<WishlistScreen> {
  List<dynamic> _favoriteProducts = [];
  bool _isLoading = true;
  String? _errorMessage;

  @override
  void initState() {
    super.initState();
    _fetchWishlist();
  }

  Future<void> _fetchWishlist() async {
    setState(() {
      _isLoading = true;
      _errorMessage = null;
    });

    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      // Step 1: Get favorited product IDs
      final favResponse = await http.get(
        Uri.parse(ApiEndpoints.wishlistList()),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
      ).timeout(const Duration(seconds: 8));

      if (favResponse.statusCode != 200) {
        throw Exception('Failed to load wishlist (${favResponse.statusCode})');
      }

      final favData = jsonDecode(favResponse.body) as Map<String, dynamic>;
      final List<dynamic> productIds = (favData['product_tracking_ids'] as List<dynamic>?) ?? [];

      if (productIds.isEmpty) {
        if (mounted) setState(() { _favoriteProducts = []; _isLoading = false; });
        return;
      }

      // Step 2: Fetch product details for each favorited ID.
      // The product-service doesn't have a batch-by-IDs endpoint yet, so
      // we fetch the full catalog and filter. At scale, a dedicated
      // GET /products?ids=PROD-1,PROD-2 endpoint would be more efficient.
      final prodResponse = await http.get(
        Uri.parse(ApiEndpoints.productsList(limit: 100, offset: 0)),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
      ).timeout(const Duration(seconds: 10));

      if (prodResponse.statusCode == 200) {
        final allProducts = jsonDecode(prodResponse.body) as List<dynamic>;
        final favSet = productIds.map((e) => e.toString()).toSet();
        final favorited = allProducts.where((p) {
          final pid = p['product_tracking_id']?.toString() ?? '';
          return favSet.contains(pid);
        }).toList();

        if (mounted) {
          setState(() {
            _favoriteProducts = favorited;
            _isLoading = false;
          });
        }
      } else {
        throw Exception('Failed to load product details');
      }
    } catch (e) {
      if (mounted) {
        setState(() {
          _errorMessage = e.toString();
          _isLoading = false;
        });
      }
    }
  }

  Future<void> _removeFromWishlist(String productId) async {
    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      await http.delete(
        Uri.parse(ApiEndpoints.wishlistRemove(productId)),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
      ).timeout(const Duration(seconds: 5));

      // Remove from local list
      setState(() {
        _favoriteProducts.removeWhere((p) => p['product_tracking_id'] == productId);
      });
    } catch (e) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Failed to remove: $e'), backgroundColor: Colors.redAccent),
      );
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: AppTheme.bgColor,
      body: SafeArea(
        child: Padding(
          padding: const EdgeInsets.all(24.0),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const Text('My Wishlist',
                  style: TextStyle(fontSize: 24, fontWeight: FontWeight.bold, color: AppTheme.blackAccent),),
              const SizedBox(height: 4),
              Text('${_favoriteProducts.length} favorited items',
                  style: const TextStyle(color: Colors.grey, fontSize: 14),),
              const SizedBox(height: 20),

              Expanded(
                child: _isLoading
                    ? const Center(child: CircularProgressIndicator(color: AppTheme.limeAccent))
                    : _errorMessage != null
                        ? Center(
                            child: Column(
                              mainAxisAlignment: MainAxisAlignment.center,
                              children: [
                                const Icon(Icons.error_outline_rounded, color: Colors.redAccent, size: 48),
                                const SizedBox(height: 16),
                                Text(_errorMessage!, textAlign: TextAlign.center, style: const TextStyle(color: Colors.grey)),
                                const SizedBox(height: 24),
                                ElevatedButton(
                                  onPressed: _fetchWishlist,
                                  style: ElevatedButton.styleFrom(backgroundColor: AppTheme.blackAccent),
                                  child: const Text('Retry'),
                                ),
                              ],
                            ),
                          )
                        : _favoriteProducts.isEmpty
                            ? Center(
                                child: Column(
                                  mainAxisAlignment: MainAxisAlignment.center,
                                  children: [
                                    const Icon(Icons.favorite_border_rounded, size: 64, color: Colors.grey),
                                    const SizedBox(height: 16),
                                    const Text('No favorites yet.',
                                        style: TextStyle(color: Colors.grey, fontSize: 16),),
                                    const SizedBox(height: 8),
                                    const Text('Tap the heart icon on products to save them here.',
                                        style: TextStyle(color: Colors.grey, fontSize: 13),),
                                    const SizedBox(height: 24),
                                    ElevatedButton(
                                      onPressed: () {
                                        if (widget.onNavigateToCatalog != null) {
                                          widget.onNavigateToCatalog!(1);
                                        }
                                      },
                                      style: ElevatedButton.styleFrom(backgroundColor: AppTheme.blackAccent),
                                      child: const Text('Browse Catalog'),
                                    ),
                                  ],
                                ),
                              )
                            : RefreshIndicator(
                                onRefresh: _fetchWishlist,
                                color: AppTheme.limeAccent,
                                child: GridView.builder(
                                  itemCount: _favoriteProducts.length,
                                  gridDelegate: const SliverGridDelegateWithFixedCrossAxisCount(
                                    crossAxisCount: 2,
                                    crossAxisSpacing: 14,
                                    mainAxisSpacing: 14,
                                    childAspectRatio: 0.75,
                                  ),
                                  itemBuilder: (context, index) {
                                    final p = _favoriteProducts[index];
                                    final String name = (p['name'] as String?) ?? 'Unknown';
                                    final double price = ((p['base_price'] as num?) ?? 0).toDouble();
                                    final String prodId = (p['product_tracking_id'] as String?) ?? '';

                                    return GestureDetector(
                                      onTap: () {
                                        Navigator.push(
                                          context,
                                          MaterialPageRoute<void>(
                                            builder: (_) => ProductDetailsScreen(
                                              product: Product.fromJson(p as Map<String, dynamic>),
                                              userTrackingId: widget.customerTrackingId,
                                            ),
                                          ),
                                        );
                                      },
                                      onLongPress: () => _removeFromWishlist(prodId),
                                      child: Container(
                                        decoration: BoxDecoration(
                                          color: Colors.white,
                                          borderRadius: BorderRadius.circular(24),
                                          boxShadow: [
                                            BoxShadow(color: Colors.black.withOpacity(0.02), blurRadius: 10, offset: const Offset(0, 5)),
                                          ],
                                        ),
                                        child: Column(
                                          crossAxisAlignment: CrossAxisAlignment.start,
                                          children: [
                                            Expanded(
                                              child: Stack(
                                                children: [
                                                  ClipRRect(
                                                    borderRadius: const BorderRadius.vertical(top: Radius.circular(24)),
                                                    child: Container(
                                                      width: double.infinity,
                                                      color: AppTheme.softPink,
                                                      child: (p['image_url'] != null && p['image_url'].toString().isNotEmpty)
                                                          ? Image.network(p['image_url'] as String, fit: BoxFit.cover,
                                                              errorBuilder: (_, __, ___) => const Center(child: Icon(Icons.shopping_bag_outlined, size: 40, color: AppTheme.blackAccent)),)
                                                          : const Center(child: Icon(Icons.shopping_bag_outlined, size: 40, color: AppTheme.blackAccent)),
                                                    ),
                                                  ),
                                                  Positioned(
                                                    top: 8,
                                                    right: 8,
                                                    child: GestureDetector(
                                                      onTap: () => _removeFromWishlist(prodId),
                                                      child: Container(
                                                        padding: const EdgeInsets.all(6),
                                                        decoration: BoxDecoration(
                                                          color: Colors.white.withOpacity(0.9),
                                                          shape: BoxShape.circle,
                                                        ),
                                                        child: const Icon(Icons.favorite, color: Colors.redAccent, size: 18),
                                                      ),
                                                    ),
                                                  ),
                                                ],
                                              ),
                                            ),
                                            Padding(
                                              padding: const EdgeInsets.all(12.0),
                                              child: Column(
                                                crossAxisAlignment: CrossAxisAlignment.start,
                                                children: [
                                                  Text(name,
                                                      style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 13, color: AppTheme.blackAccent),
                                                      maxLines: 1, overflow: TextOverflow.ellipsis,),
                                                  const SizedBox(height: 4),
                                                  Text('PKR ${price.toStringAsFixed(2)}',
                                                      style: const TextStyle(fontWeight: FontWeight.w800, color: AppTheme.blackAccent),),
                                                ],
                                              ),
                                            ),
                                          ],
                                        ),
                                      ),
                                    );
                                  },
                                ),
                              ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}