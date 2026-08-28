import 'package:flutter/material.dart';
import '../../../../core/theme/app_theme.dart';

class OrderSuccessScreen extends StatelessWidget {

  const OrderSuccessScreen({super.key, required this.trackingId, this.pending = false, this.failed = false});
  final String trackingId;

  /// PF-4 FIX: when true the payment is still processing at the gateway —
  /// render an honest amber "processing" state instead of a green success.
  final bool pending;

  /// When true the payment has failed — render a red "failed" state with retry options.
  final bool failed;

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
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 12),
                decoration: BoxDecoration(
                  color: const Color(0xFFF8F9FA),
                  borderRadius: BorderRadius.circular(12),
                ),
                child: Column(
                  children: [
                    const Text('Order Tracking ID', style: TextStyle(color: Colors.grey, fontSize: 12)),
                    const SizedBox(height: 4),
                    Text(trackingId, style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 16)),
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
                      // Go back to cart to retry
                      Navigator.pop(context);
                      Navigator.pop(context);
                    },
                    child: const Text('Back to Cart', style: TextStyle(color: Colors.white, fontSize: 16, fontWeight: FontWeight.bold)),
                  ),
                ),
                const SizedBox(height: 16),
              ],
              SizedBox(
                width: double.infinity,
                height: 55,
                child: ElevatedButton(
                  style: ElevatedButton.styleFrom(
                    backgroundColor: AppTheme.blackAccent,
                    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                  ),
                  onPressed: () {
                    // Just pop back to dashboard
                    Navigator.pop(context);
                  },
                  child: Text(isFailed ? 'Back to Home' : 'Track Order', style: const TextStyle(color: Colors.white, fontSize: 16, fontWeight: FontWeight.bold)),
                ),
              ),
              const SizedBox(height: 16),
              SizedBox(
                width: double.infinity,
                height: 55,
                child: TextButton(
                  onPressed: () {
                    Navigator.pop(context);
                  },
                  child: const Text('Back to Home', style: TextStyle(color: AppTheme.blackAccent, fontSize: 16, fontWeight: FontWeight.bold)),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
