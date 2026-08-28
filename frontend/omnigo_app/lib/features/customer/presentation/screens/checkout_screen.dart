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

  Future<void> _submitOrder(CartProvider cart) async {
    if (_deliveryLocation == null) {
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Please wait for location to be fetched'), backgroundColor: Colors.orange),
      );
      return;
    }

    setState(() => _isLoading = true);
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
      };

      final response = await apiClient.post(ApiEndpoints.orderCheckout(), payload);
      final realOrderTrackingId =
          response['order_tracking_id'] ?? response['tracking_id'];
      if (realOrderTrackingId == null) {
        throw Exception('Order creation failed: no tracking id returned');
      }

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
        ) as Map<String, dynamic>;

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
            if (mounted) {
              ScaffoldMessenger.of(context).showSnackBar(
                SnackBar(
                  content: Text('Payment failed: ${e.toString()}. Your order has been automatically cancelled.'),
                  duration: const Duration(seconds: 5),
                  backgroundColor: Colors.red,
                ),
              );
            }
            return;
          }
        } else {
          throw Exception('No client secret returned from checkout');
        }
      } else if (_selectedPaymentMethod == 'payfast') {
        if (!mounted) return;
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
        ) as Map<String, dynamic>;

        final status = payfastResponse['status']?.toString();
        if (status == 'failed') {
          throw Exception(payfastResponse['message'] ?? 'PayFast payment failed');
        }

        // Handle 3DS Challenge redirect if gateway returned 3DS form HTML
        if (status == '3ds_redirect' || payfastResponse['action'] == '3ds_redirect') {
          final threedHtml = payfastResponse['threed_html']?.toString() ?? '';
          if (threedHtml.isNotEmpty && mounted) {
            final verified = await showPayFast3DSChallenge(context, threedHtml);
            if (!verified) {
              throw Exception('3DS verification was cancelled or incomplete');
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
      }

      // PF-4 FIX: never show unconditional success. Poll the order until the
      // SettlementWorker flips it to a paid state (or timeout → honest
      // "processing" handoff). Covers 3DS completion, manual-verify tap, and
      // direct non-3DS approvals alike.
      final paidConfirmed = await _waitForPaymentConfirmation(
        apiClient,
        realOrderTrackingId.toString(),
      );

      final trackingId = realOrderTrackingId.toString();

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
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(e.toString())));
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
                            ? const SizedBox(width: 20, height: 20, child: CircularProgressIndicator(color: Colors.white, strokeWidth: 2))
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
                            SizedBox(width: 20, height: 20, child: CircularProgressIndicator(strokeWidth: 2)),
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
                      const Row(
                        mainAxisAlignment: MainAxisAlignment.spaceBetween,
                        children: [
                          Text('Delivery Fee', style: TextStyle(color: Colors.grey)),
                          Text('PKR 0'),
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
