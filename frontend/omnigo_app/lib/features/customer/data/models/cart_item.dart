class CartItem {

  CartItem({
    required this.productId,
    required this.name,
    required this.price,
    required this.quantity,
    required this.storeTrackingId,
  });

  factory CartItem.fromJson(Map<String, dynamic> json) => CartItem(
        productId: json['product_id'] as String,
        name: json['name'] as String,
        price: (json['price'] as num).toDouble(),
        quantity: json['quantity'] as int,
        storeTrackingId: (json['store_tracking_id'] ?? 'STOR-001') as String,
      );
  final String productId;
  final String name;
  final double price;
  int quantity;
  final String storeTrackingId;

  Map<String, dynamic> toJson() => {
        'product_id': productId,
        'name': name,
        'price': price,
        'quantity': quantity,
        'store_tracking_id': storeTrackingId,
      };
}
