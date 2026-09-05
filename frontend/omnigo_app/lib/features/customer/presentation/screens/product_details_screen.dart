import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter_stripe/flutter_stripe.dart';
import 'package:geolocator/geolocator.dart';
import 'package:http/http.dart' as http;
import 'package:omnigo_app/core/network/api_client.dart';
import 'package:url_launcher/url_launcher.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:provider/provider.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/services/cart_provider.dart';

import '../../data/models/product.dart';
import '../widgets/payfast_card_sheet.dart';
import 'order_success_screen.dart';
import 'vendor_store_page.dart';

class ProductDetailsScreen extends StatefulWidget {

  const ProductDetailsScreen({
    super.key,
    required this.product,
    required this.userTrackingId,
  });
  final Product product;
  final String userTrackingId;

  @override
  ProductDetailsScreenState createState() => ProductDetailsScreenState();
}

class ProductDetailsScreenState extends State<ProductDetailsScreen> {
  bool _isCheckoutProcessing = false;
  int _quantity = 1;
  double _deliveryFee = 0.0;
  String _routingStatus = 'DYNAMIC_CALCULATED';

  // Customer GPS location for accurate delivery fee + rider dropoff
  double _customerLat = 0.0;
  double _customerLng = 0.0;
  bool _isFetchingLocation = false;
  String? _locationError;

  // ── Review state ─────────────────────────────────────────────────
  List<dynamic> _reviews = [];
  double _avgRating = 0.0;
  int _totalReviews = 0;
  bool _isLoadingReviews = false;

  // ── Multi-Image & Recommendations state ────────────────────────────
  int _activeImageIndex = 0;
  final PageController _imagePageController = PageController();
  List<Product> _recommendations = [];
  bool _isLoadingRecommendations = false;

  // ── Vendor store card state ───────────────────────────────────────
  Map<String, dynamic>? _storeInfo;
  bool _isLoadingStore = false;

  int get _maxQuantity {
    final stock = widget.product.stock;
    // BUG-18 FIX: Return 0 when out of stock to prevent purchase.
    return stock > 0 ? stock : 0;
  }

  @override
  void initState() {
    super.initState();
    _fetchReviewSummary();
    _fetchReviews();
    _fetchAIRecommendations();
    _fetchStoreInfo();
    // H4: Fetch customer GPS on screen load for accurate delivery fee
    _fetchCustomerLocation();
  }

  @override
  void dispose() {
    _imagePageController.dispose();
    super.dispose();
  }

  /// Fetches vendor store info to show the Daraz-style store card.
  Future<void> _fetchStoreInfo() async {
    final storeId = widget.product.storeTrackingId;
    if (storeId.isEmpty) return;

    // Pre-fill from product model if available (no extra network call needed)
    if (widget.product.storeName != null) {
      setState(() {
        _storeInfo = {
          'store_name': widget.product.storeName,
          'logo_url': widget.product.storeLogoUrl,
          'banner_url': widget.product.storeBannerUrl,
          'store_tracking_id': storeId,
        };
      });
      return;
    }

    setState(() => _isLoadingStore = true);
    try {
      final result = await ApiClient().get('/stores/$storeId');
      if (mounted) {
        setState(() {
          _storeInfo = result as Map<String, dynamic>;
          _isLoadingStore = false;
        });
      }
    } catch (_) {
      if (mounted) setState(() => _isLoadingStore = false);
    }
  }

  Future<void> _fetchAIRecommendations() async {
    final prodId = widget.product.productTrackingId;
    if (prodId.isEmpty) return;

    setState(() => _isLoadingRecommendations = true);
    try {
      final result = await ApiClient().get('/products/$prodId/recommendations');
      if (mounted) {
        final data = result as List<dynamic>;
        setState(() {
          _recommendations = data.map((e) => Product.fromJson(e as Map<String, dynamic>)).toList();
          _isLoadingRecommendations = false;
        });
      }
    } catch (e) {
      if (mounted) setState(() => _isLoadingRecommendations = false);
    }
  }

  // ── Review API calls ─────────────────────────────────────────────

  Future<void> _fetchReviewSummary() async {
    final prodId = widget.product.productTrackingId;
    if (prodId.isEmpty) return;

    try {
      final response = await http.get(
        Uri.parse(ApiEndpoints.reviewSummary(prodId)),
      ).timeout(const Duration(seconds: 5));

      if (response.statusCode == 200 && mounted) {
        final data = jsonDecode(response.body) as Map<String, dynamic>;
        setState(() {
          _avgRating = (data['average_rating'] as num?)?.toDouble() ?? 0.0;
          _totalReviews = (data['total_reviews'] as num?)?.toInt() ?? 0;
        });
      }
    } catch (e) {
      debugPrint('Error fetching review summary: $e');
    }
  }

  Future<void> _fetchReviews() async {
    final prodId = widget.product.productTrackingId;
    if (prodId.isEmpty) return;

    setState(() => _isLoadingReviews = true);

    try {
      final response = await http.get(
        Uri.parse(ApiEndpoints.reviewList(prodId)),
      ).timeout(const Duration(seconds: 5));

      if (response.statusCode == 200 && mounted) {
        setState(() {
          _reviews = jsonDecode(response.body) as List<dynamic>;
          _isLoadingReviews = false;
        });
      } else if (mounted) {
        setState(() => _isLoadingReviews = false);
      }
    } catch (e) {
      if (mounted) setState(() => _isLoadingReviews = false);
    }
  }

  Future<void> _submitReview(int rating, String comment) async {
    final prodId = widget.product.productTrackingId;
    if (prodId.isEmpty) return;

    try {
      final prefs = await SharedPreferences.getInstance();
      final customerTrackingId = prefs.getString('tracking_id') ?? '';

      await ApiClient().post('/reviews/', {
        'product_tracking_id': prodId,
        'customer_tracking_id': customerTrackingId,
        'rating': rating,
        'comment': comment,
      });

      if (mounted) {
        Navigator.pop(context);
        unawaited(_fetchReviewSummary());
        unawaited(_fetchReviews());
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Review submitted!'), backgroundColor: Colors.green),
        );
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Network error: $e'), backgroundColor: Colors.redAccent),
        );
      }
    }
  }

  /// Shows a payment method selector dialog. Returns the selected method
  /// string ('card', 'jazzcash', 'easypaisa', 'cash') or null if cancelled.
  Future<String?> _showPaymentMethodDialog() {
    return showDialog<String>(
      context: context,
      builder: (ctx) => SimpleDialog(
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(24)),
        title: const Text('Select Payment Method', style: TextStyle(fontWeight: FontWeight.bold)),
        children: [
          _buildPaymentOption(ctx, 'card', 'Credit / Debit Card', Icons.credit_card, Colors.blue),
          _buildPaymentOption(ctx, 'payfast', 'PayFast (PK)', Icons.payment_outlined, Colors.deepOrange),
          _buildPaymentOption(ctx, 'wallet', 'Wallet Balance', Icons.account_balance_wallet, Colors.teal),
          _buildPaymentOption(ctx, 'jazzcash', 'JazzCash', Icons.account_balance_wallet_outlined, Colors.red),
          _buildPaymentOption(ctx, 'easypaisa', 'EasyPaisa', Icons.account_balance_wallet_outlined, Colors.green),
          _buildPaymentOption(ctx, 'cash', 'Cash on Delivery', Icons.money_outlined, Colors.grey),
        ],
      ),
    );
  }

  Widget _buildPaymentOption(BuildContext ctx, String value, String label, IconData icon, Color color) {
    return SimpleDialogOption(
      onPressed: () => Navigator.pop(ctx, value),
      child: Row(
        children: [
          Icon(icon, color: color, size: 28),
          const SizedBox(width: 16),
          Text(label, style: const TextStyle(fontSize: 16, fontWeight: FontWeight.w500)),
        ],
      ),
    );
  }

  void _showReviewDialog() {
    int selectedRating = 5;
    final commentController = TextEditingController();

    showDialog<void>(
      context: context,
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, setDialogState) => AlertDialog(
          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(24)),
          title: const Text('Write a Review', style: TextStyle(fontWeight: FontWeight.bold)),
          content: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Text('Tap to rate:', style: TextStyle(color: Colors.grey, fontSize: 13)),
              const SizedBox(height: 8),
              Row(
                mainAxisAlignment: MainAxisAlignment.center,
                children: List.generate(5, (i) {
                  return IconButton(
                    icon: Icon(
                      i < selectedRating ? Icons.star_rounded : Icons.star_border_rounded,
                      color: i < selectedRating ? Colors.amber : Colors.grey,
                      size: 36,
                    ),
                    onPressed: () => setDialogState(() => selectedRating = i + 1),
                  );
                }),
              ),
              const SizedBox(height: 12),
              TextField(
                controller: commentController,
                maxLines: 3,
                decoration: InputDecoration(
                  hintText: 'Share your experience...',
                  border: OutlineInputBorder(borderRadius: BorderRadius.circular(16)),
                ),
              ),
            ],
          ),
          actions: [
            TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('Cancel')),
            ElevatedButton(
              onPressed: () => _submitReview(selectedRating, commentController.text.trim()),
              style: ElevatedButton.styleFrom(backgroundColor: AppTheme.blackAccent, foregroundColor: AppTheme.limeAccent),
              child: const Text('Submit'),
            ),
          ],
        ),
      ),
    ).then((_) => commentController.dispose());
  }

  // H4: Fetch customer GPS with retry guards — accurate location for delivery fee + rider dropoff
  Future<void> _fetchCustomerLocation() async {
    setState(() {
      _isFetchingLocation = true;
      _locationError = null;
    });

    const maxRetries = 3;
    double? lastLat;
    double? lastLng;

    for (int attempt = 0; attempt < maxRetries; attempt++) {
      try {
        final bool serviceEnabled = await Geolocator.isLocationServiceEnabled();
        if (!serviceEnabled) {
          setState(() {
            _locationError = 'Location services are disabled. Please enable GPS.';
            _isFetchingLocation = false;
          });
          return;
        }

        LocationPermission permission = await Geolocator.checkPermission();
        if (permission == LocationPermission.denied) {
          permission = await Geolocator.requestPermission();
          if (permission == LocationPermission.denied) {
            if (attempt < maxRetries - 1) {
              await Future<void>.delayed(Duration(seconds: attempt + 1));
              continue;
            }
            setState(() {
              _locationError = 'Location permission denied. Please enable in Settings.';
              _isFetchingLocation = false;
            });
            return;
          }
        }

        if (permission == LocationPermission.deniedForever) {
          setState(() {
            _locationError = 'Location permanently denied. Enable in Settings.';
            _isFetchingLocation = false;
          });
          return;
        }

        final position = await Geolocator.getCurrentPosition(
          locationSettings: const LocationSettings(accuracy: LocationAccuracy.high),
        ).timeout(const Duration(seconds: 10));

        lastLat = position.latitude;
        lastLng = position.longitude;

        setState(() {
          _customerLat = position.latitude;
          _customerLng = position.longitude;
          _isFetchingLocation = false;
          _locationError = null;
        });

        // H4: Estimate delivery fee with actual customer location
        await _estimateDeliveryFee(_customerLat, _customerLng);
        return;
      } catch (e) {
        if (attempt < maxRetries - 1) {
          await Future<void>.delayed(Duration(seconds: attempt + 1));
          continue;
        }
        // Last known good position? Use it but flag as fallback
        if (lastLat != null && lastLng != null) {
          setState(() {
            _customerLat = lastLat!;
            _customerLng = lastLng!;
            _isFetchingLocation = false;
            _locationError = 'Using last known location. GPS accuracy may be low.';
          });
          await _estimateDeliveryFee(_customerLat, _customerLng);
        } else {
          setState(() {
            _locationError = 'Could not get location. Please enable GPS.';
            _isFetchingLocation = false;
          });
        }
      }
    }
  }

  // H4/H3: Estimate delivery fee — uses ACTUAL customer GPS location
  Future<void> _estimateDeliveryFee(double dropoffLat, double dropoffLng) async {
    final storeId = widget.product.storeTrackingId;
    if (storeId.isEmpty) return;
    try {
      final resp = await ApiClient().post(
        ApiEndpoints.deliveryEstimateFee(),
        {
          'vendor_store_tracking_id': storeId,
          'dropoff_lat': dropoffLat,
          'dropoff_lng': dropoffLng,
        },
      );
      if (resp != null) {
        final fee = (resp['delivery_fee'] is num) ? (resp['delivery_fee'] as num).toDouble() : 0.0;
        final routing = (resp['routing_status'] as String?) ?? 'DYNAMIC_CALCULATED';
        setState(() {
          _deliveryFee = fee;
          _routingStatus = routing;
        });
      }
    } catch (e) {
      setState(() {
        _deliveryFee = 0.0;
        _routingStatus = 'FAILED_CALCULATION';
      });
    }
  }

  // C-4 FIX: Cancel order on payment failure to release stock
  Future<void> _cancelOrderOnFailure(String orderId, String reason) async {
    try {
      await ApiClient().post('/orders/$orderId/cancel', {'reason': reason});
    } catch (_) {}
  }

  Future<void> _executeCheckout() async {
    if (_isCheckoutProcessing) return;

    // ── Payment method selector ─────────────────────────────────────
    final paymentChoice = await _showPaymentMethodDialog();
    if (paymentChoice == null) return; // user cancelled

    setState(() {
      _isCheckoutProcessing = true;
    });

    // H4: Use actual customer GPS — retry if not fetched yet, block if unavailable
    double dropoffLat = _customerLat;
    double dropoffLng = _customerLng;

    if (dropoffLat == 0 || dropoffLng == 0) {
      // Location not fetched yet — retry now with user notification
      await _fetchCustomerLocation();
      dropoffLat = _customerLat;
      dropoffLng = _customerLng;
    }

    // Final guard: if still no location, block checkout
    if (dropoffLat == 0 || dropoffLng == 0) {
      setState(() => _isCheckoutProcessing = false);
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
            content: Text('Location required for delivery. Please enable GPS.'),
            backgroundColor: Colors.red,
            behavior: SnackBarBehavior.floating,
          ),
        );
      }
      return;
    }

    final String nonce = '${widget.product.productTrackingId}_${DateTime.now().millisecondsSinceEpoch}';
    final prefs = await SharedPreferences.getInstance();
    await prefs.setString('pending_nonce', nonce);
    await prefs.setString('pending_order_product', widget.product.name.isNotEmpty ? widget.product.name : 'Unknown');
    await prefs.setString('pending_order_status', 'PENDING_CONFIRMATION');

    try {
      final vendorStoreId = widget.product.storeTrackingId;
      final unitPrice = widget.product.basePrice;
      final totalPrice = unitPrice * _quantity;
      final prodId = widget.product.productTrackingId.isNotEmpty ? widget.product.productTrackingId : 'PROD-N/A';

      final reqItems = [{
        'product_tracking_id': prodId,
        'quantity': _quantity,
      }];

      // Step 1: Place the order (ApiClient throws on error)
      final orderData = await ApiClient().post('/orders/', {
        'user_tracking_id': widget.userTrackingId,
        'vendor_store_tracking_id': vendorStoreId,
        'items': reqItems,
        'total_amount': totalPrice,
        'currency': 'PKR',
        'payment_gateway': paymentChoice == 'cash' ? 'cod' : paymentChoice,
        'device_session_nonce': nonce,
        'dropoff_lat': dropoffLat,
        'dropoff_lng': dropoffLng,
        // H4: Uber-style — customer pays product + delivery fee
        'delivery_fee_paisa': (_deliveryFee * 100).round(),
        // H3: routing audit trail
        'routing_status': _routingStatus,
      }) as Map<String, dynamic>;

      final realOrderTrackingId =
          (orderData['order_tracking_id'] ?? orderData['tracking_id'])?.toString();
      if (realOrderTrackingId == null || realOrderTrackingId.isEmpty) {
        if (mounted) _showErrorDialog('Order created but no tracking id returned.');
        return;
      }

      // ── Step 2: Branch on payment method using the REAL order id.
      if (paymentChoice == 'card') {
        try {
          final checkoutData = await ApiClient().post('/payment/checkout', {
            'gateway': 'stripe',
            'customer_id': widget.userTrackingId,
            'store_id': vendorStoreId,
            'order_id': realOrderTrackingId,
            'amount': totalPrice,
            'currency': 'PKR',
          }) as Map<String, dynamic>;

          final clientSecret = checkoutData['client_secret']?.toString();
          if (clientSecret != null && clientSecret.isNotEmpty) {
            try {
              await Stripe.instance.initPaymentSheet(
                paymentSheetParameters: SetupPaymentSheetParameters(
                  paymentIntentClientSecret: clientSecret,
                  merchantDisplayName: 'OMNIGO Super App',
                  allowsDelayedPaymentMethods: true,
                ),
              );
              await Stripe.instance.presentPaymentSheet();
              if (mounted) {
                await prefs.remove('pending_nonce');
                await prefs.remove('pending_order_product');
                await prefs.remove('pending_order_status');
                await Navigator.pushAndRemoveUntil(
                  context,
                  MaterialPageRoute<void>(
                    builder: (_) => OrderSuccessScreen(
                      trackingId: realOrderTrackingId,
                      pending: true,
                    ),
                  ),
                  (route) => route.isFirst,
                );
              }
              return;
            } catch (e) {
              if (mounted) {
                setState(() => _isCheckoutProcessing = false);
                _showErrorDialog('Payment was cancelled or failed: $e');
              }
              await _cancelOrderOnFailure(realOrderTrackingId, 'Stripe payment failed: $e');
              await prefs.remove('pending_nonce');
              await prefs.remove('pending_order_product');
              await prefs.remove('pending_order_status');
              return;
            }
          }
        } catch (e) {
          if (mounted) setState(() => _isCheckoutProcessing = false);
          final msg = e.toString();
          if (msg.contains('409')) {
            if (mounted) {
              await prefs.remove('pending_nonce');
              await prefs.remove('pending_order_product');
              await prefs.remove('pending_order_status');
              await Navigator.pushAndRemoveUntil(
                context,
                MaterialPageRoute<void>(
                  builder: (_) => OrderSuccessScreen(
                    trackingId: realOrderTrackingId,
                    pending: false,
                  ),
                ),
                (route) => route.isFirst,
              );
              return;
            }
          }
          if (mounted) _showErrorDialog('Stripe checkout failed. Please try again.');
          await _cancelOrderOnFailure(realOrderTrackingId, 'Stripe checkout failed');
          await prefs.remove('pending_nonce');
          await prefs.remove('pending_order_product');
          await prefs.remove('pending_order_status');
          return;
        }
      } else if (paymentChoice == 'wallet') {
        try {
          final wcData = await ApiClient().post('/payment/checkout', {
            'gateway': 'wallet',
            'customer_id': widget.userTrackingId,
            'order_id': realOrderTrackingId,
            'amount': totalPrice,
            'currency': 'PKR',
          }, idempotencyKey: 'buynow-$nonce') as Map<String, dynamic>;

          final wcError = wcData['error']?.toString();
          if (wcError != null && wcError.isNotEmpty) {
            if (mounted) setState(() => _isCheckoutProcessing = false);
            if (mounted) _showErrorDialog(wcError);
            await prefs.remove('pending_nonce');
            await prefs.remove('pending_order_product');
            await prefs.remove('pending_order_status');
            return;
          }
        } catch (e) {
          if (mounted) setState(() => _isCheckoutProcessing = false);
          final msg = e.toString();
          if (msg.contains('409')) {
            if (mounted) {
              await prefs.remove('pending_nonce');
              await prefs.remove('pending_order_product');
              await prefs.remove('pending_order_status');
              await Navigator.pushAndRemoveUntil(
                context,
                MaterialPageRoute<void>(
                  builder: (_) => OrderSuccessScreen(
                    trackingId: realOrderTrackingId,
                    pending: false,
                  ),
                ),
                (route) => route.isFirst,
              );
              return;
            }
          }
          if (mounted) _showErrorDialog('Wallet payment failed. Please try again.');
          await _cancelOrderOnFailure(realOrderTrackingId, 'Wallet payment failed');
          await prefs.remove('pending_nonce');
          await prefs.remove('pending_order_product');
          await prefs.remove('pending_order_status');
          return;
        }
      } else if (paymentChoice == 'jazzcash' || paymentChoice == 'easypaisa') {
        try {
          final walletData = await ApiClient().post('/wallet/charge', {
            'customer_id': widget.userTrackingId,
            'store_id': vendorStoreId,
            'gateway': paymentChoice,
            'order_id': realOrderTrackingId,
            'amount_cents': (totalPrice * 100).round(),
            'nonce': nonce,
          }) as Map<String, dynamic>;

          final redirectUrl = walletData['redirect_url']?.toString() ?? '';
          if (redirectUrl.isNotEmpty && mounted) {
            final uri = Uri.parse(redirectUrl);
            if (await canLaunchUrl(uri)) {
              await launchUrl(uri, mode: LaunchMode.externalApplication);
            }
          }
          if (mounted) {
            await prefs.remove('pending_nonce');
            await prefs.remove('pending_order_product');
            await prefs.remove('pending_order_status');
            await Navigator.pushAndRemoveUntil(
              context,
              MaterialPageRoute<void>(
                builder: (_) => OrderSuccessScreen(
                  trackingId: realOrderTrackingId,
                  pending: true,
                ),
              ),
              (route) => route.isFirst,
            );
          }
          return;
        } catch (e) {
          if (mounted) setState(() => _isCheckoutProcessing = false);
          final msg = e.toString();
          if (msg.contains('409')) {
            if (mounted) {
              await prefs.remove('pending_nonce');
              await prefs.remove('pending_order_product');
              await prefs.remove('pending_order_status');
              await Navigator.pushAndRemoveUntil(
                context,
                MaterialPageRoute<void>(
                  builder: (_) => OrderSuccessScreen(
                    trackingId: realOrderTrackingId,
                    pending: false,
                  ),
                ),
                (route) => route.isFirst,
              );
              return;
            }
          }
          if (mounted) _showErrorDialog('Failed to initiate wallet payment. Please try again.');
          await _cancelOrderOnFailure(realOrderTrackingId, 'JazzCash/EasyPaisa init failed');
          await prefs.remove('pending_nonce');
          await prefs.remove('pending_order_product');
          await prefs.remove('pending_order_status');
          return;
        }
      } else if (paymentChoice == 'payfast') {
        if (!mounted) return;
        final cardDetails = await showPayFastCardDetailsSheet(context);
        if (cardDetails == null) {
          if (mounted) setState(() => _isCheckoutProcessing = false);
          await prefs.remove('pending_nonce');
          return;
        }

        Map<String, dynamic> pfData;
        try {
          pfData = await ApiClient().post('/payments/payfast/payment', {
            'order_id': realOrderTrackingId,
            'card_number': cardDetails['card_number'],
            'expiry_month': cardDetails['expiry_month'],
            'expiry_year': cardDetails['expiry_year'],
            'cvv': cardDetails['cvv'],
            'customer_mobile_no': cardDetails['customer_mobile_no'],
          }, idempotencyKey: 'buynow-$nonce') as Map<String, dynamic>;
        } catch (e) {
          if (mounted) setState(() => _isCheckoutProcessing = false);
          final msg = e.toString();
          if (msg.contains('409')) {
            await prefs.remove('pending_nonce');
            await prefs.remove('pending_order_product');
            await prefs.remove('pending_order_status');
            if (mounted) {
              await Navigator.pushAndRemoveUntil(
                context,
                MaterialPageRoute<void>(
                  builder: (_) => OrderSuccessScreen(
                    trackingId: realOrderTrackingId,
                    pending: false,
                  ),
                ),
                (route) => route.isFirst,
              );
              return;
            }
          }
          if (mounted) _showErrorDialog('Failed to initiate PayFast payment. Please try again.');
          await _cancelOrderOnFailure(realOrderTrackingId, 'PayFast init failed');
          return;
        }

        final status = pfData['status']?.toString();
        if (status == 'failed') {
          if (mounted) setState(() => _isCheckoutProcessing = false);
          if (mounted) _showErrorDialog(pfData['message']?.toString() ?? 'PayFast payment failed');
          await _cancelOrderOnFailure(realOrderTrackingId, 'PayFast payment failed: ${pfData['message']}');
          return;
        }

        if (status == 'hosted_redirect') {
          final redirectUrl = pfData['redirect_url']?.toString() ?? '';
          if (redirectUrl.isNotEmpty && mounted) {
            final uri = Uri.parse(redirectUrl);
            if (await canLaunchUrl(uri)) {
              await launchUrl(uri, mode: LaunchMode.externalApplication);
            }
            if (mounted) {
              ScaffoldMessenger.of(context).showSnackBar(
                const SnackBar(
                  content: Text('Payment page opened. Your order will update after payment.'),
                  duration: Duration(seconds: 5),
                ),
              );
            }
            if (mounted) {
              await prefs.remove('pending_nonce');
              await prefs.remove('pending_order_product');
              await prefs.remove('pending_order_status');
              await Navigator.pushAndRemoveUntil(
                context,
                MaterialPageRoute<void>(
                  builder: (_) => OrderSuccessScreen(
                    trackingId: realOrderTrackingId,
                    pending: true,
                  ),
                ),
                (route) => route.isFirst,
              );
            }
            return;
          } else {
            if (mounted) setState(() => _isCheckoutProcessing = false);
            if (mounted) _showErrorDialog('Payment gateway returned no redirect URL');
            return;
          }
        }

        if (status == '3ds_redirect' || pfData['action'] == '3ds_redirect') {
          final threedHtml = pfData['threed_html']?.toString() ?? '';
          if (threedHtml.isNotEmpty && mounted) {
            final verified = await showPayFast3DSChallenge(context, threedHtml);
            if (!verified) {
              try {
                await ApiClient().post('/orders/$realOrderTrackingId/cancel', {});
                debugPrint('3DS cancel: backend cancel succeeded');
              } catch (e) {
                debugPrint('3DS cancel: backend cancel failed: $e');
              }
              if (mounted) setState(() => _isCheckoutProcessing = false);
              if (mounted) {
                _showErrorDialog('3DS verification was cancelled. Your order has been cancelled.');
              }
              return;
            }
          }
        }

        if (status == 'gateway_pending' || status == 'in_progress') {
          if (mounted) {
            ScaffoldMessenger.of(context).showSnackBar(
              const SnackBar(
                content: Text('Payment is processing at the gateway. Your order will update automatically once it completes.'),
                duration: Duration(seconds: 5),
              ),
            );
          }
        }
      } else {
        // Cash on Delivery — no payment gateway call needed
      }

      // ── Step 3: success — same landing screen as cart checkout for a
      //    consistent post-payment experience across entry points.
      await prefs.remove('pending_nonce');
      await prefs.remove('pending_order_product');
      await prefs.remove('pending_order_status');
      if (mounted) {
        await Navigator.pushAndRemoveUntil(
          context,
          MaterialPageRoute<void>(builder: (_) => OrderSuccessScreen(trackingId: realOrderTrackingId)),
          (route) => route.isFirst,
        );
      }
    } catch (e) {
      if (mounted) _showErrorDialog('Network Error: Could not reach the server.');
    } finally {
      if (mounted) {
        setState(() {
          _isCheckoutProcessing = false;
        });
      }
    }
  }

  void _showErrorDialog(String message) {
    showDialog<void>(
      context: context,
      builder: (context) => AlertDialog(
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(20)),
        title: const Text('Checkout Failed', style: TextStyle(color: Colors.red)),
        content: Text(message),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('Close'),
          ),
        ],
      ),
    );
  }

  // B7 FIX: _showPaymentPendingDialog removed — no longer called after B1 fix
  // (PayFast/JazzCash/EasyPaisa now navigate to OrderSuccessScreen with
  // pending=true instead of showing a dialog and returning to product page).

  @override
  Widget build(BuildContext context) {
    final prod = widget.product;
    final String name = prod.name.isNotEmpty ? prod.name : 'Unknown Product';
    final String description = prod.description.isNotEmpty ? prod.description : 'No description available for this product.';
    final double unitPrice = prod.basePrice;
    final double totalPrice = unitPrice * _quantity;
    final String prodId = prod.productTrackingId.isNotEmpty ? prod.productTrackingId : 'PROD-N/A';
    final String storeId = prod.storeTrackingId.isNotEmpty ? prod.storeTrackingId : 'STOR-N/A';

    return Scaffold(
      backgroundColor: const Color(0xFFF8F9FA),
      extendBodyBehindAppBar: true,
      appBar: AppBar(
        backgroundColor: Colors.transparent,
        elevation: 0,
        iconTheme: const IconThemeData(color: AppTheme.blackAccent),
      ),
      body: Stack(
        children: [
          // Background Gradient with Multi-Image PageView Carousel
          Container(
            height: MediaQuery.of(context).size.height * 0.45,
            decoration: const BoxDecoration(
              gradient: LinearGradient(
                colors: [AppTheme.softPink, AppTheme.softBlue],
                begin: Alignment.topLeft,
                end: Alignment.bottomRight,
              ),
            ),
            child: Stack(
              alignment: Alignment.bottomCenter,
              children: [
                Center(
                  child: Hero(
                    tag: prodId,
                    child: prod.allImages.isNotEmpty
                        ? PageView.builder(
                            controller: _imagePageController,
                            itemCount: prod.allImages.length,
                            onPageChanged: (idx) => setState(() => _activeImageIndex = idx),
                            itemBuilder: (context, idx) {
                              return Center(
                                child: ClipRRect(
                                  borderRadius: BorderRadius.circular(24),
                                  child: Image.network(
                                    prod.allImages[idx],
                                    fit: BoxFit.cover,
                                    width: 220,
                                    height: 220,
                                    errorBuilder: (context, error, stackTrace) =>
                                        Icon(Icons.shopping_bag, size: 120, color: Colors.white.withOpacity(0.8)),
                                  ),
                                ),
                              );
                            },
                          )
                        : Icon(Icons.shopping_bag, size: 120, color: Colors.white.withOpacity(0.8)),
                  ),
                ),
                if (prod.allImages.length > 1)
                  Positioned(
                    bottom: 70,
                    child: Row(
                      mainAxisAlignment: MainAxisAlignment.center,
                      children: List.generate(
                        prod.allImages.length,
                        (index) => Container(
                          margin: const EdgeInsets.symmetric(horizontal: 4),
                          width: _activeImageIndex == index ? 20 : 8,
                          height: 8,
                          decoration: BoxDecoration(
                            color: _activeImageIndex == index ? AppTheme.blackAccent : Colors.white.withOpacity(0.7),
                            borderRadius: BorderRadius.circular(4),
                          ),
                        ),
                      ),
                    ),
                  ),
              ],
            ),
          ),

          // Content Panel
          Align(
            alignment: Alignment.bottomCenter,
            child: Container(
              height: MediaQuery.of(context).size.height * 0.65,
              padding: const EdgeInsets.all(24),
              decoration: const BoxDecoration(
                color: Colors.white,
                borderRadius: BorderRadius.only(
                  topLeft: Radius.circular(40),
                  topRight: Radius.circular(40),
                ),
                boxShadow: [
                  BoxShadow(
                    color: Colors.black12,
                    blurRadius: 20,
                    offset: Offset(0, -5),
                  ),
                ],
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  // Title and Price
                  Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Expanded(
                        child: Text(
                          name,
                          style: const TextStyle(
                            fontSize: 28,
                            fontWeight: FontWeight.bold,
                            color: AppTheme.blackAccent,
                          ),
                        ),
                      ),
                      Text(
                        'PKR ${totalPrice.toStringAsFixed(0)}',
                        style: const TextStyle(
                          fontSize: 24,
                          fontWeight: FontWeight.w900,
                          color: AppTheme.blackAccent,
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 12),

                  // H4: Show estimated delivery fee with location status
                  if (_locationError != null && _locationError!.isNotEmpty)
                    Container(
                      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
                      decoration: BoxDecoration(
                        color: Colors.orange.shade50,
                        borderRadius: BorderRadius.circular(8),
                        border: Border.all(color: Colors.orange.shade300),
                      ),
                      child: Row(
                        children: [
                          Icon(Icons.location_off, size: 16, color: Colors.orange.shade700),
                          const SizedBox(width: 6),
                          Expanded(
                            child: Text(
                              _locationError!,
                              style: TextStyle(fontSize: 12, color: Colors.orange.shade700),
                            ),
                          ),
                          GestureDetector(
                            onTap: _isFetchingLocation ? null : _fetchCustomerLocation,
                            child: Text(
                              'Retry',
                              style: TextStyle(fontSize: 12, fontWeight: FontWeight.bold, color: Colors.orange.shade700),
                            ),
                          ),
                        ],
                      ),
                    ),

                  // H4: Delivery fee estimate (shown once location is fetched)
                  if (_customerLat != 0 && _customerLng != 0 && _deliveryFee > 0)
                    Padding(
                      padding: const EdgeInsets.only(top: 8),
                      child: Row(
                        children: [
                          Icon(Icons.local_shipping, size: 16, color: Colors.green.shade700),
                          const SizedBox(width: 4),
                          Text(
                            'Delivery: PKR ${_deliveryFee.toStringAsFixed(0)}',
                            style: TextStyle(fontSize: 13, fontWeight: FontWeight.w600, color: Colors.green.shade700),
                          ),
                          if (_routingStatus == 'FALLBACK_HAVERSINE')
                            Text(
                              ' (approx)',
                              style: TextStyle(fontSize: 11, color: Colors.orange.shade700),
                            ),
                        ],
                      ),
                    ),

                  // Product ID chip
                  _buildChip(Icons.qr_code, prodId, AppTheme.softBlue),
                  const SizedBox(height: 16),

                  // ── Daraz-style Vendor Store Card ─────────────────
                  _buildStoreCard(storeId),
                  const SizedBox(height: 24),

                  const Text(
                    'Description',
                    style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold),
                  ),
                  const SizedBox(height: 8),
                  Expanded(
                    child: SingleChildScrollView(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            description,
                            style: const TextStyle(fontSize: 15, color: Colors.black54, height: 1.5),
                          ),
                          const SizedBox(height: 24),

                          // ── AI Recommendations Section ────────────────
                          if (_isLoadingRecommendations)
                            const Center(child: Padding(padding: EdgeInsets.all(12), child: CircularProgressIndicator(strokeWidth: 2)))
                          else if (_recommendations.isNotEmpty) ...[
                            const Row(
                              children: [
                                Icon(Icons.auto_awesome, color: Colors.amber, size: 18),
                                SizedBox(width: 6),
                                Text(
                                  'Customers Who Bought This Also Bought',
                                  style: TextStyle(fontSize: 16, fontWeight: FontWeight.bold, color: AppTheme.blackAccent),
                                ),
                              ],
                            ),
                            const SizedBox(height: 12),
                            SizedBox(
                              height: 150,
                              child: ListView.builder(
                                scrollDirection: Axis.horizontal,
                                itemCount: _recommendations.length,
                                itemBuilder: (context, idx) {
                                  final rec = _recommendations[idx];
                                  return GestureDetector(
                                    onTap: () {
                                      // #58: Use push instead of pushReplacement so user can go back
                                      Navigator.push(
                                        context,
                                        MaterialPageRoute<void>(
                                          builder: (context) => ProductDetailsScreen(
                                            product: rec,
                                            userTrackingId: widget.userTrackingId,
                                          ),
                                        ),
                                      );
                                    },
                                    child: Container(
                                      width: 120,
                                      margin: const EdgeInsets.only(right: 12),
                                      padding: const EdgeInsets.all(8),
                                      decoration: BoxDecoration(
                                        color: Colors.grey.shade50,
                                        borderRadius: BorderRadius.circular(16),
                                        border: Border.all(color: Colors.grey.shade200),
                                      ),
                                      child: Column(
                                        crossAxisAlignment: CrossAxisAlignment.start,
                                        children: [
                                          Expanded(
                                            child: ClipRRect(
                                              borderRadius: BorderRadius.circular(12),
                                              child: (rec.imageUrl != null && rec.imageUrl!.isNotEmpty)
                                                  ? Image.network(
                                                      rec.imageUrl!,
                                                      fit: BoxFit.cover,
                                                      width: double.infinity,
                                                      errorBuilder: (context, error, stackTrace) =>
                                                          const Center(child: Icon(Icons.shopping_bag_outlined, color: Colors.grey)),
                                                    )
                                                  : const Center(child: Icon(Icons.shopping_bag_outlined, color: Colors.grey)),
                                            ),
                                          ),
                                          const SizedBox(height: 6),
                                          Text(
                                            rec.name,
                                            maxLines: 1,
                                            overflow: TextOverflow.ellipsis,
                                            style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 12),
                                          ),
                                          Text(
                                            'PKR ${rec.basePrice.toStringAsFixed(0)}',
                                            style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 11, color: Colors.blue),
                                          ),
                                        ],
                                      ),
                                    ),
                                  );
                                },
                              ),
                            ),
                            const SizedBox(height: 24),
                          ],

                          // ── Reviews Section ─────────────────────────
                          Row(
                            mainAxisAlignment: MainAxisAlignment.spaceBetween,
                            children: [
                              Row(
                                children: [
                                  const Text('Reviews',
                                      style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold),),
                                  const SizedBox(width: 8),
                                  if (_totalReviews > 0)
                                    Row(
                                      children: [
                                        const Icon(Icons.star_rounded, color: Colors.amber, size: 18),
                                        const SizedBox(width: 2),
                                        Text('${_avgRating.toStringAsFixed(1)} ($_totalReviews)',
                                            style: const TextStyle(color: Colors.grey, fontSize: 13),),
                                      ],
                                    ),
                                ],
                              ),
                              TextButton.icon(
                                onPressed: _showReviewDialog,
                                icon: const Icon(Icons.edit_outlined, size: 16),
                                label: const Text('Write', style: TextStyle(fontSize: 13)),
                              ),
                            ],
                          ),
                          const SizedBox(height: 8),
                          if (_isLoadingReviews)
                            const Center(child: Padding(
                              padding: EdgeInsets.all(16),
                              child: CircularProgressIndicator(strokeWidth: 2),
                            ),)
                          else if (_reviews.isEmpty)
                            const Padding(
                              padding: EdgeInsets.symmetric(vertical: 12),
                              child: Text('No reviews yet. Be the first!', style: TextStyle(color: Colors.grey, fontSize: 13)),
                            )
                          else
                            ..._reviews.take(3).map((r) => _buildReviewItem(r)),
                        ],
                      ),
                    ),
                  ),

                  // Quantity Selector + Checkout Buttons
                  const SizedBox(height: 16),

                  // Quantity Stepper - disabled during checkout processing
                  Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    children: [
                      const Text(
                        'Quantity',
                        style: TextStyle(fontSize: 16, fontWeight: FontWeight.bold, color: AppTheme.blackAccent),
                      ),
                      Container(
                        decoration: BoxDecoration(
                          border: Border.all(color: _isCheckoutProcessing ? Colors.grey.shade200 : Colors.grey.shade300),
                          borderRadius: BorderRadius.circular(16),
                          color: _isCheckoutProcessing ? Colors.grey.shade100 : Colors.white,
                        ),
                        child: Row(
                          children: [
                            IconButton(
                              icon: Icon(
                                Icons.remove_rounded,
                                color: _isCheckoutProcessing ? Colors.grey : AppTheme.blackAccent,
                              ),
                              onPressed: _isCheckoutProcessing || _quantity <= 1
                                  ? null
                                  : () => setState(() => _quantity--),
                            ),
                            Text(
                              '$_quantity',
                              style: TextStyle(
                                fontSize: 18,
                                fontWeight: FontWeight.bold,
                                color: _isCheckoutProcessing ? Colors.grey : AppTheme.blackAccent,
                              ),
                            ),
                            IconButton(
                              icon: Icon(
                                Icons.add_rounded,
                                color: _isCheckoutProcessing ? Colors.grey : AppTheme.blackAccent,
                              ),
                              onPressed: _isCheckoutProcessing || _quantity >= _maxQuantity
                                  ? null
                                  : () => setState(() => _quantity++),
                            ),
                          ],
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 16),

                  // H4: Total price + delivery fee summary before checkout
                  if (_customerLat != 0 && _customerLng != 0 && _deliveryFee > 0)
                    Container(
                      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
                      decoration: BoxDecoration(
                        color: Colors.green.shade50,
                        borderRadius: BorderRadius.circular(10),
                        border: Border.all(color: Colors.green.shade200),
                      ),
                      child: Row(
                        mainAxisAlignment: MainAxisAlignment.spaceBetween,
                        children: [
                          Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              Text(
                                'Product: PKR ${(widget.product.basePrice * _quantity).toStringAsFixed(0)}',
                                style: TextStyle(fontSize: 13, color: Colors.grey.shade700),
                              ),
                              Text(
                                'Delivery: PKR ${_deliveryFee.toStringAsFixed(0)}',
                                style: TextStyle(fontSize: 13, color: Colors.grey.shade700),
                              ),
                            ],
                          ),
                          Column(
                            crossAxisAlignment: CrossAxisAlignment.end,
                            children: [
                              const Text(
                                'Total',
                                style: TextStyle(fontSize: 11, color: Colors.grey),
                              ),
                              Text(
                                'PKR ${((widget.product.basePrice * _quantity) + _deliveryFee).toStringAsFixed(0)}',
                                style: TextStyle(
                                  fontSize: 17,
                                  fontWeight: FontWeight.bold,
                                  color: Colors.green.shade700,
                                ),
                              ),
                            ],
                          ),
                        ],
                      ),
                    ),

                  const SizedBox(height: 12),

                  Row(
                    children: [
                      Container(
                        height: 55,
                        width: 60,
                        margin: const EdgeInsets.only(right: 12),
                        decoration: BoxDecoration(
                          border: Border.all(color: Colors.grey.shade300),
                          borderRadius: BorderRadius.circular(16),
                        ),
                        child: IconButton(
                          icon: const Icon(Icons.add_shopping_cart, color: AppTheme.blackAccent),
                          onPressed: () {
                            context.read<CartProvider>().addItem(prod, quantity: _quantity);
                            ScaffoldMessenger.of(context).showSnackBar(
                              SnackBar(
                                content: Text('Added $_quantity x $name to cart!'),
                                backgroundColor: Colors.green,
                                behavior: SnackBarBehavior.floating,
                                duration: const Duration(milliseconds: 800),
                              ),
                            );
                          },
                        ),
                      ),
                      Expanded(
                        child: SizedBox(
                          height: 55,
                          child: ElevatedButton(
                            onPressed: _isCheckoutProcessing ? null : _executeCheckout,
                            style: ElevatedButton.styleFrom(
                              backgroundColor: AppTheme.blackAccent,
                              shape: RoundedRectangleBorder(
                                borderRadius: BorderRadius.circular(16),
                              ),
                              elevation: 5,
                            ),
                            child: _isCheckoutProcessing
                                ? const CircularProgressIndicator(color: Colors.white)
                                : const Text(
                                    'Buy Now',
                                    style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold, color: Colors.white),
                                  ),
                          ),
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 20),
                ],
              ),
            ),
          ),
        ],
      ),
    );
  }

  Widget _buildReviewItem(dynamic r) {
    final rating = (r['rating'] ?? 0) as int;
    final comment = (r['comment'] ?? '').toString();
    final customer = (r['customer_tracking_id'] ?? 'CUST').toString();
    final shortCustomer = customer.length > 10 ? '${customer.substring(0, 10)}...' : customer;

    return Container(
      margin: const EdgeInsets.only(bottom: 12),
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: Colors.grey.shade50,
        borderRadius: BorderRadius.circular(16),
        border: Border.all(color: Colors.grey.shade100),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text(shortCustomer, style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 12, color: AppTheme.blackAccent)),
              const Spacer(),
              ...List.generate(5, (i) => Icon(
                i < rating ? Icons.star_rounded : Icons.star_border_rounded,
                color: i < rating ? Colors.amber : Colors.grey.shade300,
                size: 14,
              ),),
            ],
          ),
          if (comment.isNotEmpty) ...[
            const SizedBox(height: 6),
            Text(comment, style: const TextStyle(fontSize: 13, color: Colors.black54)),
          ],
        ],
      ),
    );
  }

  Widget _buildChip(IconData icon, String label, Color bgColor) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
      decoration: BoxDecoration(
        color: bgColor.withOpacity(0.2),
        borderRadius: BorderRadius.circular(20),
        border: Border.all(color: bgColor.withOpacity(0.5)),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(icon, size: 14, color: AppTheme.blackAccent),
          const SizedBox(width: 4),
          Text(
            label,
            style: const TextStyle(fontSize: 12, fontWeight: FontWeight.w600, color: AppTheme.blackAccent),
          ),
        ],
      ),
    );
  }

  /// Daraz-style vendor store card — logo, name, verified badge, Visit Store button.
  Widget _buildStoreCard(String storeId) {
    if (_isLoadingStore) {
      return Container(
        height: 72,
        decoration: BoxDecoration(
          color: Colors.grey.shade50,
          borderRadius: BorderRadius.circular(18),
          border: Border.all(color: Colors.grey.shade200),
        ),
        child: const Center(
          child: SizedBox(
            width: 24, height: 24,
            child: CircularProgressIndicator(strokeWidth: 2, color: Colors.grey),
          ),
        ),
      );
    }

    final storeName = (_storeInfo?['store_name'] as String?) ??
        widget.product.storeName ??
        storeId;
    final logoUrl = (_storeInfo?['logo_url'] as String?) ??
        widget.product.storeLogoUrl;
    final bannerUrl = (_storeInfo?['banner_url'] as String?) ??
        widget.product.storeBannerUrl;

    return GestureDetector(
      onTap: () => Navigator.push(
        context,
        MaterialPageRoute<void>(
          builder: (_) => VendorStorePage(
            storeTrackingId: storeId,
            userTrackingId: widget.userTrackingId,
            initialStoreName: storeName,
            initialLogoUrl: logoUrl,
            initialBannerUrl: bannerUrl,
          ),
        ),
      ),
      child: Container(
        padding: const EdgeInsets.all(12),
        decoration: BoxDecoration(
          color: Colors.grey.shade50,
          borderRadius: BorderRadius.circular(18),
          border: Border.all(color: Colors.grey.shade200),
        ),
        child: Row(
          children: [
            // Store logo / avatar
            CircleAvatar(
              radius: 24,
              backgroundColor: Colors.grey.shade200,
              backgroundImage: (logoUrl != null && logoUrl.isNotEmpty)
                  ? NetworkImage(logoUrl) as ImageProvider
                  : null,
              child: (logoUrl == null || logoUrl.isEmpty)
                  ? Text(
                      storeName.isNotEmpty ? storeName[0].toUpperCase() : 'S',
                      style: const TextStyle(
                        fontWeight: FontWeight.bold,
                        fontSize: 20,
                        color: AppTheme.blackAccent,
                      ),
                    )
                  : null,
            ),
            const SizedBox(width: 12),
            // Name + badge
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    storeName,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: const TextStyle(
                      fontWeight: FontWeight.bold,
                      fontSize: 14,
                      color: AppTheme.blackAccent,
                    ),
                  ),
                  const SizedBox(height: 4),
                  Row(
                    children: [
                      Icon(Icons.verified_outlined,
                          size: 12, color: Colors.green.shade600,),
                      const SizedBox(width: 3),
                      Text(
                        'Verified Seller',
                        style: TextStyle(
                            fontSize: 11, color: Colors.green.shade600,),
                      ),
                      const SizedBox(width: 8),
                      Icon(Icons.storefront_outlined,
                          size: 11, color: Colors.grey.shade500,),
                      const SizedBox(width: 3),
                      Flexible(
                        child: Text(
                          storeId,
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                          style: TextStyle(
                              fontSize: 10,
                              color: Colors.grey.shade400,
                              fontFamily: 'monospace',),
                        ),
                      ),
                    ],
                  ),
                ],
              ),
            ),
            // Visit Store button
            Container(
              padding:
                  const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
              decoration: BoxDecoration(
                color: AppTheme.blackAccent,
                borderRadius: BorderRadius.circular(12),
              ),
              child: const Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Text(
                    'Visit Store',
                    style: TextStyle(
                      color: AppTheme.limeAccent,
                      fontWeight: FontWeight.bold,
                      fontSize: 12,
                    ),
                  ),
                  SizedBox(width: 4),
                  Icon(Icons.arrow_forward_ios_rounded,
                      size: 10, color: AppTheme.limeAccent,),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}
