import 'dart:convert';
import 'dart:io';
import 'package:flutter/material.dart';
import 'package:image_picker/image_picker.dart';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';
import '../../../../core/di/service_locator.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/network/api_client.dart';

/// OrderDetailScreen displays the full breakdown of a single order:
/// products, store info, rider info, payment details, a visual
/// status timeline, and a secure dispute/confirm flow.
class OrderDetailScreen extends StatefulWidget {

  const OrderDetailScreen({super.key, required this.order});
  final Map<String, dynamic> order;

  @override
  State<OrderDetailScreen> createState() => _OrderDetailScreenState();
}

class _OrderDetailScreenState extends State<OrderDetailScreen> {
  late Map<String, dynamic> _currentOrder;
  bool _isSubmitting = false;
  File? _disputeImage;
  final _reasonController = TextEditingController();

  @override
  void initState() {
    super.initState();
    _currentOrder = Map<String, dynamic>.from(widget.order);
  }

  Future<void> _pickDisputeImage() async {
    final picker = ImagePicker();
    final pickedFile = await picker.pickImage(
      source: ImageSource.camera,
      maxWidth: 1080,
      maxHeight: 1080,
      imageQuality: 85,
    );

    if (pickedFile != null) {
      setState(() {
        _disputeImage = File(pickedFile.path);
      });
    }
  }

  Future<String?> _uploadDisputePhoto(File imageFile) async {
    try {
      final uri = Uri.parse('${ApiEndpoints.deliveryBase}/delivery/gig/upload-proof');
      final request = http.MultipartRequest('POST', uri);
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';
      if (token.isNotEmpty) {
        request.headers['Authorization'] = 'Bearer $token';
      }
      request.files.add(await http.MultipartFile.fromPath('photo', imageFile.path));

      final streamedResponse = await request.send().timeout(const Duration(seconds: 30));
      final response = await http.Response.fromStream(streamedResponse);

      if (response.statusCode == 200) {
        final data = jsonDecode(response.body);
        return data['photo_url'] as String;
      }
    } catch (e) {
      debugPrint('Error uploading photo: $e');
    }
    return null;
  }

  Future<void> _submitDispute() async {
    if (_disputeImage == null) {
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Please take a photo of the product as proof.')),
      );
      return;
    }

    final reason = _reasonController.text.trim();
    if (reason.isEmpty) {
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Please enter a reason for the dispute.')),
      );
      return;
    }

    setState(() {
      _isSubmitting = true;
    });

    // 1. Upload the photo first
    final photoUrl = await _uploadDisputePhoto(_disputeImage!);
    if (photoUrl == null) {
      setState(() {
        _isSubmitting = false;
      });
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Failed to upload proof photo. Please try again.')),
      );
      return;
    }

    // 2. Submit the dispute to backend
    try {
      final trackingId = _currentOrder['order_tracking_id'] ?? 'ORD-UNKNOWN';
      await sl<ApiClient>().post(ApiEndpoints.deliveryGigDispute(), {
        'tracking_id': trackingId,
        'photo_url': photoUrl,
        'reason': reason,
      });

      setState(() {
        _currentOrder['dispute_status'] = 'disputed';
        _isSubmitting = false;
        _disputeImage = null;
        _reasonController.clear();
      });

      if (mounted) {
        Navigator.pop(context); // Close the dispute dialog
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
            content: Text('Dispute reported successfully. Admin will review the photos.'),
            backgroundColor: Colors.redAccent,
          ),
        );
      }
    } catch (e) {
      setState(() {
        _isSubmitting = false;
      });
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Error reporting dispute: $e')),
      );
    }
  }

  Future<void> _confirmDelivery() async {
    setState(() {
      _isSubmitting = true;
    });

    try {
      final orderId = _currentOrder['order_tracking_id'] ?? 'ORD-UNKNOWN';
      await sl<ApiClient>().post(ApiEndpoints.orderConfirm(), {
        'order_tracking_id': orderId,
      });

      setState(() {
        _currentOrder['status'] = 'delivered';
        _isSubmitting = false;
      });

      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
            content: Text('Order delivery confirmed successfully!'),
            backgroundColor: Colors.green,
          ),
        );
      }
    } catch (e) {
      setState(() {
        _isSubmitting = false;
      });
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Error confirming delivery: $e')),
      );
    }
  }

  Future<void> _requestRefundOrCancel() async {
    final reasonController = TextEditingController();
    final status = (_currentOrder['status'] ?? 'pending').toString().toLowerCase();
    final canCancel = ['pending', 'paid', 'accepted', 'shipped', 'in_transit', 'picked_up'].contains(status);
    final canRefund = ['completed', 'delivered'].contains(status);

    final action = await showDialog<String>(
      context: context,
      builder: (context) => AlertDialog(
        backgroundColor: Colors.white,
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(24)),
        title: const Text('Order Issue', style: TextStyle(fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
        content: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(
              canCancel
                  ? 'This order can still be cancelled before delivery.'
                  : canRefund
                      ? 'Request a refund for this delivered order.'
                      : 'No refund or cancellation is available for this order state.',
              style: const TextStyle(color: Colors.grey, fontSize: 13),
            ),
            const SizedBox(height: 16),
            TextField(
              controller: reasonController,
              maxLines: 3,
              decoration: InputDecoration(
                hintText: 'Reason (e.g. changed my mind, item damaged)',
                hintStyle: const TextStyle(color: Colors.grey, fontSize: 13),
                filled: true,
                fillColor: Colors.grey.shade50,
                border: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(16),
                  borderSide: BorderSide(color: Colors.grey.shade300),
                ),
              ),
            ),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('Close', style: TextStyle(color: Colors.grey)),
          ),
          if (canCancel)
            ElevatedButton(
              onPressed: () => Navigator.pop(context, 'cancel'),
              style: ElevatedButton.styleFrom(backgroundColor: Colors.orange, shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12))),
              child: const Text('Cancel Order', style: TextStyle(color: Colors.white)),
            ),
          if (canRefund)
            ElevatedButton(
              onPressed: () => Navigator.pop(context, 'refund'),
              style: ElevatedButton.styleFrom(backgroundColor: Colors.redAccent, shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12))),
              child: const Text('Request Refund', style: TextStyle(color: Colors.white)),
            ),
        ],
      ),
    );

    if (action == null) return;

    setState(() => _isSubmitting = true);
    try {
      final orderId = _currentOrder['order_tracking_id'] ?? 'ORD-UNKNOWN';
      final body = {
        'order_tracking_id': orderId,
        'reason': reasonController.text.trim().isNotEmpty ? reasonController.text.trim() : 'Customer requested',
      };

      if (action == 'cancel') {
        await sl<ApiClient>().post(ApiEndpoints.customerCancelOrder(orderId), body);
        setState(() => _currentOrder['status'] = 'cancelled');
        if (mounted) {
          ScaffoldMessenger.of(context).showSnackBar(
            const SnackBar(content: Text('Order cancelled successfully'), backgroundColor: Colors.orange),
          );
        }
      } else {
        await sl<ApiClient>().post(ApiEndpoints.customerReturnOrder(orderId), body);
        if (mounted) {
          ScaffoldMessenger.of(context).showSnackBar(
            const SnackBar(content: Text('Return request submitted'), backgroundColor: Colors.green),
          );
        }
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Request failed: $e')),
        );
      }
    } finally {
      setState(() => _isSubmitting = false);
    }
  }

  void _showDisputeDialog() {
    showDialog<void>(
      context: context,
      barrierDismissible: false,
      builder: (context) {
        return StatefulBuilder(
          builder: (context, setDialogState) {
            return AlertDialog(
              backgroundColor: Colors.white,
              shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(24)),
              title: const Text(
                'Report Delivery Issue',
                style: TextStyle(fontWeight: FontWeight.bold, color: AppTheme.blackAccent),
              ),
              content: SingleChildScrollView(
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    const Text(
                      'If the order is broken, fake, or incorrect, please take a photo and explain.',
                      style: TextStyle(color: Colors.grey, fontSize: 13),
                    ),
                    const SizedBox(height: 16),
                    GestureDetector(
                      onTap: () async {
                        await _pickDisputeImage();
                        setDialogState(() {});
                      },
                      child: Container(
                        height: 160,
                        width: double.infinity,
                        decoration: BoxDecoration(
                          color: Colors.grey.shade100,
                          borderRadius: BorderRadius.circular(16),
                          border: Border.all(color: Colors.grey.shade300),
                        ),
                        child: _disputeImage != null
                            ? ClipRRect(
                                borderRadius: BorderRadius.circular(16),
                                child: Image.file(_disputeImage!, fit: BoxFit.cover),
                              )
                            : const Column(
                                mainAxisAlignment: MainAxisAlignment.center,
                                children: [
                                  Icon(Icons.camera_alt_outlined, color: Colors.grey, size: 40),
                                  SizedBox(height: 8),
                                  Text('Take Photo Proof', style: TextStyle(color: Colors.grey)),
                                ],
                              ),
                      ),
                    ),
                    const SizedBox(height: 16),
                    TextField(
                      controller: _reasonController,
                      maxLines: 3,
                      decoration: InputDecoration(
                        hintText: 'Explain the issue (e.g., Rider replaced product, items missing...)',
                        hintStyle: const TextStyle(color: Colors.grey, fontSize: 13),
                        filled: true,
                        fillColor: Colors.grey.shade50,
                        border: OutlineInputBorder(
                          borderRadius: BorderRadius.circular(16),
                          borderSide: BorderSide(color: Colors.grey.shade300),
                        ),
                      ),
                    ),
                  ],
                ),
              ),
              actions: [
                TextButton(
                  onPressed: () {
                    setState(() {
                      _disputeImage = null;
                      _reasonController.clear();
                    });
                    Navigator.pop(context);
                  },
                  child: const Text('Cancel', style: TextStyle(color: Colors.grey)),
                ),
                ElevatedButton(
                  onPressed: _isSubmitting ? null : _submitDispute,
                  style: ElevatedButton.styleFrom(
                    backgroundColor: Colors.redAccent,
                    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
                  ),
                  child: _isSubmitting
                      ? const SizedBox(width: 20, height: 20, child: CircularProgressIndicator(color: Colors.white, strokeWidth: 2))
                      : const Text('Submit Report', style: TextStyle(color: Colors.white)),
                ),
              ],
            );
          },
        );
      },
    );
  }

  void _showRatingDialog(String productId, String vendorId) {
    int selectedRating = 5;
    final commentController = TextEditingController();
    bool isSubmittingRating = false;

    showDialog<void>(
      context: context,
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, setDialogState) => AlertDialog(
          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(24)),
          title: const Text('Rate Vendor & Order', style: TextStyle(fontWeight: FontWeight.bold)),
          content: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Text('How was your experience with this vendor?', style: TextStyle(color: Colors.grey, fontSize: 13)),
              const SizedBox(height: 12),
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
                  hintText: 'Share feedback about the vendor & delivery...',
                  border: OutlineInputBorder(borderRadius: BorderRadius.circular(16)),
                ),
              ),
            ],
          ),
          actions: [
            TextButton(
              onPressed: isSubmittingRating ? null : () => Navigator.pop(ctx),
              child: const Text('Cancel'),
            ),
            ElevatedButton(
              onPressed: isSubmittingRating
                  ? null
                  : () async {
                      setDialogState(() => isSubmittingRating = true);
                      try {
                        final prefs = await SharedPreferences.getInstance();
                        final token = prefs.getString('jwt_token') ?? '';
                        final response = await http.post(
                          Uri.parse(ApiEndpoints.ratingCreate()),
                          headers: {
                            'Content-Type': 'application/json',
                            'Authorization': 'Bearer $token',
                          },
                          body: jsonEncode({
                            'product_tracking_id': productId.isNotEmpty ? productId : 'PROD-ORDER',
                            'target_user_tracking_id': vendorId,
                            'rating': selectedRating,
                            'comment': commentController.text.trim(),
                          }),
                        ).timeout(const Duration(seconds: 8));

                        if (mounted) {
                          Navigator.pop(ctx);
                          if (response.statusCode == 200 || response.statusCode == 201) {
                            ScaffoldMessenger.of(context).showSnackBar(
                              const SnackBar(
                                content: Text('Rating submitted successfully! Thank you.'),
                                backgroundColor: Colors.green,
                              ),
                            );
                          } else {
                            ScaffoldMessenger.of(context).showSnackBar(
                              SnackBar(
                                content: Text('Failed to submit rating (${response.statusCode})'),
                                backgroundColor: Colors.redAccent,
                              ),
                            );
                          }
                        }
                      } catch (e) {
                        if (mounted) {
                          Navigator.pop(ctx);
                          ScaffoldMessenger.of(context).showSnackBar(
                            SnackBar(content: Text('Error: $e'), backgroundColor: Colors.redAccent),
                          );
                        }
                      }
                    },
              style: ElevatedButton.styleFrom(
                backgroundColor: AppTheme.blackAccent,
                foregroundColor: AppTheme.limeAccent,
                shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
              ),
              child: isSubmittingRating
                  ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(color: AppTheme.limeAccent, strokeWidth: 2))
                  : const Text('Submit Rating'),
            ),
          ],
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final orderId = _currentOrder['order_tracking_id'] ?? 'ORD-UNKNOWN';
    final status = (_currentOrder['status'] ?? 'pending').toString().toLowerCase();
    final total = (_currentOrder['total_amount'] ?? 0.0).toString();
    final currency = _currentOrder['currency'] ?? 'PKR';
    final storeId = (_currentOrder['store_tracking_id'] ?? 'STOR-N/A').toString();
    final vendorId = (_currentOrder['vendor_tracking_id'] ?? 'N/A').toString();
    final riderId = _currentOrder['rider_tracking_id']?.toString();
    final items = (_currentOrder['items'] as List<dynamic>?) ?? <dynamic>[];
    final productIds = items.map((item) => (item['product_tracking_id'] ?? '').toString()).where((s) => s.isNotEmpty).toList();
    final otpCode = _currentOrder['otp_code']?.toString();
    final paymentGateway = _currentOrder['payment_gateway']?.toString();
    final paymentStatus = (_currentOrder['payment_status'] ?? 'pending').toString().toLowerCase();
    final deliveryType = _currentOrder['delivery_type']?.toString();
    final disputeStatus = _currentOrder['dispute_status']?.toString() ?? 'none';
    final adminCommission = (_currentOrder['admin_commission'] as num?)?.toDouble() ?? 0.0;
    final vendorEscrow = (_currentOrder['vendor_escrow'] as num?)?.toDouble() ?? 0.0;
    final deliveryEscrow = (_currentOrder['delivery_escrow'] as num?)?.toDouble() ?? 0.0;
    final handoverPhotoUrl = _currentOrder['handover_photo_url']?.toString();
    final handoverAt = _currentOrder['handover_at']?.toString();
    final handoverNotes = _currentOrder['handover_notes']?.toString();
    final handedByTrackingId = _currentOrder['handed_over_by_tracking_id']?.toString();

    return Scaffold(
      backgroundColor: AppTheme.bgColor,
      appBar: AppBar(
        title: const Text('Order Details', style: TextStyle(color: Colors.black, fontWeight: FontWeight.bold)),
        backgroundColor: Colors.white,
        elevation: 0,
        leading: IconButton(
          icon: const Icon(Icons.arrow_back_rounded, color: Colors.black),
          onPressed: () => Navigator.pop(context),
        ),
      ),
      body: SingleChildScrollView(
        physics: const BouncingScrollPhysics(),
        padding: const EdgeInsets.all(24),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // ── Order Header ─────────────────────────────────────
            Container(
              padding: const EdgeInsets.all(20),
              decoration: BoxDecoration(
                color: AppTheme.blackAccent,
                borderRadius: BorderRadius.circular(24),
              ),
              child: Row(
                mainAxisAlignment: MainAxisAlignment.spaceBetween,
                children: [
                  Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text('Order #$orderId',
                          style: const TextStyle(color: Colors.white, fontSize: 18, fontWeight: FontWeight.bold),),
                      const SizedBox(height: 4),
                      Text(_statusLabel(status),
                          style: TextStyle(color: _statusColor(status), fontSize: 14, fontWeight: FontWeight.bold),),
                    ],
                  ),
                  Text('PKR $total',
                      style: const TextStyle(color: AppTheme.limeAccent, fontSize: 24, fontWeight: FontWeight.w900),),
                ],
              ),
            ),
            const SizedBox(height: 24),

            // ── Dispute Resolution Status Alert ───────────────────
            if (disputeStatus != 'none') ...[
              Container(
                width: double.infinity,
                padding: const EdgeInsets.all(16),
                decoration: BoxDecoration(
                  color: Colors.redAccent.withValues(alpha: 0.1),
                  borderRadius: BorderRadius.circular(16),
                  border: Border.all(color: Colors.redAccent.withValues(alpha: 0.3)),
                ),
                child: Row(
                  children: [
                    const Icon(Icons.warning_amber_rounded, color: Colors.redAccent),
                    const SizedBox(width: 12),
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          const Text('Dispute Under Investigation', style: TextStyle(color: Colors.redAccent, fontWeight: FontWeight.bold)),
                          const SizedBox(height: 2),
                          Text(
                            disputeStatus == 'resolved_rider_guilty'
                                ? 'Resolved: Rider was found guilty and suspended. Return processing.'
                                : disputeStatus == 'resolved_vendor_guilty'
                                    ? 'Resolved: Vendor error. Refund initiated.'
                                    : 'Admin is comparing photos to resolve the scam report.',
                            style: const TextStyle(color: Colors.grey, fontSize: 12),
                          ),
                        ],
                      ),
                    ),
                  ],
                ),
              ),
              const SizedBox(height: 24),
            ],

            // ── Status Timeline ──────────────────────────────────
            const Text('Delivery Timeline', style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
            const SizedBox(height: 16),
            _buildStatusTimeline(status),
            const SizedBox(height: 32),

            // ── Customer Verification Section (On-the-spot verification) ──
            if (status == 'delivered' && disputeStatus == 'none') ...[
              Container(
                padding: const EdgeInsets.all(20),
                decoration: BoxDecoration(
                  color: Colors.white,
                  borderRadius: BorderRadius.circular(24),
                  border: Border.all(color: AppTheme.limeAccent.withValues(alpha: 0.3)),
                  boxShadow: [
                    BoxShadow(
                      color: Colors.black.withValues(alpha: 0.02),
                      blurRadius: 10,
                      offset: const Offset(0, 4),
                    ),
                  ],
                ),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    const Text('Instant Verification', style: TextStyle(fontSize: 16, fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
                    const SizedBox(height: 8),
                    const Text(
                      'Please verify the product right now. If it matches what you ordered and is in perfect condition, confirm below. If there is a scam or damage, report a dispute immediately.',
                      style: TextStyle(color: Colors.grey, fontSize: 13),
                    ),
                    const SizedBox(height: 16),
                    Row(
                      children: [
                        Expanded(
                          child: OutlinedButton(
                            onPressed: _showDisputeDialog,
                            style: OutlinedButton.styleFrom(
                              side: const BorderSide(color: Colors.redAccent),
                              shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                              padding: const EdgeInsets.symmetric(vertical: 16),
                            ),
                            child: const Text('Report Dispute', style: TextStyle(color: Colors.redAccent, fontWeight: FontWeight.bold)),
                          ),
                        ),
                        const SizedBox(width: 12),
                        Expanded(
                          child: ElevatedButton(
                            onPressed: _isSubmitting ? null : _confirmDelivery,
                            style: ElevatedButton.styleFrom(
                              backgroundColor: AppTheme.blackAccent,
                              shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                              padding: const EdgeInsets.symmetric(vertical: 16),
                            ),
                            child: _isSubmitting
                                ? const SizedBox(width: 20, height: 20, child: CircularProgressIndicator(color: Colors.white, strokeWidth: 2))
                                : const Text('Confirm & Accept', style: TextStyle(color: AppTheme.limeAccent, fontWeight: FontWeight.bold)),
                          ),
                        ),
                      ],
                    ),
                  ],
                ),
              ),
              const SizedBox(height: 24),
            ],

            // ── Rate Vendor & Order (NEW) ──────────────────────────────────
            if (status == 'completed' || status == 'delivered') ...[
              SizedBox(
                width: double.infinity,
                child: ElevatedButton.icon(
                  onPressed: () {
                    final pId = productIds.isNotEmpty ? productIds.first.toString() : '';
                    _showRatingDialog(pId, vendorId);
                  },
                  icon: const Icon(Icons.star_rate_rounded, color: Colors.amber),
                  label: const Text(
                    'Rate Vendor & Order',
                    style: TextStyle(color: AppTheme.blackAccent, fontWeight: FontWeight.bold),
                  ),
                  style: ElevatedButton.styleFrom(
                    backgroundColor: AppTheme.limeAccent,
                    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                    padding: const EdgeInsets.symmetric(vertical: 16),
                  ),
                ),
              ),
              const SizedBox(height: 12),
            ],

            // ── Refund / Cancel Action (NEW) ──────────────────────────────
            if (['pending', 'paid', 'accepted', 'shipped', 'in_transit', 'picked_up', 'completed', 'delivered'].contains(status)) ...[
              SizedBox(
                width: double.infinity,
                child: OutlinedButton.icon(
                  onPressed: _isSubmitting ? null : _requestRefundOrCancel,
                  icon: const Icon(Icons.request_page_outlined, color: Colors.orange),
                  label: Text(
                    status == 'completed' || status == 'delivered' ? 'Request Refund' : 'Cancel Order',
                    style: const TextStyle(color: Colors.orange, fontWeight: FontWeight.bold),
                  ),
                  style: OutlinedButton.styleFrom(
                    side: const BorderSide(color: Colors.orange),
                    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                    padding: const EdgeInsets.symmetric(vertical: 16),
                  ),
                ),
              ),
              const SizedBox(height: 24),
            ],

            // ── Products ──────────────────────────────────────────
            const Text('Products', style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
            const SizedBox(height: 12),
            if (productIds.isEmpty)
              const Text('No product details available.', style: TextStyle(color: Colors.grey))
            else
              ...productIds.map((pid) => _buildInfoCard(Icons.shopping_bag_outlined, 'Product', pid.toString())),
            const SizedBox(height: 24),

            // ── Store & Vendor ────────────────────────────────────
            const Text('Store & Vendor', style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
            const SizedBox(height: 12),
            _buildInfoCard(Icons.storefront_outlined, 'Store', storeId),
            _buildInfoCard(Icons.person_outline, 'Vendor', vendorId),
            const SizedBox(height: 24),

            // ── Rider ─────────────────────────────────────────────
            const Text('Rider', style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
            const SizedBox(height: 12),
            _buildInfoCard(Icons.delivery_dining_outlined, 'Assigned Rider',
                riderId != null && riderId.isNotEmpty ? riderId : 'Not yet assigned',),
            const SizedBox(height: 24),

            // ── Payment ───────────────────────────────────────────
            const Text('Payment', style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
            const SizedBox(height: 12),
            _buildInfoCard(Icons.attach_money, 'Total Amount', '$currency $total'),
            _buildInfoCard(Icons.payment_outlined, 'Payment Status', paymentStatus.toUpperCase()),
            if (paymentGateway != null && paymentGateway.isNotEmpty)
              _buildInfoCard(Icons.credit_card_outlined, 'Payment Method', paymentGateway),
            if (deliveryType != null && deliveryType.isNotEmpty)
              _buildInfoCard(Icons.local_shipping_outlined, 'Delivery Type', deliveryType),
            if (adminCommission > 0) ...[
              const SizedBox(height: 8),
              _buildInfoCard(Icons.account_balance_outlined, 'Platform Commission', '$currency ${adminCommission.toStringAsFixed(2)}'),
              _buildInfoCard(Icons.store_outlined, 'Vendor Earning', '$currency ${vendorEscrow.toStringAsFixed(2)}'),
              _buildInfoCard(Icons.delivery_dining_outlined, 'Rider Earning', '$currency ${deliveryEscrow.toStringAsFixed(2)}'),
            ],
            const SizedBox(height: 24),

            // ── Handover Audit ──────────────────────────────────
            if (handoverPhotoUrl != null && handoverPhotoUrl.isNotEmpty) ...[
              const Text('Handover', style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
              const SizedBox(height: 12),
              _buildInfoCard(Icons.camera_alt_outlined, 'Handover Photo', handoverPhotoUrl),
              if (handoverAt != null && handoverAt.isNotEmpty)
                _buildInfoCard(Icons.access_time, 'Handover At', handoverAt),
              if (handedByTrackingId != null && handedByTrackingId.isNotEmpty)
                _buildInfoCard(Icons.person_outlined, 'Handed Over By', handedByTrackingId),
              if (handoverNotes != null && handoverNotes.isNotEmpty)
                _buildInfoCard(Icons.notes_outlined, 'Handover Notes', handoverNotes),
              const SizedBox(height: 24),
            ],
            if (otpCode != null && otpCode.isNotEmpty && disputeStatus == 'none' && status != 'completed') ...[
              const SizedBox(height: 16),
              Container(
                padding: const EdgeInsets.all(16),
                decoration: BoxDecoration(
                  color: AppTheme.limeAccent.withValues(alpha: 0.2),
                  borderRadius: BorderRadius.circular(16),
                  border: Border.all(color: AppTheme.limeAccent.withValues(alpha: 0.5)),
                ),
                child: Row(
                  children: [
                    const Icon(Icons.lock_outline, color: AppTheme.blackAccent),
                    const SizedBox(width: 12),
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          const Text('Delivery OTP', style: TextStyle(fontSize: 12, color: Colors.grey)),
                          Text(otpCode, style: const TextStyle(fontSize: 20, fontWeight: FontWeight.bold, color: AppTheme.blackAccent, letterSpacing: 4)),
                        ],
                      ),
                    ),
                  ],
                ),
              ),
            ],
            const SizedBox(height: 32),
          ],
        ),
      ),
    );
  }

  Widget _buildStatusTimeline(String status) {
    // GAP-3/4 FIX: full lifecycle visibility. 'paid' shows payment captured
    // (COD confirm or gateway webhook), 'in_transit' shows the rider en route
    // between store and customer — both were invisible jumps before.
    final steps = [
      {'icon': Icons.receipt_long_outlined, 'label': 'Order Placed', 'key': 'pending'},
      {'icon': Icons.payments_outlined, 'label': 'Payment Confirmed', 'key': 'paid'},
      {'icon': Icons.store_mall_directory_outlined, 'label': 'Accepted by Store', 'key': 'accepted'},
      {'icon': Icons.local_shipping_outlined, 'label': 'Picked Up', 'key': 'shipped'},
      {'icon': Icons.delivery_dining_outlined, 'label': 'On the Way', 'key': 'in_transit'},
      {'icon': Icons.check_circle_outline, 'label': 'Delivered', 'key': 'delivered'},
    ];

    const statusOrder = [
      'pending', 'paid', 'accepted', 'shipped', 'picked_up',
      'in_transit', 'delivered', 'completed', 'cancelled',
    ];
    int currentIdx = statusOrder.indexOf(status);
    if (currentIdx < 0) currentIdx = 0;
    final isCancelled = status == 'cancelled';

    return Column(
      children: List.generate(steps.length, (i) {
        final step = steps[i];
        final stepIdx = statusOrder.indexOf(step['key'] as String);
        final isCompleted = !isCancelled && stepIdx <= currentIdx;
        final isCurrent = !isCancelled && stepIdx == currentIdx;
        final isLast = i == steps.length - 1;

        return Row(
          children: [
            Column(
              children: [
                Container(
                  width: 40,
                  height: 40,
                  decoration: BoxDecoration(
                    color: isCompleted ? AppTheme.blackAccent : (isCancelled ? Colors.redAccent.withValues(alpha: 0.1) : Colors.grey.shade200),
                    shape: BoxShape.circle,
                    border: isCurrent ? Border.all(color: AppTheme.limeAccent, width: 3) : null,
                  ),
                  child: Icon(
                    step['icon'] as IconData,
                    color: isCompleted ? AppTheme.limeAccent : (isCancelled ? Colors.redAccent : Colors.grey),
                    size: 20,
                  ),
                ),
                if (!isLast)
                  Container(
                    width: 2,
                    height: 30,
                    color: isCompleted ? AppTheme.blackAccent : Colors.grey.shade200,
                  ),
              ],
            ),
            const SizedBox(width: 16),
            Expanded(
              child: Text(
                step['label'] as String,
                style: TextStyle(
                  fontSize: 14,
                  fontWeight: isCurrent ? FontWeight.bold : FontWeight.normal,
                  color: isCompleted ? AppTheme.blackAccent : (isCancelled ? Colors.redAccent : Colors.grey),
                ),
              ),
            ),
          ],
        );
      }),
    );
  }

  Widget _buildInfoCard(IconData icon, String label, String value) {
    return Container(
      margin: const EdgeInsets.only(bottom: 12),
      padding: const EdgeInsets.all(16),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(16),
        border: Border.all(color: Colors.grey.shade100),
      ),
      child: Row(
        children: [
          Icon(icon, color: Colors.grey, size: 20),
          const SizedBox(width: 12),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(label, style: const TextStyle(fontSize: 12, color: Colors.grey)),
                const SizedBox(height: 2),
                Text(value, style: const TextStyle(fontSize: 14, fontWeight: FontWeight.bold, color: AppTheme.blackAccent)),
              ],
            ),
          ),
        ],
      ),
    );
  }

  String _statusLabel(String status) {
    switch (status) {
      case 'pending': return 'Pending Payment';
      case 'paid': return 'Paid — Awaiting Store';
      case 'accepted': return 'Accepted by Store';
      case 'shipped': return 'Out for Delivery';
      case 'in_transit': return 'In Transit';
      case 'delivered':
      case 'completed': return 'Delivered';
      case 'cancelled': return 'Cancelled';
      case 'failed':
      case 'payment_failed': return 'Payment Failed';
      case 'refunded': return 'Refunded';
      case 'returned': return 'Returned';
      default: return status.toUpperCase();
    }
  }

  Color _statusColor(String status) {
    switch (status) {
      case 'pending': return Colors.orange;
      case 'paid': return Colors.blue;
      case 'accepted': return Colors.blue;
      case 'shipped':
      case 'in_transit': return Colors.purple;
      case 'delivered':
      case 'completed': return Colors.green;
      case 'cancelled': return Colors.redAccent;
      case 'failed':
      case 'payment_failed': return Colors.red;
      case 'refunded': return Colors.orange;
      case 'returned': return Colors.deepOrange;
      default: return Colors.grey;
    }
  }
}