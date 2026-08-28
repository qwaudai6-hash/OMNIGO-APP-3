class Product {

  Product({
    required this.productTrackingId,
    required this.storeTrackingId,
    required this.name,
    required this.description,
    required this.basePrice,
    this.id = 0,
    this.stock = 0,
    this.imageUrl,
    this.category,
    this.isActive = true,
    this.isFeatured = false,
    this.sku,
    this.storeName,
    this.storeLogoUrl,
    this.storeBannerUrl,
    this.vendorTrackingId,
  });

  factory Product.fromJson(Map<String, dynamic> json) {
    return Product(
      id: (json['id'] as num?)?.toInt() ?? 0,
      productTrackingId: (json['product_tracking_id'] ?? '') as String,
      storeTrackingId: (json['store_tracking_id'] ?? '') as String,
      vendorTrackingId: json['vendor_tracking_id']?.toString(),
      sku: json['sku']?.toString(),
      name: (json['name'] ?? 'Unknown Product') as String,
      description: (json['description'] ?? '') as String,
      basePrice: (json['base_price'] as num?)?.toDouble() ?? 0.0,
      stock: (json['stock'] as num?)?.toInt() ?? 0,
      imageUrl: json['image_url']?.toString(),
      category: json['category']?.toString(),
      isActive: (json['is_active'] as bool?) ?? true,
      isFeatured: (json['is_featured'] as bool?) ?? false,
      storeName: json['store_name']?.toString(),
      storeLogoUrl: json['store_logo_url']?.toString() ?? json['logo_url']?.toString(),
      storeBannerUrl: json['store_banner_url']?.toString() ?? json['banner_url']?.toString(),
    );
  }

  final int id;
  final String productTrackingId;
  final String storeTrackingId;
  final String? vendorTrackingId;
  final String? sku;
  final String name;
  final String description;
  final double basePrice;
  final int stock;
  final String? imageUrl;
  final String? category;
  final bool isActive;
  final bool isFeatured;
  final String? storeName;
  final String? storeLogoUrl;
  final String? storeBannerUrl;

  List<String> get allImages {
    final list = <String>[];
    if (imageUrl != null && imageUrl!.isNotEmpty) {
      list.add(imageUrl!);
    }
    return list;
  }

  Map<String, dynamic> toJson() => {
        'id': id,
        'product_tracking_id': productTrackingId,
        'store_tracking_id': storeTrackingId,
        'vendor_tracking_id': vendorTrackingId,
        'sku': sku,
        'name': name,
        'description': description,
        'base_price': basePrice,
        'stock': stock,
        'image_url': imageUrl,
        'category': category,
        'is_active': isActive,
        'is_featured': isFeatured,
        'store_name': storeName,
        'store_logo_url': storeLogoUrl,
        'store_banner_url': storeBannerUrl,
      };
}
