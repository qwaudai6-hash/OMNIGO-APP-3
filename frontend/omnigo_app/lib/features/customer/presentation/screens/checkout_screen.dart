import 'package:flutter/material.dart';
import 'package:flutter_stripe/flutter_stripe.dart';
import 'package:geolocator/geolocator.dart';
import 'package:maplibre_gl/maplibre_gl.dart';
import 'package:provider/provider.dart';
import 'package:shared_preferences/shared_preferences.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/services/cart_provider.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/di/service_locator.dart';
import '../widgets/payfast_card_sheet.dart';
import 'order_success_screen.dart';

class CheckoutScreen extends StatefulWidget {
  const CheckoutScreen({super.key});

  @override
  State<CheckoutScreen> createState() => _CheckoutScreenState();
}

class _CheckoutScreenState extends State<CheckoutScreen> {
  int _currentStep = 0;
  String _selectedPaymentMethod = 'cod';
  bool _isLoading = false;
  late final String _checkoutSessionNonce;
  String? _createdOrderTrackingId;
  double _deliveryFee = 0.0;
  bool _deliveryFeeLoading = false;
  String _routingStatus = 'DYNAMIC_CALCULATED'; // H3: tracks how delivery fee was calculated

  // Real delivery location — fetched from GPS
  String _deliveryAddress = 'Fetching your location...';
  LatLng? _deliveryLocation;
  bool _isFetchingLocation = true;
  String? _locationError;

  @override
  void initState() {
    super.initState();
    _checkoutSessionNonce = '${DateTime.now().millisecondsSinceEpoch}_${UniqueKey().toString()}';
    _fetchCurrentLocation();
  }

  // Helper: Cancel order on payment failure
  Future<void> _cancelOrderOnFailure(String orderId, String reason) async {
    try {
      await sl<ApiClient>().post(
        ApiEndpoints.customerCancelOrder(orderId),
        {'reason': reason},
      );
    } catch (e) {
      debugPrint('Failed to cancel order $orderId: $e');
    }
  }

  // Helper: Get user-friendly error message from exception
  String _getUserFriendlyError(dynamic e, String defaultMessage) {
    if (e == null) return defaultMessage;
    final errStr = e.toString().toLowerCase();

    if (errStr.contains('socketexception') || errStr.contains('timeout') || errStr.contains('connection')) {
      return 'Network error. Please check your internet connection and try again.';
    }
    if (errStr.contains('insufficient')) {
      return 'Insufficient wallet balance for this purchase.';
    }
    if (errStr.contains('already paid') || errStr.contains('409')) {
      return 'This order has already been paid.';
    }
    if (errStr.contains('cancelled') || errStr.contains('declined')) {
      return 'Payment was declined. Please try a different payment method.';
    }
    if (errStr.contains('card') && errStr.contains('invalid')) {
      return 'Invalid card details. Please check and try again.';
    }
    return defaultMessage;
  }

  // Helper: Check if error indicates order is already paid
  bool _isAlreadyPaidError(dynamic error) {
    if (error == null) return false;
    final errStr = error.toString().toLowerCase();
    return errStr.contains('already paid') ||
        errStr.contains('already been paid') ||
        errStr.contains('409');
  }

  Future<void> _fetchCurrentLocation() async {
    setState(() {
      _isFetchingLocation = true;
      _locationError = null;
    });

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
          setState(() {
            _locationError = 'Location permission denied. Please enable in Settings.';
            _isFetchingLocation = false;
          });
          return;
        }
      }

      if (permission == LocationPermission.deniedForever) {
        setState(() {
          _locationError = 'Location permission permanently denied. Enable in Settings.';
          _isFetchingLocation = false;
        });
        return;
      }

      final Position position = await Geolocator.getCurrentPosition(
        locationSettings: const LocationSettings(accuracy: LocationAccuracy.high),
      );

      final latLng = LatLng(position.latitude, position.longitude);

      // Reverse geocode to get human-readable address. Goes through the
      // internal /api/v1/geo/reverse proxy (Kong → admin service) so we
      // never hit the public Nominatim service from the mobile client —
      // see phase 5 of the architecture plan.
      String address = '${position.latitude.toStringAsFixed(4)}, ${position.longitude.toStringAsFixed(4)}';
      try {
        final data = await sl<ApiClient>().get(
          ApiEndpoints.geocodingReverse(position.latitude, position.longitude),
        ) as Map<String, dynamic>;
        final addr = (data['address'] ?? <String, dynamic>{}) as Map<String, dynamic>;
        final parts = <String>[
          if (addr['road'] != null) (addr['road'] as String),
          if (addr['suburb'] != null) (addr['suburb'] as String),
          if (addr['city'] != null)
            (addr['city'] as String)
          else if (addr['town'] != null)
            (addr['town'] as String),
          if (addr['country'] != null) (addr['country'] as String),
        ];
        if (parts.isNotEmpty) address = parts.join(', ');
      } catch (_) {
        // Use coordinates as fallback
      }

      if (mounted) {
        setState(() {
          _deliveryLocation = latLng;
          _deliveryAddress = address;
          _isFetchingLocation = false;
        });
        _estimateDeliveryFee();
      }
    } catch (e) {
      if (mounted) {
        setState(() {
          _locationError = 'Failed to get location: $e';
          _isFetchingLocation = false;
        });
      }
    }
  }

  Future<void> _estimateDeliveryFee() async {
    if (_deliveryLocation == null) return;
    final cart = context.read<CartProvider>();
    final storeId = cart.currentStoreId;
    if (storeId == null || storeId.isEmpty) return;

    setState(() => _deliveryFeeLoading = true);
    try {
      final resp = await sl<ApiClient>().post(
        ApiEndpoints.deliveryEstimateFee(),
        {
          'vendor_store_tracking_id': storeId,
          'dropoff_lat': _deliveryLocation!.latitude,
          'dropoff_lng': _deliveryLocation!.longitude,
        },
      );
      if (mounted && resp != null) {
        final fee = (resp['delivery_fee'] is num) ? (resp['delivery_fee'] as num).toDouble() : 0.0;
        final routing = (resp['routing_status'] as String?) ?? 'DYNAMIC_CALCULATED';
        setState(() {
          _deliveryFee = fee;
          _routingStatus = routing;
          _deliveryFeeLoading = false;
        });
        cart.setDeliveryFee(fee);
      }
    } catch (e) {
      if (mounted) {
        setState(() {
          _deliveryFee = 0.0;
          _routingStatus = 'FAILED_CALCULATION';
          _deliveryFeeLoading = false;
        });
        cart.setDeliveryFee(0.0);
      }
    }
  }

  Future<void> _submitOrder(CartProvider cart) async {
    if (_deliveryLocation == null) {
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Please wait for location to be fetched'), backgroundColor: Colors.orange),
      );
      return;
    }

    // H4: Block checkout if delivery fee wasn't calculated (API failed)
    if (_deliveryFee <= 0) {
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(
          content: Text('Could not calculate delivery fee. Please check your connection and try again.'),
          backgroundColor: Colors.red,
          behavior: SnackBarBehavior.floating,
        ),
      );
      return;
    }

    setState(() => _isLoading = true);
    _createdOrderTrackingId = null;
    try {
      final prefs = await SharedPreferences.getInstance();
      final userTrackId = prefs.getString('tracking_id') ?? '';
      final apiClient = sl<ApiClient>();

      final items = cart.items.values.map((i) => {
        'product_tracking_id': i.productId,
        'quantity': i.quantity,
      },).toList();

      final storeTrackingId = cart.items.values.first.storeTrackingId;

      // Place the order FIRST so the backend allocates a real ORD- tracking
      // ID. Previously we generated a synthetic ID up-front and passed it
      // to Stripe, which meant the Stripe PaymentIntent and the order
      // row had different IDs — the webhook couldn't correlate the
      // payment back to the order.
      final payload = {
        'user_tracking_id': userTrackId,
        'vendor_store_tracking_id': storeTrackingId,
        'dropoff_lat': _deliveryLocation!.latitude,
        'dropoff_lng': _deliveryLocation!.longitude,
        'payment_gateway': _selectedPaymentMethod,
        'currency': 'PKR',
        'device_session_nonce': _checkoutSessionNonce,
        'items': items,
        'total_amount': cart.totalAmount,
        // H4: Uber-style — customer pays product + delivery fee
        'delivery_fee_paisa': (_deliveryFee * 100).round(),
        // H3: routing audit trail
        'routing_status': _routingStatus,
      };

      final response = await apiClient.post(ApiEndpoints.orderCheckout(), payload);
      final realOrderTrackingId =
          (response['order_tracking_id'] ?? response['tracking_id']) as String?;
      if (realOrderTrackingId == null) {
        throw Exception('Order creation failed: no tracking id returned');
      }
      _createdOrderTrackingId = realOrderTrackingId;

      // If card payment selected, create Stripe PaymentIntent using the
      // REAL order tracking ID so the webhook can correlate.
      if (_selectedPaymentMethod == 'card') {
        final checkoutResponse = await apiClient.post(
          ApiEndpoints.stripeCheckout(),
          {
            'gateway': 'stripe',
            'order_id': realOrderTrackingId,
            'customer_id': userTrackId,
            'amount': cart.totalAmount,
            'currency': 'PKR',
            'return_url': '${ApiEndpoints.gatewayBase}/order-success',
            'cancel_url': '${ApiEndpoints.gatewayBase}/checkout',
          },
          idempotencyKey: 'checkout_stripe_$_checkoutSessionNonce',
        ) as Map<String, dynamic>;

        // Check if order is already paid (Stripe returns error instead of client_secret)
        final stripeError = checkoutResponse['error']?.toString() ?? '';
        if (stripeError.contains('already paid')) {
          await cart.clearCart();
          if (mounted) {
            await Navigator.pushAndRemoveUntil(
              context,
              MaterialPageRoute<void>(
                builder: (_) => OrderSuccessScreen(trackingId: realOrderTrackingId.toString()),
              ),
              (route) => route.isFirst,
            );
          }
          return;
        }

        final clientSecret = checkoutResponse['client_secret']?.toString();
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
            await _cancelOrderOnFailure(realOrderTrackingId.toString(), 'Stripe payment failed');
            if (mounted) {
              final userMsg = _getUserFriendlyError(e, 'Payment failed. Please try again.');
              ScaffoldMessenger.of(context).showSnackBar(
                SnackBar(
                  content: Text('$userMsg Your order has been cancelled.'),
                  duration: const Duration(seconds: 5),
                  backgroundColor: Colors.red,
                  action: SnackBarAction(
                    label: 'OK',
                    textColor: Colors.white,
                    onPressed: () {},
                  ),
                ),
              );
            }
            return;
          }
        } else {
          await _cancelOrderOnFailure(realOrderTrackingId.toString(), 'Stripe checkout failed: no client secret');
          throw Exception('Unable to initialize payment. Please try again.');
        }
      } else if (_selectedPaymentMethod == 'wallet') {
        // Wallet checkout: deduct balance directly via /payment/checkout
        try {
          await apiClient.post(
            ApiEndpoints.stripeCheckout(),
            {
              'gateway': 'wallet',
              'order_id': realOrderTrackingId,
              'customer_id': userTrackId,
              'amount': cart.totalAmount,
              'currency': 'PKR',
              'return_url': '${ApiEndpoints.gatewayBase}/order-success',
              'cancel_url': '${ApiEndpoints.gatewayBase}/checkout',
            },
            idempotencyKey: 'checkout_wallet_$_checkoutSessionNonce',
          );
        } catch (e) {
          if (_isAlreadyPaidError(e)) {
            await cart.clearCart();
            if (mounted) {
              await Navigator.pushAndRemoveUntil(
                context,
                MaterialPageRoute<void>(
                  builder: (_) => OrderSuccessScreen(trackingId: realOrderTrackingId.toString()),
                ),
                (route) => route.isFirst,
              );
            }
            return;
          }
          await _cancelOrderOnFailure(realOrderTrackingId.toString(), 'Wallet payment failed');
          final userMsg = _getUserFriendlyError(e, 'Wallet payment failed. Please try again.');
          throw Exception(userMsg);
        }
      } else if (_selectedPaymentMethod == 'payfast') {
        // Collect card details from user (Option C Tokenized Flow)
        final cardDetails = await showPayFastCardDetailsSheet(context);
        if (cardDetails == null) {
          // User cancelled card entry
          if (mounted) setState(() => _isLoading = false);
          return;
        }

        final payfastResponse = await apiClient.post(
          ApiEndpoints.payfastPayment(),
          {
            'order_id': realOrderTrackingId,
            // account_type_id intentionally omitted — the orchestrator derives
            // it from the supplied instrument (card vs bank/wallet).
            'card_number': cardDetails['card_number'],
            'expiry_month': cardDetails['expiry_month'],
            'expiry_year': cardDetails['expiry_year'],
            'cvv': cardDetails['cvv'],
            'customer_mobile_no': cardDetails['customer_mobile_no'],
          },
          idempotencyKey: 'checkout_payfast_$_checkoutSessionNonce',
        ) as Map<String, dynamic>;

        // Check for already paid error in response
        final errorMsg = payfastResponse['error']?.toString() ?? '';
        if (errorMsg.contains('already paid')) {
          // Order already paid - show success instead of error
          await cart.clearCart();
          if (mounted) {
            await Navigator.pushAndRemoveUntil(
              context,
              MaterialPageRoute<void>(
                builder: (_) => OrderSuccessScreen(trackingId: realOrderTrackingId.toString()),
              ),
              (route) => route.isFirst,
            );
          }
          return;
        }

        final status = payfastResponse['status']?.toString();
        if (status == 'failed') {
          await _cancelOrderOnFailure(realOrderTrackingId.toString(), 'PayFast payment failed');
          throw Exception(_getUserFriendlyError(payfastResponse['message'], 'Payment failed. Please try a different payment method.'));
        }

        // Handle 3DS Challenge redirect if gateway returned 3DS form HTML
        if (status == '3ds_redirect') {
          final threedHtml = payfastResponse['threed_html']?.toString() ?? '';
          if (threedHtml.isNotEmpty && mounted) {
            final verified = await showPayFast3DSChallenge(context, threedHtml);
            if (!verified) {
              await _cancelOrderOnFailure(realOrderTrackingId.toString(), '3DS verification failed');
              throw Exception('Payment verification failed. Please try again.');
            }
          }
        }

        // Deferred outcomes: money may not be captured yet — inform the user instead
        // of showing an unconditional success screen.
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

        // Settlement pending or success - proceed to poll for order confirmation
        if (status == 'settlement_pending' || status == 'success' || status == 'approved') {
          // Payment authorized, proceed to confirmation polling
        }
      }

      final trackingId = realOrderTrackingId.toString();

      // BUG-16 FIX: Skip payment confirmation poll for COD orders — COD is paid on delivery, not at order time.
      if (_selectedPaymentMethod == 'cod' || _selectedPaymentMethod == 'cash') {
        await cart.clearCart();
        if (mounted) {
          await Navigator.pushAndRemoveUntil(
            context,
            MaterialPageRoute<void>(builder: (_) => OrderSuccessScreen(trackingId: trackingId)),
            (route) => route.isFirst,
          );
        }
        return;
      }

      // PF-4 FIX: never show unconditional success. Poll the order until the
      // SettlementWorker flips it to a paid state (or timeout → honest
      // "processing" handoff). Covers 3DS completion, manual-verify tap, and
      // direct non-3DS approvals alike.
      final paidConfirmed = await _waitForPaymentConfirmation(
        apiClient,
        realOrderTrackingId.toString(),
      );

      if (!paidConfirmed && mounted) {
        // Don't clear cart on payment failure — user may want to retry
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
            content: Text('Payment is still processing at the bank. Track this order — status updates automatically.'),
            duration: Duration(seconds: 6),
          ),
        );
        await Navigator.pushReplacement(
          context,
          MaterialPageRoute<void>(builder: (_) => OrderSuccessScreen(trackingId: trackingId, pending: true)),
        );
        return;
      }

      // Only clear cart after successful payment confirmation
      await cart.clearCart();

      if (mounted) {
        await Navigator.pushAndRemoveUntil(
          context,
          MaterialPageRoute<void>(builder: (_) => OrderSuccessScreen(trackingId: trackingId)),
          (route) => route.isFirst,
        );
      }
    } catch (e) {
      if (_createdOrderTrackingId != null) {
        final errStr = e.toString().toLowerCase();
        final isNetworkError = errStr.contains('socketexception') ||
            errStr.contains('timeout') ||
            errStr.contains('connection') ||
            errStr.contains('network') ||
            errStr.contains('no internet');
        if (isNetworkError) {
          await _cancelOrderOnFailure(_createdOrderTrackingId!, 'Network error during payment');
        }
      }
      if (mounted) {
        final userMsg = _getUserFriendlyError(e, 'Something went wrong. Please try again.');
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
            content: Text(userMsg),
            backgroundColor: Colors.red,
            duration: const Duration(seconds: 4),
            action: SnackBarAction(
              label: 'OK',
              textColor: Colors.white,
              onPressed: () {},
            ),
          ),
        );
      }
    } finally {
      if (mounted) setState(() => _isLoading = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: const Color(0xFFF8F9FA),
      appBar: AppBar(
        title: const Text('Checkout', style: TextStyle(color: AppTheme.blackAccent, fontWeight: FontWeight.bold)),
        backgroundColor: Colors.white,
        elevation: 0,
        iconTheme: const IconThemeData(color: AppTheme.blackAccent),
      ),
      body: Consumer<CartProvider>(
        builder: (context, cart, child) {
          if (cart.items.isEmpty && !_isLoading) {
            return const Center(child: Text('Your cart is empty.'));
          }

          return Stepper(
            type: StepperType.vertical,
            currentStep: _currentStep,
            onStepContinue: () {
              if (_currentStep < 2) {
                setState(() => _currentStep += 1);
              } else {
                _submitOrder(cart);
              }
            },
            onStepCancel: () {
              if (_currentStep > 0) {
                setState(() => _currentStep -= 1);
              } else {
                Navigator.pop(context);
              }
            },
            controlsBuilder: (context, details) {
              final isLastStep = _currentStep == 2;
              return Container(
                margin: const EdgeInsets.only(top: 24),
                child: Row(
                  children: [
                    Expanded(
                      child: ElevatedButton(
                        style: ElevatedButton.styleFrom(
                          backgroundColor: AppTheme.blackAccent,
                          padding: const EdgeInsets.symmetric(vertical: 16),
                          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
                        ),
                        onPressed: _isLoading ? null : details.onStepContinue,
                        child: _isLoading
                            ? const SizedBox(width: 24, height: 24, child: CircularProgressIndicator(color: Colors.white, strokeWidth: 2))
                            : Text(isLastStep ? 'Place Order' : 'Continue', style: const TextStyle(color: Colors.white, fontWeight: FontWeight.bold)),
                      ),
                    ),
                    if (_currentStep > 0 && !_isLoading) ...[
                      const SizedBox(width: 12),
                      Expanded(
                        child: OutlinedButton(
                          style: OutlinedButton.styleFrom(
                            padding: const EdgeInsets.symmetric(vertical: 16),
                            shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
                          ),
                          onPressed: details.onStepCancel,
                          child: const Text('Back', style: TextStyle(color: AppTheme.blackAccent)),
                        ),
                      ),
                    ],
                  ],
                ),
              );
            },
            steps: [
              Step(
                title: const Text('Delivery Address', style: TextStyle(fontWeight: FontWeight.bold)),
                content: Container(
                  width: double.infinity,
                  padding: const EdgeInsets.all(16),
                  decoration: BoxDecoration(color: Colors.white, borderRadius: BorderRadius.circular(12), border: Border.all(color: Colors.grey.shade200)),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      if (_isFetchingLocation) ...[
                        const Row(
                          children: [
                            SizedBox(width: 24, height: 24, child: CircularProgressIndicator(strokeWidth: 2)),
                            SizedBox(width: 12),
                            Text('Fetching your location...'),
                          ],
                        ),
                      ] else if (_locationError != null) ...[
                        Row(
                          children: [
                            const Icon(Icons.error_outline, color: Colors.redAccent, size: 20),
                            const SizedBox(width: 8),
                            Expanded(child: Text(_locationError!, style: const TextStyle(color: Colors.redAccent, fontSize: 13))),
                          ],
                        ),
                        const SizedBox(height: 8),
                        TextButton.icon(
                          onPressed: _fetchCurrentLocation,
                          icon: const Icon(Icons.refresh, size: 16),
                          label: const Text('Retry'),
                        ),
                      ] else ...[
                        Row(
                          children: [
                            const Icon(Icons.location_on, color: Colors.green),
                            const SizedBox(width: 8),
                            Expanded(child: Text(_deliveryAddress, style: const TextStyle(fontSize: 14))),
                          ],
                        ),
                        const SizedBox(height: 4),
                        Text(
                          'Lat: ${_deliveryLocation?.latitude.toStringAsFixed(4)}, Lng: ${_deliveryLocation?.longitude.toStringAsFixed(4)}',
                          style: TextStyle(color: Colors.grey.shade500, fontSize: 11),
                        ),
                        const SizedBox(height: 8),
                        TextButton.icon(
                          onPressed: _fetchCurrentLocation,
                          icon: const Icon(Icons.my_location, size: 16),
                          label: const Text('Refresh Location'),
                        ),
                      ],
                    ],
                  ),
                ),
                isActive: _currentStep >= 0,
                state: _currentStep > 0 ? StepState.complete : StepState.indexed,
              ),
              Step(
                title: const Text('Payment Method', style: TextStyle(fontWeight: FontWeight.bold)),
                content: Column(
                  children: [
                    _buildPaymentOption('cod', 'Cash on Delivery', Icons.money),
                    _buildPaymentOption('payfast', 'PayFast (Debit/Credit Card & Bank)', Icons.payment_outlined),
                    _buildPaymentOption('card', 'Credit/Debit Card (International)', Icons.credit_card),
                    _buildPaymentOption('wallet', 'Wallet Balance', Icons.account_balance_wallet),
                    _buildPaymentOption('easypaisa', 'EasyPaisa (Mobile Account)', Icons.account_balance_wallet, isComingSoon: true),
                    _buildPaymentOption('jazzcash', 'JazzCash (Mobile Account)', Icons.phone_android, isComingSoon: true),
                  ],
                ),
                isActive: _currentStep >= 1,
                state: _currentStep > 1 ? StepState.complete : StepState.indexed,
              ),
              Step(
                title: const Text('Order Summary', style: TextStyle(fontWeight: FontWeight.bold)),
                content: Container(
                  width: double.infinity,
                  padding: const EdgeInsets.all(16),
                  decoration: BoxDecoration(color: Colors.white, borderRadius: BorderRadius.circular(12), border: Border.all(color: Colors.grey.shade200)),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      ...cart.items.values.map((item) => Padding(
                        padding: const EdgeInsets.only(bottom: 8.0),
                        child: Row(
                          mainAxisAlignment: MainAxisAlignment.spaceBetween,
                          children: [
                            Expanded(child: Text('${item.quantity}x ${item.name}', maxLines: 1, overflow: TextOverflow.ellipsis)),
                            Text('PKR ${(item.price * item.quantity).toStringAsFixed(0)}', style: const TextStyle(fontWeight: FontWeight.bold)),
                          ],
                        ),
                      ),),
                      const Divider(),
                      Row(
                        mainAxisAlignment: MainAxisAlignment.spaceBetween,
                        children: [
                          const Text('Subtotal', style: TextStyle(color: Colors.grey)),
                          Text('PKR ${cart.totalAmount.toStringAsFixed(0)}'),
                        ],
                      ),
                      const SizedBox(height: 8),
                      Row(
                        mainAxisAlignment: MainAxisAlignment.spaceBetween,
                        children: [
                          const Text('Delivery Fee', style: TextStyle(color: Colors.grey)),
                          _deliveryFeeLoading
                              ? const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2))
                              : Text('PKR ${_deliveryFee.toStringAsFixed(0)}', style: const TextStyle(color: Colors.green, fontWeight: FontWeight.bold)),
                        ],
                      ),
                      const Divider(),
                      Row(
                        mainAxisAlignment: MainAxisAlignment.spaceBetween,
                        children: [
                          const Text('Total', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 18)),
                          Text('PKR ${cart.totalAmount.toStringAsFixed(0)}', style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 18, color: Colors.redAccent)),
                        ],
                      ),
                      const SizedBox(height: 4),
                      Text(
                        '* Delivery fee (PKR ${_deliveryFee.toStringAsFixed(0)}) is included by the platform — no extra charge to you',
                        style: TextStyle(fontSize: 11, color: Colors.grey[600]),
                      ),
                    ],
                  ),
                ),
                isActive: _currentStep >= 2,
              ),
            ],
          );
        },
      ),
    );
  }

  Widget _buildPaymentOption(String value, String title, IconData icon, {bool isComingSoon = false}) {
    final isSelected = !isComingSoon && _selectedPaymentMethod == value;
    return GestureDetector(
      onTap: () {
        if (isComingSoon) {
          ScaffoldMessenger.of(context).showSnackBar(
            SnackBar(
              content: Text('$title is coming soon! Please use PayFast Card or Cash on Delivery.'),
              duration: const Duration(seconds: 2),
            ),
          );
          return;
        }
        setState(() => _selectedPaymentMethod = value);
      },
      child: Container(
        margin: const EdgeInsets.only(bottom: 12),
        padding: const EdgeInsets.all(16),
        decoration: BoxDecoration(
          color: isSelected ? Colors.blue.shade50 : (isComingSoon ? Colors.grey.shade50 : Colors.white),
          borderRadius: BorderRadius.circular(12),
          border: Border.all(
            color: isSelected ? AppTheme.blackAccent : Colors.grey.shade200,
            width: isSelected ? 2 : 1,
          ),
        ),
        child: Row(
          children: [
            Icon(icon, color: isComingSoon ? Colors.grey.shade400 : (isSelected ? AppTheme.blackAccent : Colors.grey)),
            const SizedBox(width: 16),
            Expanded(
              child: Text(
                title,
                style: TextStyle(
                  fontWeight: isSelected ? FontWeight.bold : FontWeight.normal,
                  color: isComingSoon ? Colors.grey.shade500 : Colors.black87,
                ),
              ),
            ),
            if (isComingSoon)
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
                decoration: BoxDecoration(
                  color: Colors.amber.shade100,
                  borderRadius: BorderRadius.circular(8),
                  border: Border.all(color: Colors.amber.shade400, width: 0.8),
                ),
                child: Text(
                  'Coming Soon',
                  style: TextStyle(fontSize: 11, fontWeight: FontWeight.w600, color: Colors.amber.shade900),
                ),
              )
            else if (isSelected)
              const Icon(Icons.check_circle, color: AppTheme.blackAccent),
          ],
        ),
      ),
    );
  }
}

/// PF-4: polls order status up to ~45s waiting for the settlement worker to
/// mark the order paid. Returns true only on confirmed payment states.
/// Checks both `status` and `payment_status` fields since the backend may
/// set payment_status independently of the order status.
Future<bool> _waitForPaymentConfirmation(
  ApiClient apiClient,
  String orderId,
) async {
  const paidStatuses = {'paid', 'accepted', 'shipped', 'in_transit', 'delivered', 'completed'};
  const failedStatuses = {'failed', 'cancelled', 'payment_failed', 'refunded'};
  for (var i = 0; i < 15; i++) {
    await Future<void>.delayed(const Duration(seconds: 3));
    try {
      final resp = await apiClient.get('/orders/$orderId');
      if (resp is Map<String, dynamic>) {
        final orderStatus = resp['status']?.toString().toLowerCase() ?? '';
        final paymentStatus = resp['payment_status']?.toString().toLowerCase() ?? '';
        if (paidStatuses.contains(orderStatus) || paidStatuses.contains(paymentStatus)) return true;
        if (failedStatuses.contains(orderStatus) || failedStatuses.contains(paymentStatus)) return false;
      }
    } catch (_) {/* transient network errors — keep polling */}
  }
  return false;
}
