import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:http/http.dart' as http;
import 'package:provider/provider.dart';
import 'package:shared_preferences/shared_preferences.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/services/cart_provider.dart';
import '../../data/models/product.dart';
import 'product_details_screen.dart';

/// Daraz-style full vendor store page.
///
/// Shows:
/// - Banner image (fullwidth hero)
/// - Store logo, name, rating badge
/// - "Follow" / share actions
/// - Search bar inside store
/// - Tabbed product grid (All / Featured / by category)
/// - Tap product → ProductDetailsScreen
/// - "Add to Cart" directly from grid
class VendorStorePage extends StatefulWidget {
  const VendorStorePage({
    super.key,
    required this.storeTrackingId,
    required this.userTrackingId,
    this.initialStoreName,
    this.initialLogoUrl,
    this.initialBannerUrl,
  });

  final String storeTrackingId;
  final String userTrackingId;
  final String? initialStoreName;
  final String? initialLogoUrl;
  final String? initialBannerUrl;

  @override
  State<VendorStorePage> createState() => _VendorStorePageState();
}

class _VendorStorePageState extends State<VendorStorePage>
    with SingleTickerProviderStateMixin {
  // ── State ────────────────────────────────────────────────────────
  Map<String, dynamic>? _storeData;
  List<Product> _allProducts = [];
  List<Product> _filtered = [];
  bool _isLoadingProducts = true;
  String _searchQuery = '';
  String _selectedCategory = 'All';
  late TabController _tabController;
  final _searchCtrl = TextEditingController();
  final _scrollCtrl = ScrollController();
  bool _showStickyHeader = false;

  double _avgRating = 0.0;
  int _totalRatings = 0;

  List<String> get _categories {
    final cats = <String>{'All'};
    for (final p in _allProducts) {
      if (p.category != null && p.category!.isNotEmpty) cats.add(p.category!);
    }
    return cats.toList();
  }

  @override
  void initState() {
    super.initState();
    _tabController = TabController(length: 2, vsync: this);
    _scrollCtrl.addListener(_onScroll);
    _fetchStore();
    _fetchProducts();
    _fetchVendorRating();
  }

  Future<void> _fetchVendorRating() async {
    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';
      final resp = await http.get(
        Uri.parse(ApiEndpoints.ratingForUser(widget.storeTrackingId)),
        headers: {'Authorization': 'Bearer $token'},
      ).timeout(const Duration(seconds: 8));

      if (resp.statusCode == 200 && mounted) {
        final data = jsonDecode(resp.body) as Map<String, dynamic>;
        setState(() {
          _avgRating = (data['average_rating'] as num?)?.toDouble() ?? 0.0;
          _totalRatings = (data['total_ratings'] as num?)?.toInt() ?? 0;
        });
      }
    } catch (_) {}
  }

  @override
  void dispose() {
    _tabController.dispose();
    _searchCtrl.dispose();
    _scrollCtrl.dispose();
    super.dispose();
  }

  void _onScroll() {
    final show = _scrollCtrl.offset > 200;
    if (show != _showStickyHeader) setState(() => _showStickyHeader = show);
  }

  // ── API ──────────────────────────────────────────────────────────

  Future<void> _fetchStore() async {
    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';
      final resp = await http.get(
        Uri.parse(ApiEndpoints.vendorStore(widget.storeTrackingId)),
        headers: {'Authorization': 'Bearer $token'},
      ).timeout(const Duration(seconds: 10));

      if (resp.statusCode == 200 && mounted) {
        setState(() {
          _storeData = jsonDecode(resp.body) as Map<String, dynamic>;
        });
      }
    } catch (_) {}
  }

  Future<void> _fetchProducts() async {
    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';
      // Use the public product list, filtered by store
      final uri = Uri.parse(
        '${ApiEndpoints.productBase}/products?store_id=${widget.storeTrackingId}&limit=60',
      );
      final resp = await http.get(
        uri,
        headers: {'Authorization': 'Bearer $token'},
      ).timeout(const Duration(seconds: 10));

      if (resp.statusCode == 200 && mounted) {
        final raw = jsonDecode(resp.body);
        final list = raw is List
            ? raw
            : (raw as Map<String, dynamic>)['products'] as List? ?? [];
        setState(() {
          _allProducts =
              list.map((e) => Product.fromJson(e as Map<String, dynamic>)).toList();
          _filtered = _allProducts;
          _isLoadingProducts = false;
        });
      } else if (mounted) {
        setState(() => _isLoadingProducts = false);
      }
    } catch (_) {
      if (mounted) setState(() => _isLoadingProducts = false);
    }
  }

  void _applyFilter() {
    final q = _searchQuery.toLowerCase();
    setState(() {
      _filtered = _allProducts.where((p) {
        final matchCat =
            _selectedCategory == 'All' || p.category == _selectedCategory;
        final matchQ = q.isEmpty ||
            p.name.toLowerCase().contains(q) ||
            (p.description.toLowerCase().contains(q));
        return matchCat && matchQ;
      }).toList();
    });
  }

  // ── Helpers ──────────────────────────────────────────────────────

  String get _storeName =>
      (_storeData?['store_name'] as String?) ??
      widget.initialStoreName ??
      'Vendor Store';

  String? get _logoUrl =>
      (_storeData?['logo_url'] as String?) ?? widget.initialLogoUrl;

  String? get _bannerUrl =>
      (_storeData?['banner_url'] as String?) ?? widget.initialBannerUrl;

  // ── Build ────────────────────────────────────────────────────────

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: const Color(0xFFF4F5F7),
      body: NestedScrollView(
        controller: _scrollCtrl,
        headerSliverBuilder: (ctx, inner) => [
          _buildHeroSliver(),
          _buildStoreInfoSliver(),
          _buildSearchAndFilterSliver(),
          _buildTabBarSliver(),
        ],
        body: TabBarView(
          controller: _tabController,
          children: [
            _buildProductGrid(
                _filtered.where((p) => !p.isFeatured).toList(),),
            _buildProductGrid(
                _filtered.where((p) => p.isFeatured).toList(),),
          ],
        ),
      ),
    );
  }

  // ── Slivers ──────────────────────────────────────────────────────

  Widget _buildHeroSliver() {
    return SliverAppBar(
      expandedHeight: 220,
      pinned: true,
      backgroundColor: AppTheme.blackAccent,
      iconTheme: const IconThemeData(color: Colors.white),
      title: _showStickyHeader
          ? Row(
              children: [
                if (_logoUrl != null && _logoUrl!.isNotEmpty)
                  CircleAvatar(
                    radius: 14,
                    backgroundImage: NetworkImage(_logoUrl!),
                    backgroundColor: Colors.white24,
                  ),
                const SizedBox(width: 8),
                Text(
                  _storeName,
                  style: const TextStyle(
                      color: Colors.white,
                      fontWeight: FontWeight.bold,
                      fontSize: 16,),
                ),
              ],
            )
          : null,
      flexibleSpace: FlexibleSpaceBar(
        background: Stack(
          fit: StackFit.expand,
          children: [
            // Banner
            if (_bannerUrl != null && _bannerUrl!.isNotEmpty)
              Image.network(
                _bannerUrl!,
                fit: BoxFit.cover,
                errorBuilder: (_, __, ___) => _defaultBanner(),
              )
            else
              _defaultBanner(),
            // Gradient overlay
            const DecoratedBox(
              decoration: BoxDecoration(
                gradient: LinearGradient(
                  begin: Alignment.topCenter,
                  end: Alignment.bottomCenter,
                  colors: [Colors.transparent, Colors.black54],
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _defaultBanner() {
    return Container(
      decoration: BoxDecoration(
        gradient: LinearGradient(
          colors: [
            AppTheme.blackAccent,
            AppTheme.limeAccent.withValues(alpha: 0.7),
          ],
          begin: Alignment.topLeft,
          end: Alignment.bottomRight,
        ),
      ),
    );
  }

  Widget _buildStoreInfoSliver() {
    return SliverToBoxAdapter(
      child: Container(
        color: Colors.white,
        padding: const EdgeInsets.fromLTRB(20, 0, 20, 16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // Logo + name row
            Row(
              crossAxisAlignment: CrossAxisAlignment.end,
              children: [
                // Logo circle (overlaps banner)
                Transform.translate(
                  offset: const Offset(0, -30),
                  child: Container(
                    decoration: BoxDecoration(
                      shape: BoxShape.circle,
                      border: Border.all(color: Colors.white, width: 3),
                      boxShadow: [
                        BoxShadow(
                          color: Colors.black.withValues(alpha: 0.12),
                          blurRadius: 12,
                        ),
                      ],
                    ),
                    child: CircleAvatar(
                      radius: 36,
                      backgroundColor: Colors.grey.shade200,
                      backgroundImage: (_logoUrl != null && _logoUrl!.isNotEmpty)
                          ? NetworkImage(_logoUrl!) as ImageProvider
                          : null,
                      child: (_logoUrl == null || _logoUrl!.isEmpty)
                          ? Text(
                              _storeName.isNotEmpty
                                  ? _storeName[0].toUpperCase()
                                  : 'S',
                              style: const TextStyle(
                                  fontWeight: FontWeight.bold,
                                  fontSize: 28,
                                  color: AppTheme.blackAccent,),
                            )
                          : null,
                    ),
                  ),
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      const SizedBox(height: 8),
                      Text(
                        _storeName,
                        style: const TextStyle(
                          fontSize: 22,
                          fontWeight: FontWeight.w900,
                          color: AppTheme.blackAccent,
                        ),
                      ),
                      const SizedBox(height: 4),
                      Row(
                        children: [
                          Container(
                            padding: const EdgeInsets.symmetric(
                                horizontal: 8, vertical: 3,),
                            decoration: BoxDecoration(
                              color: Colors.green.shade50,
                              borderRadius: BorderRadius.circular(20),
                              border: Border.all(color: Colors.green.shade200),
                            ),
                            child: Row(
                              mainAxisSize: MainAxisSize.min,
                              children: [
                                Icon(Icons.verified_outlined,
                                    size: 12, color: Colors.green.shade700,),
                                const SizedBox(width: 4),
                                Text(
                                  'Verified Seller',
                                  style: TextStyle(
                                      fontSize: 11,
                                      color: Colors.green.shade700,
                                      fontWeight: FontWeight.w600,),
                                ),
                              ],
                            ),
                          ),
                          if (_avgRating > 0) ...[
                            const SizedBox(width: 6),
                            Container(
                              padding: const EdgeInsets.symmetric(
                                  horizontal: 8, vertical: 3,),
                              decoration: BoxDecoration(
                                color: Colors.amber.shade50,
                                borderRadius: BorderRadius.circular(20),
                                border: Border.all(color: Colors.amber.shade300),
                              ),
                              child: Row(
                                mainAxisSize: MainAxisSize.min,
                                children: [
                                  const Icon(Icons.star_rounded, size: 12, color: Colors.amber),
                                  const SizedBox(width: 3),
                                  Text(
                                    '${_avgRating.toStringAsFixed(1)} ($_totalRatings)',
                                    style: TextStyle(
                                      fontSize: 11,
                                      color: Colors.amber.shade900,
                                      fontWeight: FontWeight.bold,
                                    ),
                                  ),
                                ],
                              ),
                            ),
                          ],
                          const SizedBox(width: 8),
                          Text(
                            '${_allProducts.length} products',
                            style: const TextStyle(
                                fontSize: 12, color: Colors.grey,),
                          ),
                          ],
                        ),
                      ],
                    ),
                  ),
              ],
            ),
            const SizedBox(height: 4),
            // Store tracking ID chip
            Row(
              children: [
                Container(
                  padding:
                      const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
                  decoration: BoxDecoration(
                    color: Colors.grey.shade100,
                    borderRadius: BorderRadius.circular(20),
                  ),
                  child: Text(
                    widget.storeTrackingId,
                    style: const TextStyle(
                        fontSize: 11,
                        color: Colors.grey,
                        fontFamily: 'monospace',),
                  ),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildSearchAndFilterSliver() {
    return SliverToBoxAdapter(
      child: Container(
        color: Colors.white,
        padding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // Search bar
            Container(
              height: 46,
              decoration: BoxDecoration(
                color: Colors.grey.shade100,
                borderRadius: BorderRadius.circular(14),
                border: Border.all(color: Colors.grey.shade200),
              ),
              child: TextField(
                controller: _searchCtrl,
                onChanged: (v) {
                  _searchQuery = v;
                  _applyFilter();
                },
                style: const TextStyle(fontSize: 14),
                decoration: InputDecoration(
                  hintText: 'Search in $_storeName…',
                  hintStyle:
                      const TextStyle(color: Colors.grey, fontSize: 14),
                  prefixIcon: const Icon(Icons.search, color: Colors.grey, size: 20),
                  suffixIcon: _searchQuery.isNotEmpty
                      ? IconButton(
                          icon: const Icon(Icons.clear, size: 18, color: Colors.grey),
                          onPressed: () {
                            _searchCtrl.clear();
                            _searchQuery = '';
                            _applyFilter();
                          },
                        )
                      : null,
                  border: InputBorder.none,
                  contentPadding: const EdgeInsets.symmetric(vertical: 13),
                ),
              ),
            ),
            const SizedBox(height: 10),
            // Category chips
            if (_categories.length > 1)
              SizedBox(
                height: 32,
                child: ListView.separated(
                  scrollDirection: Axis.horizontal,
                  itemCount: _categories.length,
                  separatorBuilder: (_, __) => const SizedBox(width: 8),
                  itemBuilder: (_, i) {
                    final cat = _categories[i];
                    final selected = cat == _selectedCategory;
                    return GestureDetector(
                      onTap: () {
                        setState(() => _selectedCategory = cat);
                        _applyFilter();
                      },
                      child: AnimatedContainer(
                        duration: const Duration(milliseconds: 200),
                        padding: const EdgeInsets.symmetric(
                            horizontal: 14, vertical: 6,),
                        decoration: BoxDecoration(
                          color: selected
                              ? AppTheme.blackAccent
                              : Colors.grey.shade100,
                          borderRadius: BorderRadius.circular(20),
                          border: Border.all(
                            color: selected
                                ? AppTheme.blackAccent
                                : Colors.grey.shade300,
                          ),
                        ),
                        child: Text(
                          cat,
                          style: TextStyle(
                            fontSize: 12,
                            fontWeight: FontWeight.w600,
                            color:
                                selected ? AppTheme.limeAccent : Colors.black87,
                          ),
                        ),
                      ),
                    );
                  },
                ),
              ),
          ],
        ),
      ),
    );
  }

  Widget _buildTabBarSliver() {
    return SliverPersistentHeader(
      pinned: true,
      delegate: _TabBarDelegate(
        TabBar(
          controller: _tabController,
          labelColor: AppTheme.blackAccent,
          unselectedLabelColor: Colors.grey,
          labelStyle:
              const TextStyle(fontWeight: FontWeight.bold, fontSize: 13),
          indicatorColor: AppTheme.limeAccent,
          indicatorWeight: 3,
          tabs: const [
            Tab(text: '  All Products  '),
            Tab(text: '  ⭐ Featured  '),
          ],
        ),
      ),
    );
  }

  // ── Product Grid ─────────────────────────────────────────────────

  Widget _buildProductGrid(List<Product> products) {
    if (_isLoadingProducts) {
      return const Center(
          child: CircularProgressIndicator(color: AppTheme.blackAccent),);
    }

    if (products.isEmpty) {
      return Center(
        child: Column(
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            Icon(Icons.inventory_2_outlined,
                size: 64, color: Colors.grey.shade300,),
            const SizedBox(height: 12),
            Text(
              _searchQuery.isNotEmpty
                  ? 'No results for "$_searchQuery"'
                  : 'No products yet',
              style: TextStyle(color: Colors.grey.shade500, fontSize: 15),
            ),
          ],
        ),
      );
    }

    return GridView.builder(
      padding: const EdgeInsets.all(12),
      gridDelegate: const SliverGridDelegateWithFixedCrossAxisCount(
        crossAxisCount: 2,
        mainAxisSpacing: 12,
        crossAxisSpacing: 12,
        childAspectRatio: 0.72,
      ),
      itemCount: products.length,
      itemBuilder: (_, i) => _buildProductCard(products[i]),
    );
  }

  Widget _buildProductCard(Product product) {
    return GestureDetector(
      onTap: () => Navigator.push(
        context,
        MaterialPageRoute<void>(
          builder: (_) => ProductDetailsScreen(
            product: product,
            userTrackingId: widget.userTrackingId,
          ),
        ),
      ),
      child: Container(
        decoration: BoxDecoration(
          color: Colors.white,
          borderRadius: BorderRadius.circular(18),
          boxShadow: [
            BoxShadow(
              color: Colors.black.withValues(alpha: 0.06),
              blurRadius: 10,
              offset: const Offset(0, 4),
            ),
          ],
        ),
        clipBehavior: Clip.antiAlias,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // Product image
            Expanded(
              flex: 5,
              child: Stack(
                fit: StackFit.expand,
                children: [
                  (product.imageUrl != null && product.imageUrl!.isNotEmpty)
                      ? Image.network(
                          product.imageUrl!,
                          fit: BoxFit.cover,
                          errorBuilder: (_, __, ___) => Container(
                            color: Colors.grey.shade100,
                            child: const Icon(Icons.shopping_bag_outlined,
                                size: 48, color: Colors.grey,),
                          ),
                        )
                      : Container(
                          color: Colors.grey.shade100,
                          child: const Icon(Icons.shopping_bag_outlined,
                              size: 48, color: Colors.grey,),
                        ),
                  if (product.isFeatured)
                    Positioned(
                      top: 8,
                      left: 8,
                      child: Container(
                        padding: const EdgeInsets.symmetric(
                            horizontal: 7, vertical: 3,),
                        decoration: BoxDecoration(
                          color: Colors.amber,
                          borderRadius: BorderRadius.circular(8),
                        ),
                        child: const Text('⭐ Featured',
                            style: TextStyle(
                                fontSize: 9, fontWeight: FontWeight.bold,),),
                      ),
                    ),
                  if (product.stock == 0)
                    Container(
                      color: Colors.black.withValues(alpha: 0.45),
                      child: const Center(
                        child: Text('Out of Stock',
                            style: TextStyle(
                                color: Colors.white,
                                fontWeight: FontWeight.bold,
                                fontSize: 12,),),
                      ),
                    ),
                ],
              ),
            ),
            // Info section
            Expanded(
              flex: 4,
              child: Padding(
                padding: const EdgeInsets.fromLTRB(10, 8, 10, 8),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      product.name,
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                      style: const TextStyle(
                        fontWeight: FontWeight.bold,
                        fontSize: 13,
                        color: AppTheme.blackAccent,
                      ),
                    ),
                    const Spacer(),
                    Row(
                      mainAxisAlignment: MainAxisAlignment.spaceBetween,
                      children: [
                        Text(
                          'PKR ${product.basePrice.toStringAsFixed(0)}',
                          style: const TextStyle(
                            fontWeight: FontWeight.w900,
                            fontSize: 14,
                            color: AppTheme.blackAccent,
                          ),
                        ),
                        // Quick add to cart
                        GestureDetector(
                          onTap: product.stock > 0
                              ? () {
                                  context.read<CartProvider>().addItem(product);
                                  ScaffoldMessenger.of(context).showSnackBar(
                                    SnackBar(
                                      content:
                                          Text('${product.name} added to cart!'),
                                      backgroundColor: Colors.green,
                                      behavior: SnackBarBehavior.floating,
                                      duration:
                                          const Duration(milliseconds: 800),
                                      shape: RoundedRectangleBorder(
                                          borderRadius:
                                              BorderRadius.circular(12),),
                                    ),
                                  );
                                }
                              : null,
                          child: Container(
                            width: 32,
                            height: 32,
                            decoration: BoxDecoration(
                              color: product.stock > 0
                                  ? AppTheme.blackAccent
                                  : Colors.grey.shade300,
                              borderRadius: BorderRadius.circular(10),
                            ),
                            child: Icon(
                              Icons.add_shopping_cart_rounded,
                              color: product.stock > 0
                                  ? AppTheme.limeAccent
                                  : Colors.grey,
                              size: 16,
                            ),
                          ),
                        ),
                      ],
                    ),
                  ],
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

// ── Persistent Tab Header Delegate ───────────────────────────────────

class _TabBarDelegate extends SliverPersistentHeaderDelegate {
  const _TabBarDelegate(this.tabBar);
  final TabBar tabBar;

  @override
  double get minExtent => tabBar.preferredSize.height;

  @override
  double get maxExtent => tabBar.preferredSize.height;

  @override
  Widget build(
      BuildContext context, double shrinkOffset, bool overlapsContent,) {
    return Container(color: Colors.white, child: tabBar);
  }

  @override
  bool shouldRebuild(_TabBarDelegate oldDelegate) => false;
}
