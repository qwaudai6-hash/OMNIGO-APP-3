import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter_stripe/flutter_stripe.dart';
import 'package:geolocator/geolocator.dart';
import 'package:http/http.dart' as http;
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
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';
      final resp = await http.get(
        Uri.parse(ApiEndpoints.vendorStore(storeId)),
        headers: {'Authorization': 'Bearer $token'},
      ).timeout(const Duration(seconds: 8));

      if (resp.statusCode == 200 && mounted) {
        setState(() {
          _storeInfo = jsonDecode(resp.body) as Map<String, dynamic>;
          _isLoadingStore = false;
        });
      } else if (mounted) {
        setState(() => _isLoadingStore = false);
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
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      final response = await http.get(
        Uri.parse(ApiEndpoints.productRecommendations(prodId)),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
      ).timeout(const Duration(seconds: 5));

      if (response.statusCode == 200 && mounted) {
        final data = jsonDecode(response.body) as List<dynamic>;
        setState(() {
          _recommendations = data.map((e) => Product.fromJson(e as Map<String, dynamic>)).toList();
          _isLoadingRecommendations = false;
        });
      } else if (mounted) {
        setState(() => _isLoadingRecommendations = false);
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
      final token = prefs.getString('jwt_token') ?? '';
      final customerTrackingId = prefs.getString('tracking_id') ?? '';

      final response = await http.post(
        Uri.parse(ApiEndpoints.reviewCreate()),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
        },
        body: jsonEncode({
          'product_tracking_id': prodId,
          'customer_tracking_id': customerTrackingId,
          'rating': rating,
          'comment': comment,
        }),
      ).timeout(const Duration(seconds: 8));

      if (response.statusCode == 201 && mounted) {
        Navigator.pop(context); // close review dialog
        unawaited(_fetchReviewSummary());
        unawaited(_fetchReviews());
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Review submitted!'), backgroundColor: Colors.green),
        );
      } else if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Review failed: ${response.statusCode}'), backgroundColor: Colors.redAccent),
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

  Future<void> _executeCheckout() async {
    if (_isCheckoutProcessing) return;

    // ── Payment method selector ─────────────────────────────────────
    final paymentChoice = await _showPaymentMethodDialog();
    if (paymentChoice == null) return; // user cancelled

    setState(() {
      _isCheckoutProcessing = true;
    });

    // Fetch GPS location for delivery
    double dropoffLat = 31.5204;
    double dropoffLng = 74.3587;
    try {
      if (await Geolocator.isLocationServiceEnabled()) {
        var permission = await Geolocator.checkPermission();
        if (permission == LocationPermission.denied) {
          permission = await Geolocator.requestPermission();
        }
        if (permission == LocationPermission.whileInUse || permission == LocationPermission.always) {
          final pos = await Geolocator.getCurrentPosition(desiredAccuracy: LocationAccuracy.high, timeLimit: const Duration(seconds: 8));
          dropoffLat = pos.latitude;
          dropoffLng = pos.longitude;
        }
      }
    } catch (_) {
      // Use default Lahore coords if GPS fails
    }

    final String nonce = '${widget.product.productTrackingId}_${DateTime.now().millisecondsSinceEpoch}';
    final prefs = await SharedPreferences.getInstance();
    await prefs.setString('pending_nonce', nonce);
    await prefs.setString('pending_order_product', widget.product.name.isNotEmpty ? widget.product.name : 'Unknown');
    await prefs.setString('pending_order_status', 'PENDING_CONFIRMATION');

    try {
      final url = Uri.parse(ApiEndpoints.orderCheckout());
      final vendorStoreId = widget.product.storeTrackingId;
      final unitPrice = widget.product.basePrice;
      final totalPrice = unitPrice * _quantity;
      final prodId = widget.product.productTrackingId.isNotEmpty ? widget.product.productTrackingId : 'PROD-N/A';
      final token = prefs.getString('jwt_token') ?? '';

      final reqItems = [{
        'product_tracking_id': prodId,
        'quantity': _quantity,
      }];

      // ── Step 1: Place the order FIRST so the backend allocates a real
      //    ORD- tracking ID. The previous flow created a synthetic nonce
      //    up-front and passed it to Stripe, which meant the Stripe
      //    PaymentIntent and the order row had different IDs — the
      //    webhook couldn't correlate the payment back to the order.
      final orderResponse = await http.post(
        url,
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $token',
          'X-Customer-ID': widget.userTrackingId,
          'X-Store-ID': vendorStoreId,
          'X-Device-Session-Nonce': nonce,
        },
        body: jsonEncode({
          'user_tracking_id': widget.userTrackingId,
          'vendor_store_tracking_id': vendorStoreId,
          'items': reqItems,
          'total_amount': totalPrice,
          'currency': 'PKR',
          'payment_gateway': paymentChoice == 'cash' ? 'cod' : paymentChoice,
          'device_session_nonce': nonce,
          'dropoff_lat': dropoffLat,
          'dropoff_lng': dropoffLng,
        }),
      ).timeout(const Duration(seconds: 8));

      if (orderResponse.statusCode != 201) {
        await prefs.remove('pending_nonce');
        await prefs.remove('pending_order_product');
        await prefs.remove('pending_order_status');
        if (mounted) _showErrorDialog('Failed to create order. Server returned ${orderResponse.statusCode}');
        return;
      }

      final orderData = jsonDecode(orderResponse.body) as Map<String, dynamic>;
      final realOrderTrackingId =
          (orderData['order_tracking_id'] ?? orderData['tracking_id'])?.toString();
      if (realOrderTrackingId == null || realOrderTrackingId.isEmpty) {
        if (mounted) _showErrorDialog('Order created but no tracking id returned.');
        return;
      }

      // ── Step 2: Branch on payment method using the REAL order id.
      if (paymentChoice == 'card') {
        // Stripe Payment Intent + Payment Sheet keyed off the real order id.
        final checkoutUrl = Uri.parse(ApiEndpoints.stripeCheckout());
        final checkoutResponse = await http.post(
          checkoutUrl,
          headers: {
            'Content-Type': 'application/json',
            'Authorization': 'Bearer $token',
          },
          body: jsonEncode({
            'gateway': 'stripe',
            'customer_id': widget.userTrackingId,
            'store_id': vendorStoreId,
            'order_id': realOrderTrackingId,
            'amount': totalPrice,
            'currency': 'PKR',
          }),
        ).timeout(const Duration(seconds: 15));

        if (checkoutResponse.statusCode == 200) {
          final checkoutData = jsonDecode(checkoutResponse.body) as Map<String, dynamic>;
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
            } catch (e) {
              if (mounted) {
                setState(() => _isCheckoutProcessing = false);
                _showErrorDialog('Payment was cancelled or failed: $e');
              }
              await prefs.remove('pending_nonce');
              await prefs.remove('pending_order_product');
              await prefs.remove('pending_order_status');
              return;
            }
          }
        }
      } else if (paymentChoice == 'wallet') {
        // Wallet balance checkout: deduct directly via /payment/checkout
        final walletCheckoutUrl = Uri.parse(ApiEndpoints.stripeCheckout());
        final walletCheckoutResp = await http.post(
          walletCheckoutUrl,
          headers: {
            'Content-Type': 'application/json',
            'Authorization': 'Bearer $token',
          },
          body: jsonEncode({
            'gateway': 'wallet',
            'customer_id': widget.userTrackingId,
            'order_id': realOrderTrackingId,
            'amount': totalPrice,
            'currency': 'PKR',
          }),
        ).timeout(const Duration(seconds: 10));

        if (walletCheckoutResp.statusCode == 200) {
          final wcData = jsonDecode(walletCheckoutResp.body) as Map<String, dynamic>;
          final wcError = wcData['error']?.toString();
          if (wcError != null && wcError.isNotEmpty) {
            if (mounted) setState(() => _isCheckoutProcessing = false);
            if (mounted) _showErrorDialog(wcError);
            await prefs.remove('pending_nonce');
            await prefs.remove('pending_order_product');
            await prefs.remove('pending_order_status');
            return;
          }
        } else {
          if (mounted) setState(() => _isCheckoutProcessing = false);
          if (mounted) _showErrorDialog('Wallet payment failed. Server returned ${walletCheckoutResp.statusCode}');
          await prefs.remove('pending_nonce');
          await prefs.remove('pending_order_product');
          await prefs.remove('pending_order_status');
          return;
        }
      } else if (paymentChoice == 'jazzcash' || paymentChoice == 'easypaisa') {
        // Mobile wallet redirect flow (scaffolding — redirect URL returned)
        final walletUrl = Uri.parse('${ApiEndpoints.orderBase}/wallet/charge');
        final walletResponse = await http.post(
          walletUrl,
          headers: {
            'Content-Type': 'application/json',
            'Authorization': 'Bearer $token',
          },
          body: jsonEncode({
            'customer_id': widget.userTrackingId,
            'store_id': vendorStoreId,
            'gateway': paymentChoice,
            'order_id': realOrderTrackingId,
            'amount_cents': (totalPrice * 100).round(),
            'nonce': nonce,
          }),
        ).timeout(const Duration(seconds: 10));

        if (walletResponse.statusCode == 200) {
          final walletData = jsonDecode(walletResponse.body) as Map<String, dynamic>;
          final redirectUrl = walletData['redirect_url']?.toString() ?? '';
          if (redirectUrl.isNotEmpty && mounted) {
            final uri = Uri.parse(redirectUrl);
            if (await canLaunchUrl(uri)) {
              await launchUrl(uri, mode: LaunchMode.externalApplication);
            }
          }
          // Order will be confirmed via webhook after payment.
          if (mounted) setState(() => _isCheckoutProcessing = false);
          if (mounted) _showPaymentPendingDialog(nonce, method: paymentChoice == 'jazzcash' ? 'JazzCash' : 'EasyPaisa');
          await prefs.remove('pending_nonce');
          await prefs.remove('pending_order_product');
          await prefs.remove('pending_order_status');
          return;
        } else {
          if (mounted) setState(() => _isCheckoutProcessing = false);
          if (mounted) _showErrorDialog('Failed to initiate wallet payment. Server returned ${walletResponse.statusCode}');
          await prefs.remove('pending_nonce');
          await prefs.remove('pending_order_product');
          await prefs.remove('pending_order_status');
          return;
        }
      } else if (paymentChoice == 'payfast') {
        // PayFast PK — Option C tokenized flow (same pipeline as cart checkout):
        // fraud checks, payment_transactions audit row, 3DS step-up, gateway
        // verification and the admin/vendor/delivery ledger split all happen
        // server-side in the payment orchestrator.
        if (!mounted) return;
        final cardDetails = await showPayFastCardDetailsSheet(context);
        if (cardDetails == null) {
          // User cancelled card entry — keep the order, drop the pending nonce.
          if (mounted) setState(() => _isCheckoutProcessing = false);
          await prefs.remove('pending_nonce');
          return;
        }

        final payfastResponse = await http.post(
          Uri.parse(ApiEndpoints.payfastPayment()),
          headers: {
            'Content-Type': 'application/json',
            'Authorization': 'Bearer $token',
            // Idempotency-Key: a network-level retry of this exact Buy Now
            // checkout replays the original attempt instead of double-charging.
            'Idempotency-Key': 'buynow-$nonce',
          },
          body: jsonEncode({
            'order_id': realOrderTrackingId,
            // account_type_id intentionally omitted — the orchestrator derives
            // it from the supplied instrument (card vs bank/wallet).
            'card_number': cardDetails['card_number'],
            'expiry_month': cardDetails['expiry_month'],
            'expiry_year': cardDetails['expiry_year'],
            'cvv': cardDetails['cvv'],
            'customer_mobile_no': cardDetails['customer_mobile_no'],
          }),
        ).timeout(const Duration(seconds: 15));

        if (payfastResponse.statusCode != 200) {
          if (mounted) setState(() => _isCheckoutProcessing = false);
          final errBody = jsonDecode(payfastResponse.body) as Map<String, dynamic>? ?? {};
          final errMsg = errBody['error']?.toString() ??
              'Failed to initiate PayFast payment. Server returned ${payfastResponse.statusCode}';
          if (mounted) _showErrorDialog(errMsg);
          return;
        }

        final pfData = jsonDecode(payfastResponse.body) as Map<String, dynamic>;
        final status = pfData['status']?.toString();
        if (status == 'failed') {
          if (mounted) setState(() => _isCheckoutProcessing = false);
          if (mounted) _showErrorDialog(pfData['message']?.toString() ?? 'PayFast payment failed');
          return;
        }

        // Hosted checkout redirect (apps.net.pk): open PayFast payment page in browser.
        // After payment, PayFast sends IPN to our callback URL which marks order paid.
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
          } else {
            if (mounted) setState(() => _isCheckoutProcessing = false);
            if (mounted) _showErrorDialog('Payment gateway returned no redirect URL');
          }
          return;
        }

        // 3DS step-up: render the bank challenge; the ACS posts its result to the
        // backend callback directly, which resumes tokenization + settlement.
        if (status == '3ds_redirect' || pfData['action'] == '3ds_redirect') {
          final threedHtml = pfData['threed_html']?.toString() ?? '';
          if (threedHtml.isNotEmpty && mounted) {
            final verified = await showPayFast3DSChallenge(context, threedHtml);
            if (!verified) {
              if (mounted) setState(() => _isCheckoutProcessing = false);
              if (mounted) {
                _showErrorDialog('3DS verification was cancelled or incomplete. Your order is saved as pending.');
              }
              return;
            }
          }
        }

        // Deferred outcomes: money may still be settling at the gateway.
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

  void _showPaymentPendingDialog(String nonce, {String method = 'PayFast'}) {
    showDialog<void>(
      context: context,
      builder: (ctx) => AlertDialog(
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(20)),
        title: const Text('Payment Pending', style: TextStyle(color: Colors.orange)),
        content: Text(
          'You are being redirected to $method to complete your payment.\n\n'
          'Your order (Ref: $nonce) will be confirmed once payment is received.\n'
          'Please do not close this screen.',
        ),
        actions: [
          TextButton(
            onPressed: () {
              Navigator.pop(ctx);
              Navigator.pop(context);
            },
            child: const Text('OK'),
          ),
        ],
      ),
    );
  }

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
                                      Navigator.pushReplacement(
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

                  // Quantity Stepper
                  Row(
                    mainAxisAlignment: MainAxisAlignment.spaceBetween,
                    children: [
                      const Text(
                        'Quantity',
                        style: TextStyle(fontSize: 16, fontWeight: FontWeight.bold, color: AppTheme.blackAccent),
                      ),
                      Container(
                        decoration: BoxDecoration(
                          border: Border.all(color: Colors.grey.shade300),
                          borderRadius: BorderRadius.circular(16),
                        ),
                        child: Row(
                          children: [
                            IconButton(
                              icon: const Icon(Icons.remove_rounded, color: AppTheme.blackAccent),
                              onPressed: _quantity > 1
                                  ? () => setState(() => _quantity--)
                                  : null,
                            ),
                            Text(
                              '$_quantity',
                              style: const TextStyle(
                                fontSize: 18,
                                fontWeight: FontWeight.bold,
                                color: AppTheme.blackAccent,
                              ),
                            ),
                            IconButton(
                              icon: const Icon(Icons.add_rounded, color: AppTheme.blackAccent),
                              onPressed: _quantity < _maxQuantity
                                  ? () => setState(() => _quantity++)
                                  : null,
                            ),
                          ],
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 16),

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
            width: 20, height: 20,
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
