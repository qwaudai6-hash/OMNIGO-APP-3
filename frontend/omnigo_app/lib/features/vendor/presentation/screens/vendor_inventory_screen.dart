import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:maplibre_gl/maplibre_gl.dart';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/services/session_registry.dart';
import '../../../shared/presentation/widgets/map_libre_map_widget.dart';
import 'vendor_add_product_screen.dart';

// 1. Model class mapping the Go backend Product table fields
class ProductModel {

  ProductModel({
    required this.id,
    required this.productTrackingId,
    required this.vendorTrackingId,
    required this.storeTrackingId,
    required this.sku,
    required this.name,
    required this.description,
    required this.basePrice,
    required this.stock,
    required this.isFeatured,
    required this.imageUrl,
    required this.category,
    this.isActive = true,
  });

  factory ProductModel.fromJson(Map<String, dynamic> json) {
    return ProductModel(
      id: (json['id'] as num?)?.toInt() ?? 0,
      productTrackingId: (json['product_tracking_id'] as String?) ?? '',
      vendorTrackingId: (json['vendor_tracking_id'] as String?) ?? '',
      storeTrackingId: (json['store_tracking_id'] as String?) ?? '',
      sku: (json['sku'] as String?) ?? '',
      name: (json['name'] as String?) ?? '',
      description: (json['description'] as String?) ?? '',
      basePrice: (json['base_price'] as num?)?.toDouble() ?? 0.0,
      stock: (json['stock'] as num?)?.toInt() ?? 0,
      isFeatured: (json['is_featured'] as bool?) ?? false,
      imageUrl: (json['image_url'] as String?) ?? '',
      category: (json['category'] as String?) ?? '',
      isActive: (json['is_active'] as bool?) ?? true,
    );
  }
  final int id;
  final String productTrackingId;
  final String vendorTrackingId;
  final String storeTrackingId;
  final String sku;
  final String name;
  final String description;
  final double basePrice;
  final int stock;
  final bool isFeatured;
  final String imageUrl;
  final String category;
  final bool isActive;

  ProductModel copyWith({
    int? id,
    String? productTrackingId,
    String? vendorTrackingId,
    String? storeTrackingId,
    String? sku,
    String? name,
    String? description,
    double? basePrice,
    int? stock,
    bool? isFeatured,
    String? imageUrl,
    String? category,
    bool? isActive,
  }) {
    return ProductModel(
      id: id ?? this.id,
      productTrackingId: productTrackingId ?? this.productTrackingId,
      vendorTrackingId: vendorTrackingId ?? this.vendorTrackingId,
      storeTrackingId: storeTrackingId ?? this.storeTrackingId,
      sku: sku ?? this.sku,
      name: name ?? this.name,
      description: description ?? this.description,
      basePrice: basePrice ?? this.basePrice,
      stock: stock ?? this.stock,
      isFeatured: isFeatured ?? this.isFeatured,
      imageUrl: imageUrl ?? this.imageUrl,
      category: category ?? this.category,
      isActive: isActive ?? this.isActive,
    );
  }

  /// Serializes the model back to the JSON shape expected by the
  /// Go product-service AddProduct / UpdateProduct endpoints.
  Map<String, dynamic> toJson() {
    return {
      'vendor_tracking_id': vendorTrackingId,
      'store_tracking_id': storeTrackingId,
      'sku': sku,
      'name': name,
      'description': description,
      'base_price': basePrice,
      'stock': stock,
      'is_featured': isFeatured,
      'is_active': isActive,
      'image_url': imageUrl,
      'category': category,
    };
  }
}

// 2. State Controller class using Map<String, ValueNotifier<ProductModel>> registry pattern
class VendorInventoryController {
  final Map<String, ValueNotifier<ProductModel>> tileNotifiers = {};
  final ValueNotifier<bool> isLoading = ValueNotifier<bool>(false);
  final ValueNotifier<String?> errorMessage = ValueNotifier<String?>(null);

  void populate(List<ProductModel> products) {
    tileNotifiers.clear();
    for (final product in products) {
      tileNotifiers[product.productTrackingId] =
          ValueNotifier<ProductModel>(product);
    }
  }

  Future<void> toggleStockSecure(
      String productId, String token, BuildContext context,) async {
    final notifier = tileNotifiers[productId];
    if (notifier == null) return;

    final originalProduct = notifier.value;
    final int originalStock = originalProduct.stock;

    // Optimistic toggle: if stock is 0 (out of stock) -> toggle to 99 (in stock), else toggle to 0
    final int targetStock = originalStock == 0 ? 99 : 0;

    // A. 0ms Optimistic UI update
    notifier.value = originalProduct.copyWith(stock: targetStock);

    // B. Asynchronous background network sync
    try {
      // Dynamic Host Loopback detection depending on platforms:
      // Linux Host/Desktop: http://127.0.0.1:8082
      // Android emulator: http://10.0.2.2:8082
      final url = Uri.parse(ApiEndpoints.productStockToggle(productId));

      final response = await http
          .patch(
            url,
            headers: {
              'Content-Type': 'application/json',
              'Authorization': 'Bearer $token',
            },
            body: jsonEncode({'stock': targetStock}),
          )
          .timeout(const Duration(seconds: 4));

      if (response.statusCode != 200) {
        throw Exception(
            'Server rejected request with status code ${response.statusCode}',);
      }
    } catch (e) {
      // C. Safe Rollback: catch block instantly restores values from original state snapshot on exception
      notifier.value = originalProduct;

      if (context.mounted) {
        unawaited(
          showDialog<void>(
            context: context,
            builder: (context) => InventoryErrorDialog(
              errorMessage:
                  'Stock sync failed. Check your network.\nDetails: ${e.toString()}',
            ),
          ),
        );
      }
    }
  }

  void dispose() {
    for (final notifier in tileNotifiers.values) {
      notifier.dispose();
    }
    isLoading.dispose();
    errorMessage.dispose();
  }
}

// 3. User-friendly Glassmorphic Modal Error state dialog
class InventoryErrorDialog extends StatelessWidget {
  const InventoryErrorDialog({super.key, required this.errorMessage});
  final String errorMessage;

  @override
  Widget build(BuildContext context) {
    return Dialog(
      backgroundColor: Colors.transparent,
      insetPadding: const EdgeInsets.symmetric(horizontal: 24),
      child: Container(
        padding: const EdgeInsets.all(28),
        decoration: BoxDecoration(
          color: Colors.grey.shade900,
          borderRadius: BorderRadius.circular(32),
          border: Border.all(color: Colors.red.withValues(alpha: 0.3), width: 1.5),
          boxShadow: [
            BoxShadow(
              color: Colors.black.withValues(alpha: 0.5),
              blurRadius: 20,
              offset: const Offset(0, 10),
            ),
          ],
        ),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Container(
              padding: const EdgeInsets.all(16),
              decoration: BoxDecoration(
                color: Colors.red.withValues(alpha: 0.1),
                shape: BoxShape.circle,
              ),
              child: const Icon(
                Icons.cloud_off_rounded,
                color: Colors.redAccent,
                size: 40,
              ),
            ),
            const SizedBox(height: 20),
            const Text(
              'Sync Connectivity Loss',
              style: TextStyle(
                color: Colors.white,
                fontSize: 18,
                fontWeight: FontWeight.bold,
              ),
            ),
            const SizedBox(height: 12),
            Text(
              errorMessage,
              textAlign: TextAlign.center,
              style: TextStyle(
                color: Colors.grey.shade400,
                fontSize: 13,
                height: 1.4,
              ),
            ),
            const SizedBox(height: 24),
            SizedBox(
              width: double.infinity,
              child: ElevatedButton(
                onPressed: () => Navigator.pop(context),
                style: ElevatedButton.styleFrom(
                  backgroundColor: Colors.redAccent,
                  foregroundColor: Colors.white,
                  shape: RoundedRectangleBorder(
                    borderRadius: BorderRadius.circular(16),
                  ),
                  padding: const EdgeInsets.symmetric(vertical: 14),
                ),
                child: const Text(
                  'Dismiss',
                  style: TextStyle(fontWeight: FontWeight.bold),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

// 4. Main Inventory Catalog Grid Screen
class VendorInventoryScreen extends StatefulWidget {
  const VendorInventoryScreen({super.key, required this.vendorTrackingId});
  final String vendorTrackingId;

  @override
  VendorInventoryScreenState createState() => VendorInventoryScreenState();
}

class VendorInventoryScreenState extends State<VendorInventoryScreen> {
  final VendorInventoryController _controller = VendorInventoryController();
  LatLng _storeLocation = const LatLng(31.5204, 74.3587); // Default: Lahore
  String? _storeTrackingId;

  List<ProductModel> _loadedProducts = [];
  String _jwtToken = '';
  final int _limit = 20;
  int _offset = 0;
  bool _hasMore = true;
  bool _isFetchingMore = false;

  @override
  void initState() {
    super.initState();
    _initStoreLocation();
    _fetchProducts(refresh: true);
  }

  Future<void> _initStoreLocation() async {
    try {
      final token = SessionRegistry.instance.token ?? '';
      final response = await http.get(
        Uri.parse(ApiEndpoints.vendorStoreMe()),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
      ).timeout(const Duration(seconds: 8));

      if (response.statusCode == 200) {
        final data = jsonDecode(response.body) as Map<String, dynamic>;
        final lat = (data['latitude'] as num?)?.toDouble();
        final lng = (data['longitude'] as num?)?.toDouble();
        final storeId = data['store_tracking_id'] as String?;
        if (mounted) {
          setState(() {
            if (lat != null && lng != null && lat != 0.0 && lng != 0.0) {
              _storeLocation = LatLng(lat, lng);
            }
            if (storeId != null && storeId.isNotEmpty) {
              _storeTrackingId = storeId;
            }
          });
        }
      }
    } catch (e) {
      debugPrint('Error fetching store location: $e');
    }
  }

  Future<void> _fetchProducts({bool refresh = false}) async {
    if (refresh) {
      _offset = 0;
      _hasMore = true;
    }
    if (!_hasMore || _isFetchingMore) return;

    _isFetchingMore = true;
    _controller.isLoading.value = refresh;
    _controller.errorMessage.value = null;

    try {
      final prefs = await SharedPreferences.getInstance();
      _jwtToken = prefs.getString('jwt_token') ?? '';

      final url = Uri.parse(ApiEndpoints.vendorProducts(
        limit: _limit,
        offset: _offset,
      ),);

      final response = await http.get(url, headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer $_jwtToken',
      },);

      if (response.statusCode == 200) {
        final dynamic decoded = jsonDecode(response.body);
        final List<dynamic> listData = decoded is Map<String, dynamic>
            ? (decoded['products'] as List<dynamic>? ?? <dynamic>[])
            : (decoded is List<dynamic> ? decoded : <dynamic>[]);
        final list = listData
            .map((item) => ProductModel.fromJson(item as Map<String, dynamic>))
            .toList();

        setState(() {
          if (refresh) {
            _loadedProducts = list;
          } else {
            _loadedProducts.addAll(list);
          }
          _hasMore = list.length == _limit;
          _offset += list.length;
        });
        _controller.populate(_loadedProducts);
      } else {
        _controller.errorMessage.value =
            'Failed to fetch catalog. Status code: ${response.statusCode}';
      }
    } catch (e) {
      _controller.errorMessage.value = 'Failed to connect: ${e.toString()}';
    } finally {
      _controller.isLoading.value = false;
      _isFetchingMore = false;
    }
  }

  Future<void> _refresh() => _fetchProducts(refresh: true);

  // ── Product CRUD wiring (Phase 2 fix) ────────────────────────────

  /// Navigate to the Add/Edit form. Pass an existing product to enter edit
  /// mode, or null to create a new one. On pop with `true`, refresh the list.
  Future<void> _openProductEditor({ProductModel? existing}) async {
    final effectiveStoreId = existing?.storeTrackingId ??
        _storeTrackingId ??
        (_loadedProducts.isNotEmpty ? _loadedProducts.first.storeTrackingId : '');

    final result = await Navigator.push<bool>(
      context,
      MaterialPageRoute(
        builder: (_) => VendorAddProductScreen(
          vendorTrackingId: widget.vendorTrackingId,
          storeTrackingId: effectiveStoreId,
          existing: existing,
        ),
      ),
    );
    if (result == true) {
      unawaited(_fetchProducts(refresh: true));
    }
  }

  /// Confirms and deletes a product via the secure DELETE endpoint.
  Future<void> _confirmDeleteProduct(ProductModel product) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        backgroundColor: Colors.white,
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(24)),
        title: const Text('Delete Product',
            style: TextStyle(
                fontWeight: FontWeight.bold, color: AppTheme.blackAccent,),),
        content: Text(
            'Are you sure you want to remove "${product.name}" from your catalog? This cannot be undone.',
            style: const TextStyle(color: Colors.grey),),
        actions: [
          TextButton(
              onPressed: () => Navigator.pop(ctx, false),
              child: const Text('Cancel'),),
          TextButton(
            onPressed: () => Navigator.pop(ctx, true),
            child: const Text('Delete',
                style: TextStyle(
                    color: Colors.redAccent, fontWeight: FontWeight.bold,),),
          ),
        ],
      ),
    );

    if (confirmed != true) return;

    try {
      final response = await http.delete(
        Uri.parse(ApiEndpoints.vendorProductDelete(product.productTrackingId)),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $_jwtToken',
        },
      ).timeout(const Duration(seconds: 8));

      if (response.statusCode == 200 && mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
            content: Text('Product deleted'),
            backgroundColor: Colors.green,
            behavior: SnackBarBehavior.floating,
          ),
        );
        unawaited(_fetchProducts(refresh: true));
      } else if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
            content: Text('Delete failed (${response.statusCode})'),
            backgroundColor: Colors.redAccent,
            behavior: SnackBarBehavior.floating,
          ),
        );
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
              content: Text('Network error: $e'),
              backgroundColor: Colors.redAccent,),
        );
      }
    }
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: AppTheme.bgColor,
      appBar: AppBar(
        title: const Text('Store Catalog',
            style: TextStyle(color: Colors.black, fontWeight: FontWeight.bold),),
        backgroundColor: Colors.white,
        elevation: 0,
        leading: IconButton(
          icon: const Icon(Icons.arrow_back_rounded, color: Colors.black),
          onPressed: () => Navigator.pop(context),
        ),
        actions: [
          IconButton(
            icon: const Icon(Icons.add_rounded, color: AppTheme.blackAccent),
            tooltip: 'Add Product',
            onPressed: () => _openProductEditor(),
          ),
        ],
      ),
      floatingActionButton: FloatingActionButton.extended(
        onPressed: () => _openProductEditor(),
        backgroundColor: AppTheme.blackAccent,
        foregroundColor: AppTheme.limeAccent,
        icon: const Icon(Icons.add_rounded),
        label: const Text('Add Product',
            style: TextStyle(fontWeight: FontWeight.bold),),
      ),
      body: Column(
        children: [
          // A. OpenStreetMap embedded Map widget (Top Section)
          SizedBox(
            height: 220,
            child: Stack(
              children: [
                MapLibreMapWidget(
                  initialCenter: _storeLocation,
                  initialZoom: 14.0,
                  myLocationEnabled: false,
                  markers: {
                    'store': MarkerData(
                      position: _storeLocation,
                      iconSize: 1.0,
                    ),
                  },
                  onMapCreated: (_) {},
                ),
                Positioned(
                  bottom: 12,
                  left: 12,
                  child: Container(
                    padding:
                        const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
                    decoration: BoxDecoration(
                      color: Colors.black.withValues(alpha: 0.85),
                      borderRadius: BorderRadius.circular(12),
                    ),
                    child: const Row(
                      children: [
                        Icon(Icons.map_rounded,
                            color: Color(0xFFCAFF33), size: 14,),
                        SizedBox(width: 6),
                        Text(
                          'STOR-001 Coordinates (OSM)',
                          style: TextStyle(
                              color: Colors.white,
                              fontSize: 10,
                              fontWeight: FontWeight.bold,),
                        ),
                      ],
                    ),
                  ),
                ),
              ],
            ),
          ),

          // B. Catalog Grid List (Bottom Section)
          Expanded(
            child: ValueListenableBuilder<bool>(
              valueListenable: _controller.isLoading,
              builder: (context, isLoading, _) {
                if (isLoading && _loadedProducts.isEmpty) {
                  return const Center(
                      child: CircularProgressIndicator(
                          color: AppTheme.blackAccent,),);
                }

                return ValueListenableBuilder<String?>(
                  valueListenable: _controller.errorMessage,
                  builder: (context, error, _) {
                    if (error != null && _loadedProducts.isEmpty) {
                      return Center(
                        child: Padding(
                          padding: const EdgeInsets.all(24.0),
                          child: Column(
                            mainAxisAlignment: MainAxisAlignment.center,
                            children: [
                              const Icon(Icons.error_outline_rounded,
                                  color: Colors.redAccent, size: 48,),
                              const SizedBox(height: 16),
                              Text(error,
                                  textAlign: TextAlign.center,
                                  style: const TextStyle(color: Colors.grey),),
                              const SizedBox(height: 24),
                              ElevatedButton(
                                onPressed: () => _fetchProducts(refresh: true),
                                style: ElevatedButton.styleFrom(
                                    backgroundColor: AppTheme.blackAccent,),
                                child: const Text('Retry'),
                              ),
                            ],
                          ),
                        ),
                      );
                    }

                    if (_loadedProducts.isEmpty) {
                      return const Center(
                        child: Text(
                          'No products listed in your store catalog.',
                          style: TextStyle(color: Colors.grey, fontSize: 14),
                        ),
                      );
                    }

                    return Padding(
                      padding: const EdgeInsets.all(16.0),
                      child: RefreshIndicator(
                        onRefresh: _refresh,
                        child: NotificationListener<ScrollNotification>(
                          onNotification: (scrollInfo) {
                            if (scrollInfo.metrics.pixels >=
                                    scrollInfo.metrics.maxScrollExtent - 200 &&
                                _hasMore &&
                                !_isFetchingMore) {
                              _fetchProducts();
                            }
                            return false;
                          },
                          child: GridView.builder(
                            physics: const AlwaysScrollableScrollPhysics(),
                            itemCount: _loadedProducts.length + (_hasMore ? 1 : 0),
                            gridDelegate:
                                const SliverGridDelegateWithFixedCrossAxisCount(
                              crossAxisCount: 2,
                              crossAxisSpacing: 14,
                              mainAxisSpacing: 14,
                              childAspectRatio: 0.76,
                            ),
                            itemBuilder: (context, index) {
                              if (index == _loadedProducts.length) {
                                return const Center(
                                  child: Padding(
                                    padding: EdgeInsets.all(16.0),
                                    child: CircularProgressIndicator(
                                        color: AppTheme.blackAccent,),
                                  ),
                                );
                              }

                              final product = _loadedProducts[index];
                          final notifier = _controller
                              .tileNotifiers[product.productTrackingId]!;

                          return ValueListenableBuilder<ProductModel>(
                            valueListenable: notifier,
                            builder: (context, liveProduct, _) {
                              final bool isOutOfStock = liveProduct.stock == 0;

                              return GestureDetector(
                                onTap: () =>
                                    _openProductEditor(existing: liveProduct),
                                onLongPress: () =>
                                    _confirmDeleteProduct(liveProduct),
                                child: Card(
                                  elevation: 0,
                                  shape: RoundedRectangleBorder(
                                    borderRadius: BorderRadius.circular(24),
                                    side: BorderSide(
                                      color: isOutOfStock
                                          ? Colors.red.withValues(alpha: 0.2)
                                          : Colors.grey.shade200,
                                      width: 1.5,
                                    ),
                                  ),
                                  child: Stack(
                                    children: [
                                      Padding(
                                        padding: const EdgeInsets.all(12.0),
                                        child: Column(
                                          crossAxisAlignment:
                                              CrossAxisAlignment.start,
                                          children: [
                                            Expanded(
                                              child: ClipRRect(
                                                borderRadius:
                                                    BorderRadius.circular(16),
                                                child: Container(
                                                  color: Colors.grey.shade100,
                                                  width: double.infinity,
                                                  child: liveProduct
                                                          .imageUrl.isNotEmpty
                                                      ? Image.network(
                                                          liveProduct.imageUrl,
                                                          fit: BoxFit.cover,
                                                          errorBuilder: (context, error, stackTrace) =>
                                                              const Icon(
                                                                Icons
                                                                    .image_not_supported_rounded,
                                                                color: Colors.grey,
                                                              ),
                                                        )
                                                      : const Icon(
                                                          Icons
                                                              .image_not_supported_rounded,
                                                          color: Colors.grey,),
                                                ),
                                              ),
                                            ),
                                            const SizedBox(height: 12),
                                            Text(
                                              liveProduct.name,
                                              maxLines: 1,
                                              overflow: TextOverflow.ellipsis,
                                              style: const TextStyle(
                                                  fontWeight: FontWeight.bold,
                                                  fontSize: 13,
                                                  color: AppTheme.blackAccent,),
                                            ),
                                            const SizedBox(height: 4),
                                            Text(
                                              'Price: Rs.${liveProduct.basePrice}',
                                              style: const TextStyle(
                                                  fontWeight: FontWeight.bold,
                                                  color: Colors.blue,
                                                  fontSize: 11,),
                                            ),
                                            const SizedBox(height: 8),
                                            Row(
                                              mainAxisAlignment:
                                                  MainAxisAlignment
                                                      .spaceBetween,
                                              children: [
                                                Text(
                                                  isOutOfStock
                                                      ? 'Out of Stock'
                                                      : 'Stock: ${liveProduct.stock}',
                                                  style: TextStyle(
                                                    color: isOutOfStock
                                                        ? Colors.redAccent
                                                        : Colors.green,
                                                    fontWeight: FontWeight.bold,
                                                    fontSize: 10,
                                                  ),
                                                ),
                                                Transform.scale(
                                                  scale: 0.72,
                                                  child: Switch(
                                                    value: !isOutOfStock,
                                                    activeColor:
                                                        const Color(0xFFCAFF33),
                                                    activeTrackColor:
                                                        Colors.black,
                                                    onChanged: (bool val) {
                                                      _controller
                                                          .toggleStockSecure(
                                                        liveProduct
                                                            .productTrackingId,
                                                        _jwtToken,
                                                        context,
                                                      );
                                                    },
                                                  ),
                                                ),
                                              ],
                                            ),
                                          ],
                                        ),
                                      ),
                                      if (isOutOfStock)
                                        Positioned.fill(
                                          child: Container(
                                            decoration: BoxDecoration(
                                              color: Colors.black
                                                  .withValues(alpha: 0.38),
                                              borderRadius:
                                                  BorderRadius.circular(24),
                                            ),
                                            child: Center(
                                              child: Container(
                                                padding:
                                                    const EdgeInsets.symmetric(
                                                        horizontal: 12,
                                                        vertical: 6,),
                                                decoration: BoxDecoration(
                                                  color: Colors.redAccent,
                                                  borderRadius:
                                                      BorderRadius.circular(8),
                                                ),
                                                child: const Text(
                                                  'OUT OF STOCK',
                                                  style: TextStyle(
                                                      color: Colors.white,
                                                      fontWeight:
                                                          FontWeight.bold,
                                                      fontSize: 10,),
                                                ),
                                              ),
                                            ),
                                          ),
                                        ),
                                      ],
                                    ),
                                  ),
                                );
                                },
                              );
                            },
                          ),
                        ),
                      ),
                    );
                  },
                );
              },
            ),
          ),
        ],
      ),
    );
  }
}
