import 'dart:convert';
import 'package:flutter/foundation.dart';
import 'package:shared_preferences/shared_preferences.dart';
import '../../features/customer/data/models/cart_item.dart';
import '../../features/customer/data/models/product.dart';
import '../di/service_locator.dart';
import '../network/api_client.dart';
import '../network/api_endpoints.dart';
import 'session_registry.dart';

class CartProvider extends ChangeNotifier {
  final Map<String, CartItem> _items = {};
  // #35: Simple mutex pattern for _saveToStorage to prevent concurrent writes
  bool _isSaving = false;
  bool _pendingSave = false;
  double _deliveryFee = 0.0;

  Map<String, CartItem> get items => {..._items};

  int get itemCount => _items.values.fold(0, (sum, item) => sum + item.quantity);
  double get totalAmount => _items.values.fold(0.0, (sum, item) => sum + (item.price * item.quantity));
  double get deliveryFee => _deliveryFee;
  double get grandTotal => totalAmount + _deliveryFee;

  String? get currentStoreId => _items.values.isNotEmpty ? _items.values.first.storeTrackingId : null;

  bool get _isAuthenticated =>
      SessionRegistry.instance.token != null &&
      SessionRegistry.instance.token!.isNotEmpty;

  void setDeliveryFee(double fee) {
    _deliveryFee = fee;
    notifyListeners();
  }

  bool isDifferentStore(String storeTrackingId) {
    if (_items.isEmpty) return false;
    return _items.values.first.storeTrackingId != storeTrackingId;
  }

  Future<void> loadCart() async {
    try {
      final prefs = await SharedPreferences.getInstance();
      final data = prefs.getString('customer_cart') ?? '[]';
      final List<dynamic> list = jsonDecode(data) as List<dynamic>;
      _items.clear();
      for (var json in list) {
        final item = CartItem.fromJson(json as Map<String, dynamic>);
        _items[item.productId] = item;
      }
      notifyListeners();

      // Background sync with Go backend Cart Service if user is authenticated
      if (_isAuthenticated) {
        try {
          final res = await sl<ApiClient>().get(ApiEndpoints.cart());
          if (res is Map<String, dynamic>) {
            final backendItems = res['items'] as List<dynamic>?;
            final backendStoreId = (res['store_id'] ?? '').toString();
            if (backendItems != null && backendItems.isNotEmpty) {
              _items.clear();
              for (var raw in backendItems) {
                final map = raw as Map<String, dynamic>;
                if (!map.containsKey('store_id') && backendStoreId.isNotEmpty) {
                  map['store_id'] = backendStoreId;
                }
                final item = CartItem.fromJson(map);
                _items[item.productId] = item;
              }
              await _saveToStorage();
              notifyListeners();
            }
          }
        } catch (netErr) {
          debugPrint('Backend cart sync skipped: $netErr');
        }
      }
    } catch (e) {
      debugPrint('Error loading cart: $e');
    }
  }

  Future<void> addItem(Product product, {int quantity = 1, bool clearIfDifferentStore = false}) async {
    final productId = product.productTrackingId;
    final storeTrackingId = product.storeTrackingId;

    if (productId.isEmpty || storeTrackingId.isEmpty) {
      throw Exception('Invalid product data. Cannot add to cart.');
    }

    if (_items.isNotEmpty && _items.values.first.storeTrackingId != storeTrackingId) {
      if (clearIfDifferentStore) {
        _items.clear();
      } else {
        throw Exception('DIFFERENT_STORE');
      }
    }

    // Attempt backend sync first if authenticated to enforce stock limits (SP-GO-20)
    if (_isAuthenticated) {
      try {
        await sl<ApiClient>().post(
          ApiEndpoints.cartItems(),
          {
            'product_tracking_id': productId,
            'store_id': storeTrackingId,
            'quantity': quantity,
          },
        );
      } catch (e) {
        debugPrint('Failed to sync added item with backend: $e');
        rethrow;
      }
    }

    final name = product.name.isNotEmpty ? product.name : 'Unknown Product';
    final price = product.basePrice;

    if (_items.containsKey(productId)) {
      // CartItem is immutable (MEDIUM-14) — replace via copyWith.
      final existing = _items[productId]!;
      _items[productId] = existing.copyWith(quantity: existing.quantity + quantity);
    } else {
      _items[productId] = CartItem(
        productId: productId,
        name: name,
        price: price,
        quantity: quantity,
        storeTrackingId: storeTrackingId,
      );
    }
    await _saveToStorage();
    notifyListeners();
  }

  Future<void> addCartItem(CartItem item, {bool clearIfDifferentStore = false}) async {
    final product = Product(
      productTrackingId: item.productId,
      storeTrackingId: item.storeTrackingId,
      name: item.name,
      description: '',
      basePrice: item.price,
    );
    await addItem(product, quantity: item.quantity, clearIfDifferentStore: clearIfDifferentStore);
  }

  Future<void> removeItem(String productId) async {
    _items.remove(productId);
    await _saveToStorage();
    notifyListeners();

    if (_isAuthenticated) {
      try {
        await sl<ApiClient>().delete(ApiEndpoints.cartItem(productId));
      } catch (e) {
        debugPrint('Failed to sync removed item with backend: $e');
      }
    }
  }

  Future<void> updateQuantity(String productId, int quantity) async {
    if (!_items.containsKey(productId)) return;

    if (_isAuthenticated) {
      try {
        if (quantity <= 0) {
          await sl<ApiClient>().delete(ApiEndpoints.cartItem(productId));
        } else {
          await sl<ApiClient>().put(
            ApiEndpoints.cartItem(productId),
            {'quantity': quantity},
          );
        }
      } catch (e) {
        debugPrint('Failed to sync updated quantity with backend: $e');
        rethrow;
      }
    }

    if (quantity <= 0) {
      _items.remove(productId);
    } else {
      _items[productId] = _items[productId]!.copyWith(quantity: quantity);
    }
    await _saveToStorage();
    notifyListeners();
  }

  Future<void> clearCart() async {
    _items.clear();
    await _saveToStorage();
    notifyListeners();

    if (_isAuthenticated) {
      try {
        await sl<ApiClient>().delete(ApiEndpoints.cart());
      } catch (e) {
        debugPrint('Failed to clear cart on backend: $e');
      }
    }
  }

  Future<void> _saveToStorage() async {
    if (_isSaving) {
      _pendingSave = true;
      return;
    }
    _isSaving = true;
    try {
      final prefs = await SharedPreferences.getInstance();
      final list = _items.values.map((item) => item.toJson()).toList();
      await prefs.setString('customer_cart', jsonEncode(list));
    } catch (e) {
      debugPrint('Error saving cart: $e');
    } finally {
      _isSaving = false;
      if (_pendingSave) {
        _pendingSave = false;
        await _saveToStorage();
      }
    }
  }
}
