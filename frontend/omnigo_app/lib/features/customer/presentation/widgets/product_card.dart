import 'package:flutter/material.dart';
import 'package:provider/provider.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/services/cart_provider.dart';
import '../../data/models/product.dart';
import '../screens/product_details_screen.dart';

class ProductCard extends StatelessWidget {

  const ProductCard({
    super.key,
    required this.product,
    required this.userTrackingId,
    required this.isFavorited,
    required this.onFavoriteToggle,
  });
  final Product product;
  final String userTrackingId;
  final bool isFavorited;
  final VoidCallback onFavoriteToggle;

  @override
  Widget build(BuildContext context) {
    final String name = product.name.isNotEmpty ? product.name : 'Unknown';
    final double price = product.basePrice;
    final String storeId = product.storeTrackingId;

    return GestureDetector(
      onTap: () {
        Navigator.push(
          context,
          MaterialPageRoute<void>(
            builder: (context) => ProductDetailsScreen(
              product: product,
              userTrackingId: userTrackingId,
            ),
          ),
        );
      },
      child: Container(
        decoration: BoxDecoration(
          color: Colors.white,
          borderRadius: BorderRadius.circular(24),
          boxShadow: [
            BoxShadow(
              color: Colors.black.withOpacity(0.04),
              blurRadius: 16,
              offset: const Offset(0, 6),
            ),
          ],
        ),
        clipBehavior: Clip.antiAlias,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // Image Canvas + Heart Float
            Expanded(
              child: Stack(
                children: [
                  Positioned.fill(
                    child: Container(
                      color: AppTheme.softBlue,
                      child: product.imageUrl != null &&
                              product.imageUrl!.isNotEmpty
                          ? Image.network(
                              product.imageUrl!,
                              fit: BoxFit.cover,
                              errorBuilder: (context, error, stackTrace) =>
                                  const Center(
                                child: Icon(Icons.image_not_supported,
                                    color: Colors.grey,),
                              ),
                            )
                          : const Center(
                              child: Icon(Icons.shopping_bag_outlined,
                                  size: 32, color: Colors.grey,),
                            ),
                    ),
                  ),
                  Positioned(
                    top: 10,
                    right: 10,
                    child: GestureDetector(
                      onTap: onFavoriteToggle,
                      child: Container(
                        padding: const EdgeInsets.all(6),
                        decoration: BoxDecoration(
                          color: Colors.white.withOpacity(0.9),
                          shape: BoxShape.circle,
                        ),
                        child: Icon(
                          isFavorited
                              ? Icons.favorite
                              : Icons.favorite_border,
                          size: 16,
                          color: isFavorited
                              ? Colors.redAccent
                              : AppTheme.blackAccent,
                        ),
                      ),
                    ),
                  ),
                ],
              ),
            ),
            // Product Info
            Padding(
              padding: const EdgeInsets.all(12),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(name,
                      style: const TextStyle(
                          fontWeight: FontWeight.bold,
                          color: AppTheme.blackAccent,
                          fontSize: 15,),
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,),
                  if (storeId.isNotEmpty) ...[
                    const SizedBox(height: 4),
                    Text(storeId,
                        style: const TextStyle(color: Colors.grey, fontSize: 11),),
                  ],
                  const SizedBox(height: 8),
                  Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    children: [
                      Text('PKR ${price.toStringAsFixed(2)}',
                          style: const TextStyle(
                              fontWeight: FontWeight.w800,
                              color: AppTheme.blackAccent,),),
                      GestureDetector(
                        onTap: () {
                          context.read<CartProvider>().addItem(product);
                          ScaffoldMessenger.of(context).showSnackBar(
                            SnackBar(
                              content: Text('Added $name to cart!'),
                              backgroundColor: Colors.green,
                              behavior: SnackBarBehavior.floating,
                              duration: const Duration(milliseconds: 800),
                            ),
                          );
                        },
                        child: Container(
                          padding: const EdgeInsets.all(8),
                          decoration: const BoxDecoration(
                              color: AppTheme.blackAccent,
                              shape: BoxShape.circle,),
                          child: const Icon(Icons.add_shopping_cart_outlined,
                              color: AppTheme.limeAccent, size: 14,),
                        ),
                      ),
                    ],
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}
