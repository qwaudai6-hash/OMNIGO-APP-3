class Product {

  Product({
    required this.productTrackingId,
    required this.storeTrackingId,
    required this.name,
    required this.description,
    required this.basePrice,
    this.stock = 0,
    this.imageUrl,
    this.images,
    this.category,
    this.storeName,
    this.storeLogoUrl,
    this.storeBannerUrl,
    this.isFeatured = false,
  });

  factory Product.fromJson(Map<String, dynamic> json) {
    List<String>? parsedImages;
    if (json['images'] is List) {
      parsedImages = (json['images'] as List).map((e) => e.toString()).toList();
    }

    return Product(
      productTrackingId: (json['product_tracking_id'] ?? '') as String,
      storeTrackingId: (json['store_tracking_id'] ?? 'STOR-001') as String,
      name: (json['name'] ?? 'Unknown Product') as String,
      description: (json['description'] ?? '') as String,
      basePrice: (json['base_price'] as num?)?.toDouble() ?? 0.0,
      stock: (json['stock'] as num?)?.toInt() ?? 0,
      imageUrl: json['image_url']?.toString(),
      images: parsedImages,
      category: json['category']?.toString(),
      storeName: json['store_name']?.toString(),
      storeLogoUrl: json['store_logo_url']?.toString() ?? json['logo_url']?.toString(),
      storeBannerUrl: json['store_banner_url']?.toString() ?? json['banner_url']?.toString(),
      isFeatured: (json['is_featured'] as bool?) ?? false,
    );
  }

  final String productTrackingId;
  final String storeTrackingId;
  final String name;
  final String description;
  final double basePrice;
  final int stock;
  final String? imageUrl;
  final List<String>? images;
  final String? category;
  // ── Store info (joined / fetched separately) ──────────────────────
  final String? storeName;
  final String? storeLogoUrl;
  final String? storeBannerUrl;
  final bool isFeatured;

  List<String> get allImages {
    final list = <String>[];
    if (images != null && images!.isNotEmpty) {
      list.addAll(images!);
    } else if (imageUrl != null && imageUrl!.isNotEmpty) {
      list.add(imageUrl!);
    }
    return list;
  }

  Map<String, dynamic> toJson() => {
        'product_tracking_id': productTrackingId,
        'store_tracking_id': storeTrackingId,
        'name': name,
        'description': description,
        'base_price': basePrice,
        'stock': stock,
        'image_url': imageUrl,
        'images': images,
        'category': category,
        'store_name': storeName,
        'store_logo_url': storeLogoUrl,
        'store_banner_url': storeBannerUrl,
        'is_featured': isFeatured,
      };
}
