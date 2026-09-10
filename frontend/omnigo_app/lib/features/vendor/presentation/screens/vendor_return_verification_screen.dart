import 'dart:io';
import 'package:flutter/material.dart';
import 'package:image_picker/image_picker.dart';
import 'package:http/http.dart' as http;
import '../../../../core/network/api_client.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/di/service_locator.dart';
import '../../../../core/theme/app_theme.dart';

/// VendorReturnVerificationScreen allows vendors to verify returned products.
/// Shows return request details, photos, and allows approve/dispute actions.
class VendorReturnVerificationScreen extends StatefulWidget {
  final Map<String, dynamic> returnRequest;
  final String vendorTrackingId;

  const VendorReturnVerificationScreen({
    super.key,
    required this.returnRequest,
    required this.vendorTrackingId,
  });

  @override
  State<VendorReturnVerificationScreen> createState() => _VendorReturnVerificationScreenState();
}

class _VendorReturnVerificationScreenState extends State<VendorReturnVerificationScreen> {
  File? _verificationPhoto;
  final _notesController = TextEditingController();
  bool _isSubmitting = false;

  Future<void> _pickPhoto() async {
    final picker = ImagePicker();
    final pickedFile = await picker.pickImage(
      source: ImageSource.camera,
      maxWidth: 1080,
      maxHeight: 1080,
      imageQuality: 85,
    );
    if (pickedFile != null) {
      setState(() => _verificationPhoto = File(pickedFile.path));
    }
  }

  Future<String?> _uploadPhoto(File imageFile) async {
    try {
      final files = [await http.MultipartFile.fromPath('photo', imageFile.path)];
      final data = await sl<ApiClient>().multipartPost(
        ApiEndpoints.deliveryGigUploadProof(),
        {},
        files,
      );
      if (data is Map<String, dynamic>) {
        return data['photo_url'] as String?;
      }
    } catch (e) {
      debugPrint('Error uploading photo: $e');
    }
    return null;
  }

  Future<void> _verifyReturn(bool verified) async {
    if (_isSubmitting) return;

    setState(() => _isSubmitting = true);
    try {
      String? photoUrl;
      if (_verificationPhoto != null) {
        photoUrl = await _uploadPhoto(_verificationPhoto!);
      }

      final returnId = widget.returnRequest['id'] ?? '';
      final body = {
        'verified': verified,
        'photo_url': photoUrl ?? '',
        'notes': _notesController.text.trim(),
      };

      await sl<ApiClient>().post(
        '/api/v1/returns/$returnId/verify',
        body,
      );

      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
            content: Text(verified ? 'Return approved. Refund will be processed.' : 'Return disputed.'),
            backgroundColor: verified ? Colors.green : Colors.orange,
          ),
        );
        Navigator.pop(context, verified ? 'approved' : 'disputed');
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Verification failed: $e'), backgroundColor: Colors.redAccent),
        );
      }
    } finally {
      setState(() => _isSubmitting = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final returnRequest = widget.returnRequest;
    final status = (returnRequest['status'] ?? 'pending').toString();
    final reason = (returnRequest['reason'] ?? 'No reason provided').toString();
    final items = returnRequest['items'] as List<dynamic>? ?? [];
    final pickupPhoto = (returnRequest['pickup_photo_url'] ?? '').toString();
    final deliveryPhoto = (returnRequest['delivery_photo_url'] ?? '').toString();

    return Scaffold(
      appBar: AppBar(
        title: const Text('Verify Return', style: TextStyle(color: Colors.white)),
        backgroundColor: AppTheme.blackAccent,
        iconTheme: const IconThemeData(color: Colors.white),
      ),
      body: SingleChildScrollView(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            // Status badge
            Container(
              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
              decoration: BoxDecoration(
                color: status == 'delivered_to_vendor' ? Colors.blue.shade50 : Colors.orange.shade50,
                borderRadius: BorderRadius.circular(20),
              ),
              child: Text(
                status.toString().replaceAll('_', ' ').toUpperCase(),
                style: TextStyle(
                  color: status == 'delivered_to_vendor' ? Colors.blue : Colors.orange,
                  fontWeight: FontWeight.bold,
                  fontSize: 12,
                ),
              ),
            ),
            const SizedBox(height: 16),

            // Reason
            const Text('Return Reason', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 16)),
            const SizedBox(height: 8),
            Text(reason, style: const TextStyle(color: Colors.grey)),
            const SizedBox(height: 24),

            // Items
            if (items.isNotEmpty) ...[
              const Text('Returned Items', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 16)),
              const SizedBox(height: 8),
              ...items.map((item) => Card(
                margin: const EdgeInsets.only(bottom: 8),
                child: ListTile(
                  leading: const Icon(Icons.inventory_2_outlined),
                  title: Text((item['name'] ?? item['product_name'] ?? 'Unknown Item').toString()),
                  subtitle: Text('Qty: ${item['quantity'] ?? 1}'),
                ),
              ),),
              const SizedBox(height: 24),
            ],

            // Rider photos
            const Text('Pickup & Delivery Photos', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 16)),
            const SizedBox(height: 8),
            Row(
              children: [
                if (pickupPhoto.isNotEmpty)
                  Expanded(
                    child: Column(
                      children: [
                        const Text('Pickup', style: TextStyle(fontSize: 12, color: Colors.grey)),
                        const SizedBox(height: 4),
                        ClipRRect(
                          borderRadius: BorderRadius.circular(8),
                          child: Image.network(pickupPhoto, height: 120, fit: BoxFit.cover),
                        ),
                      ],
                    ),
                  ),
                if (pickupPhoto.isNotEmpty && deliveryPhoto.isNotEmpty) const SizedBox(width: 12),
                if (deliveryPhoto.isNotEmpty)
                  Expanded(
                    child: Column(
                      children: [
                        const Text('Delivered to Store', style: TextStyle(fontSize: 12, color: Colors.grey)),
                        const SizedBox(height: 4),
                        ClipRRect(
                          borderRadius: BorderRadius.circular(8),
                          child: Image.network(deliveryPhoto, height: 120, fit: BoxFit.cover),
                        ),
                      ],
                    ),
                  ),
              ],
            ),
            const SizedBox(height: 24),

            // Verification photo
            const Text('Verification Photo (optional)', style: TextStyle(fontWeight: FontWeight.bold, fontSize: 16)),
            const SizedBox(height: 8),
            GestureDetector(
              onTap: _pickPhoto,
              child: Container(
                height: 150,
                width: double.infinity,
                decoration: BoxDecoration(
                  color: Colors.grey.shade100,
                  borderRadius: BorderRadius.circular(12),
                  border: Border.all(color: Colors.grey.shade300),
                ),
                child: _verificationPhoto != null
                    ? ClipRRect(
                        borderRadius: BorderRadius.circular(12),
                        child: Image.file(_verificationPhoto!, fit: BoxFit.cover),
                      )
                    : Column(
                        mainAxisAlignment: MainAxisAlignment.center,
                        children: [
                          Icon(Icons.camera_alt, size: 40, color: Colors.grey.shade400),
                          const SizedBox(height: 8),
                          Text('Tap to take photo', style: TextStyle(color: Colors.grey.shade500)),
                        ],
                      ),
              ),
            ),
            const SizedBox(height: 16),

            // Notes
            TextField(
              controller: _notesController,
              maxLines: 3,
              decoration: InputDecoration(
                hintText: 'Notes (optional)',
                hintStyle: const TextStyle(color: Colors.grey, fontSize: 13),
                filled: true,
                fillColor: Colors.grey.shade50,
                border: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(16),
                  borderSide: BorderSide(color: Colors.grey.shade300),
                ),
              ),
            ),
            const SizedBox(height: 24),

            // Action buttons
            Row(
              children: [
                Expanded(
                  child: ElevatedButton.icon(
                    onPressed: _isSubmitting ? null : () => _verifyReturn(false),
                    icon: const Icon(Icons.warning_amber, color: Colors.white),
                    label: const Text('Dispute', style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold)),
                    style: ElevatedButton.styleFrom(
                      backgroundColor: Colors.orange,
                      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                      padding: const EdgeInsets.symmetric(vertical: 16),
                    ),
                  ),
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: ElevatedButton.icon(
                    onPressed: _isSubmitting ? null : () => _verifyReturn(true),
                    icon: const Icon(Icons.check_circle, color: Colors.white),
                    label: const Text('Approve Return', style: TextStyle(color: Colors.white, fontWeight: FontWeight.bold)),
                    style: ElevatedButton.styleFrom(
                      backgroundColor: Colors.green,
                      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
                      padding: const EdgeInsets.symmetric(vertical: 16),
                    ),
                  ),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}
