import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import '../../../../core/theme/app_theme.dart';
import '../screens/cart_screen.dart';

class OrderSuccessScreen extends StatelessWidget {

  const OrderSuccessScreen({
    super.key,
    required this.trackingId,
    this.pending = false,
    this.failed = false,
    this.otpCode,
  });
  final String trackingId;
  final bool pending;
  final bool failed;
  final String? otpCode;

  @override
  Widget build(BuildContext context) {
    final isFailed = failed && !pending;
    return Scaffold(
      backgroundColor: Colors.white,
      body: SafeArea(
        child: Padding(
          padding: const EdgeInsets.all(24.0),
          child: Column(
            mainAxisAlignment: MainAxisAlignment.center,
            children: [
              const Spacer(),
              Container(
                width: 120,
                height: 120,
                decoration: BoxDecoration(
                  color: isFailed ? Colors.red.shade50 : (pending ? Colors.amber.shade50 : Colors.green.shade50),
                  shape: BoxShape.circle,
                ),
                child: Icon(
                  isFailed ? Icons.error_outline : (pending ? Icons.hourglass_top_rounded : Icons.check_circle),
                  color: isFailed ? Colors.red : (pending ? Colors.orange : Colors.green),
                  size: 80,
                ),
              ),
              const SizedBox(height: 32),
              Text(
                isFailed ? 'Payment Failed' : (pending ? 'Payment Processing…' : 'Order Placed Successfully!'),
                style: const TextStyle(fontSize: 24, fontWeight: FontWeight.bold, color: AppTheme.blackAccent),
                textAlign: TextAlign.center,
              ),
              const SizedBox(height: 16),
              Text(
                isFailed
                    ? 'Your payment could not be processed. The order has been automatically cancelled. You can try again from your cart.'
                    : pending
                        ? 'Your payment is being verified with the bank. The order status updates automatically — no action needed.'
                        : 'Your order has been confirmed. You will receive a notification once a rider is assigned.',
                style: TextStyle(fontSize: 16, color: Colors.grey.shade600, height: 1.5),
                textAlign: TextAlign.center,
              ),
              const SizedBox(height: 32),
              GestureDetector(
                onTap: () {
                  Clipboard.setData(ClipboardData(text: trackingId));
                  ScaffoldMessenger.of(context).showSnackBar(
                    const SnackBar(content: Text('Order ID copied to clipboard'), duration: Duration(seconds: 2)),
                  );
                },
                child: Container(
                  padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 16),
                  decoration: BoxDecoration(
                    color: const Color(0xFFF8F9FA),
                    borderRadius: BorderRadius.circular(12),
                    border: Border.all(color: Colors.grey.shade200),
                  ),
                  child: Column(
                    children: [
                      Row(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          const Text('Order Tracking ID', style: TextStyle(color: Colors.grey, fontSize: 12)),
                          const SizedBox(width: 8),
                          Icon(Icons.copy, size: 14, color: Colors.grey.shade400),
                        ],
                      ),
                      const SizedBox(height: 8),
                      Text(
                        trackingId,
                        style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 16, fontFamily: 'monospace'),
                      ),
                    ],
                  ),
                ),
              ),
              const SizedBox(height: 16),
              // OTP display — available once rider is assigned
              if (otpCode != null && otpCode!.isNotEmpty)
                GestureDetector(
                  onTap: () {
                    Clipboard.setData(ClipboardData(text: otpCode!));
                    ScaffoldMessenger.of(context).showSnackBar(
                      const SnackBar(content: Text('OTP copied to clipboard'), duration: Duration(seconds: 2)),
                    );
                  },
                  child: Container(
                    padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 16),
                    decoration: BoxDecoration(
                      color: const Color(0xFFF0FFF0),
                      borderRadius: BorderRadius.circular(12),
                      border: Border.all(color: Colors.green.shade200),
                    ),
                    child: Column(
                      children: [
                        Row(
                          mainAxisSize: MainAxisSize.min,
                          children: [
                            const Text('Delivery OTP', style: TextStyle(color: Colors.grey, fontSize: 12)),
                            const SizedBox(width: 8),
                            Icon(Icons.copy, size: 14, color: Colors.grey.shade400),
                          ],
                        ),
                        const SizedBox(height: 8),
                        Text(
                          otpCode!,
                          style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 20, fontFamily: 'monospace', letterSpacing: 4, color: Colors.green),
                        ),
                        const SizedBox(height: 4),
                        Text(
                          'Share this with the rider when they arrive',
                          style: TextStyle(fontSize: 11, color: Colors.grey.shade600),
                        ),
                      ],
                    ),
                  ),
                )
              else if (!isFailed && !pending)
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
                  decoration: BoxDecoration(
                    color: Colors.orange.shade50,
                    borderRadius: BorderRadius.circular(10),
                    border: Border.all(color: Colors.orange.shade200),
                  ),
                  child: Row(
                    children: [
                      Icon(Icons.info_outline, size: 18, color: Colors.orange.shade700),
                      const SizedBox(width: 10),
                      Expanded(
                        child: Text(
                          'Your delivery OTP will appear here once a rider is assigned to your order.',
                          style: TextStyle(fontSize: 12, color: Colors.orange.shade700),
                        ),
                      ),
                    ],
                  ),
                ),
              const Spacer(),
              if (isFailed) ...[
                SizedBox(
                  width: double.infinity,
                  height: 55,
                  child: ElevatedButton(
                    style: ElevatedButton.styleFrom(
                      backgroundColor: AppTheme.blackAccent,
                      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                    ),
                    onPressed: () {
                      Navigator.pushAndRemoveUntil(
                        context,
                        MaterialPageRoute(builder: (_) => const CartScreen()),
                        (route) => route.isFirst,
                      );
                    },
                    child: const Row(
                      mainAxisAlignment: MainAxisAlignment.center,
                      children: [
                        Icon(Icons.shopping_cart, color: Colors.white),
                        SizedBox(width: 8),
                        Text('Back to Cart', style: TextStyle(color: Colors.white, fontSize: 16, fontWeight: FontWeight.bold)),
                      ],
                    ),
                  ),
                ),
                const SizedBox(height: 16),
              ],
              if (!isFailed) ...[
                SizedBox(
                  width: double.infinity,
                  height: 55,
                  child: ElevatedButton(
                    style: ElevatedButton.styleFrom(
                      backgroundColor: Colors.green,
                      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                    ),
                    onPressed: () {
                      Navigator.popUntil(context, (route) => route.isFirst);
                    },
                    child: const Row(
                      mainAxisAlignment: MainAxisAlignment.center,
                      children: [
                        Icon(Icons.local_shipping, color: Colors.white),
                        SizedBox(width: 8),
                        Text('Track Order', style: TextStyle(color: Colors.white, fontSize: 16, fontWeight: FontWeight.bold)),
                      ],
                    ),
                  ),
                ),
                const SizedBox(height: 16),
              ],
              SizedBox(
                width: double.infinity,
                height: 55,
                child: OutlinedButton(
                  style: OutlinedButton.styleFrom(
                    side: const BorderSide(color: AppTheme.blackAccent),
                    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                  ),
                  onPressed: () {
                    Navigator.popUntil(context, (route) => route.isFirst);
                  },
                  child: const Row(
                    mainAxisAlignment: MainAxisAlignment.center,
                    children: [
                      Icon(Icons.home, color: AppTheme.blackAccent),
                      SizedBox(width: 8),
                      Text('Back to Home', style: TextStyle(color: AppTheme.blackAccent, fontSize: 16, fontWeight: FontWeight.bold)),
                    ],
                  ),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
