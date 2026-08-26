class CartItem {

  factory CartItem.fromJson(Map<String, dynamic> json) => CartItem(
        productId: (json['product_id'] ?? '').toString(),
        name: (json['name'] ?? '').toString(),
        price: (json['price'] as num?)?.toDouble() ?? 0.0,
        quantity: (json['quantity'] as num?)?.toInt() ?? 1,
        storeTrackingId: (json['store_tracking_id'] ?? '').toString(),
      );
  CartItem({
    required this.productId,
    required this.name,
    required this.price,
    required this.quantity,
    required this.storeTrackingId,
  });

  final String productId;
  final String name;
  final double price;
  final int quantity;
  final String storeTrackingId;

  CartItem copyWith({
    String? productId,
    String? name,
    double? price,
    int? quantity,
    String? storeTrackingId,
  }) =>
      CartItem(
        productId: productId ?? this.productId,
        name: name ?? this.name,
        price: price ?? this.price,
        quantity: quantity ?? this.quantity,
        storeTrackingId: storeTrackingId ?? this.storeTrackingId,
      );

  Map<String, dynamic> toJson() => {
        'product_id': productId,
        'name': name,
        'price': price,
        'quantity': quantity,
        'store_tracking_id': storeTrackingId,
      };
}
