import 'dart:convert';
import 'package:flutter/foundation.dart';
import 'package:shared_preferences/shared_preferences.dart';
import '../../features/customer/data/models/cart_item.dart';
import '../../features/customer/data/models/product.dart';

class CartProvider extends ChangeNotifier {
  final Map<String, CartItem> _items = {};
  // #35: Simple mutex pattern for _saveToStorage to prevent concurrent writes
  bool _isSaving = false;
  bool _pendingSave = false;

  Map<String, CartItem> get items => {..._items};

  int get itemCount => _items.values.fold(0, (sum, item) => sum + item.quantity);
  double get totalAmount => _items.values.fold(0.0, (sum, item) => sum + (item.price * item.quantity));

  String? get currentStoreId => _items.values.isNotEmpty ? _items.values.first.storeTrackingId : null;

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

  Future<void> removeItem(String productId) async {
    _items.remove(productId);
    await _saveToStorage();
    notifyListeners();
  }

  Future<void> updateQuantity(String productId, int quantity) async {
    if (_items.containsKey(productId)) {
      if (quantity <= 0) {
        _items.remove(productId);
      } else {
        _items[productId] = _items[productId]!.copyWith(quantity: quantity);
      }
      await _saveToStorage();
      notifyListeners();
    }
  }

  Future<void> clearCart() async {
    _items.clear();
    await _saveToStorage();
    notifyListeners();
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
        _saveToStorage();
      }
    }
  }
}
