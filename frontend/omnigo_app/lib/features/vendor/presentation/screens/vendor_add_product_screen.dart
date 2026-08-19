import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:http/http.dart' as http;
import 'package:image_picker/image_picker.dart';
import 'package:shared_preferences/shared_preferences.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/network/api_endpoints.dart';
import 'vendor_inventory_screen.dart' show ProductModel;

/// VendorAddProductScreen is a dual-mode form used for both:
///   - Creating a new product (POST /api/v1/vendor/products/)
///   - Editing an existing product (PUT /api/v1/vendor/products/:id)
///
/// Image upload flow:
///   1. User picks image from gallery/camera via image_picker
///   2. Image is uploaded to POST /api/v1/products/upload-image (multipart)
///   3. Backend returns `image_url` which is stored in [_uploadedImageUrl]
///   4. On submit, [_uploadedImageUrl] is sent in the product body
///
/// The mode is determined by the optional `existing` parameter. When null,
/// the screen operates in "add" mode and requires a `storeTrackingId`. When
/// provided, the screen operates in "edit" mode and pre-fills the form.
class VendorAddProductScreen extends StatefulWidget {
  const VendorAddProductScreen({
    super.key,
    required this.vendorTrackingId,
    required this.storeTrackingId,
    this.existing,
  });

  final String vendorTrackingId;
  final String storeTrackingId;
  final ProductModel? existing;

  @override
  VendorAddProductScreenState createState() => VendorAddProductScreenState();
}

class VendorAddProductScreenState extends State<VendorAddProductScreen>
    with SingleTickerProviderStateMixin {
  final _formKey = GlobalKey<FormState>();
  final _nameController = TextEditingController();
  final _skuController = TextEditingController();
  final _descriptionController = TextEditingController();
  final _priceController = TextEditingController();
  final _stockController = TextEditingController();
  final _categoryController = TextEditingController();

  bool _isSaving = false;
  bool _isFeatured = false;

  // ── Image state ────────────────────────────────────────────────────
  File? _pickedImageFile;          // locally picked file (before upload)
  String? _uploadedImageUrl;       // returned by backend after upload
  bool _isUploadingImage = false;

  final _imagePicker = ImagePicker();

  // Common catalog categories — editable text field for flexibility.
  static const List<String> _presetCategories = [
    'Shoes',
    'Apparel',
    'Electronics',
    'Groceries',
    'Home',
    'Beauty',
    'Other',
  ];

  bool get _isEditMode => widget.existing != null;

  /// URL to show in preview: prefer freshly uploaded URL, fall back to existing.
  String? get _previewImageUrl =>
      _uploadedImageUrl ?? (widget.existing?.imageUrl.isNotEmpty == true ? widget.existing!.imageUrl : null);

  @override
  void initState() {
    super.initState();
    if (_isEditMode) {
      final p = widget.existing!;
      _nameController.text = p.name;
      _skuController.text = p.sku;
      _descriptionController.text = p.description;
      _priceController.text = p.basePrice.toStringAsFixed(2);
      _stockController.text = p.stock.toString();
      _categoryController.text = p.category;
      _isFeatured = p.isFeatured;
      // keep existing URL so preview shows the current image
      _uploadedImageUrl = p.imageUrl.isNotEmpty ? p.imageUrl : null;
    }
  }

  @override
  void dispose() {
    _nameController.dispose();
    _skuController.dispose();
    _descriptionController.dispose();
    _priceController.dispose();
    _stockController.dispose();
    _categoryController.dispose();
    super.dispose();
  }

  // ── Image Picker ──────────────────────────────────────────────────

  Future<void> _pickImage(ImageSource source) async {
    try {
      final picked = await _imagePicker.pickImage(
        source: source,
        maxWidth: 1024,
        maxHeight: 1024,
        imageQuality: 85,
      );
      if (picked == null) return;

      setState(() {
        _pickedImageFile = File(picked.path);
        _uploadedImageUrl = null; // clear old URL — will re-upload
      });

      await _uploadImage(_pickedImageFile!);
    } catch (e) {
      _showError('Image pick failed: $e');
    }
  }

  Future<void> _uploadImage(File imageFile) async {
    setState(() => _isUploadingImage = true);
    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      final uri = Uri.parse('${ApiEndpoints.productBase}/products/upload-image');
      final req = http.MultipartRequest('POST', uri)
        ..headers['Authorization'] = 'Bearer $token'
        ..files.add(await http.MultipartFile.fromPath('image', imageFile.path));

      final streamed = await req.send().timeout(const Duration(seconds: 30));
      final body = await streamed.stream.bytesToString();

      if (streamed.statusCode == 200 || streamed.statusCode == 201) {
        final data = jsonDecode(body) as Map<String, dynamic>;
        final url = data['image_url'] as String? ?? data['url'] as String? ?? '';
        if (url.isNotEmpty) {
          setState(() => _uploadedImageUrl = url);
        } else {
          _showError('Upload succeeded but server returned no URL.');
        }
      } else {
        _showError('Image upload failed (${streamed.statusCode}): $body');
        setState(() => _pickedImageFile = null);
      }
    } catch (e) {
      _showError('Image upload error: $e');
      setState(() => _pickedImageFile = null);
    } finally {
      if (mounted) setState(() => _isUploadingImage = false);
    }
  }

  void _showImageSourceSheet() {
    showModalBottomSheet<void>(
      context: context,
      backgroundColor: Colors.white,
      shape: const RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(24)),
      ),
      builder: (_) => SafeArea(
        child: Padding(
          padding: const EdgeInsets.symmetric(vertical: 16),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Container(
                width: 40,
                height: 4,
                margin: const EdgeInsets.only(bottom: 20),
                decoration: BoxDecoration(
                  color: Colors.grey.shade300,
                  borderRadius: BorderRadius.circular(2),
                ),
              ),
              ListTile(
                leading: Container(
                  width: 44,
                  height: 44,
                  decoration: BoxDecoration(
                    color: AppTheme.limeAccent.withOpacity(0.15),
                    borderRadius: BorderRadius.circular(12),
                  ),
                  child: const Icon(Icons.photo_library_outlined, color: AppTheme.blackAccent),
                ),
                title: const Text('Choose from Gallery',
                    style: TextStyle(fontWeight: FontWeight.w600)),
                onTap: () {
                  Navigator.pop(context);
                  _pickImage(ImageSource.gallery);
                },
              ),
              ListTile(
                leading: Container(
                  width: 44,
                  height: 44,
                  decoration: BoxDecoration(
                    color: AppTheme.limeAccent.withOpacity(0.15),
                    borderRadius: BorderRadius.circular(12),
                  ),
                  child: const Icon(Icons.camera_alt_outlined, color: AppTheme.blackAccent),
                ),
                title: const Text('Take a Photo',
                    style: TextStyle(fontWeight: FontWeight.w600)),
                onTap: () {
                  Navigator.pop(context);
                  _pickImage(ImageSource.camera);
                },
              ),
              if (_uploadedImageUrl != null) ...[
                const Divider(height: 1, indent: 16, endIndent: 16),
                ListTile(
                  leading: Container(
                    width: 44,
                    height: 44,
                    decoration: BoxDecoration(
                      color: Colors.red.shade50,
                      borderRadius: BorderRadius.circular(12),
                    ),
                    child: const Icon(Icons.delete_outline_rounded, color: Colors.redAccent),
                  ),
                  title: const Text('Remove Image',
                      style: TextStyle(fontWeight: FontWeight.w600, color: Colors.redAccent)),
                  onTap: () {
                    Navigator.pop(context);
                    setState(() {
                      _pickedImageFile = null;
                      _uploadedImageUrl = null;
                    });
                  },
                ),
              ],
            ],
          ),
        ),
      ),
    );
  }

  // ── Form Submit ───────────────────────────────────────────────────

  Future<void> _submit() async {
    if (!_formKey.currentState!.validate()) return;

    if (_isUploadingImage) {
      _showError('Please wait for the image to finish uploading.');
      return;
    }

    setState(() => _isSaving = true);
    unawaited(HapticFeedback.mediumImpact());

    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      final headers = {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer $token',
      };

      if (_isEditMode) {
        // ── EDIT MODE: partial update via PUT ─────────────────────
        final body = <String, dynamic>{};

        final newName = _nameController.text.trim();
        if (newName.isNotEmpty && newName != widget.existing!.name) {
          body['name'] = newName;
        }
        final newDesc = _descriptionController.text.trim();
        if (newDesc != widget.existing!.description) body['description'] = newDesc;

        final newPrice = double.tryParse(_priceController.text.trim()) ?? 0.0;
        if (newPrice != widget.existing!.basePrice) body['base_price'] = newPrice;

        final newStock = int.tryParse(_stockController.text.trim()) ?? 0;
        if (newStock != widget.existing!.stock) body['stock'] = newStock;

        // Only include image_url if it has changed
        final existingUrl = widget.existing!.imageUrl;
        if (_uploadedImageUrl != null && _uploadedImageUrl != existingUrl) {
          body['image_url'] = _uploadedImageUrl;
        } else if (_uploadedImageUrl == null && existingUrl.isNotEmpty) {
          body['image_url'] = ''; // explicitly cleared
        }

        final newCategory = _categoryController.text.trim();
        if (newCategory != widget.existing!.category) body['category'] = newCategory;
        if (_isFeatured != widget.existing!.isFeatured) body['is_featured'] = _isFeatured;

        final response = await http
            .put(
              Uri.parse(ApiEndpoints.vendorProductUpdate(widget.existing!.productTrackingId)),
              headers: headers,
              body: jsonEncode(body),
            )
            .timeout(const Duration(seconds: 10));

        _handleResponse(response, 'Product updated successfully!');
      } else {
        // ── ADD MODE: create via POST ──────────────────────────────
        final body = {
          'store_tracking_id': widget.storeTrackingId,
          'sku': _skuController.text.trim(),
          'name': _nameController.text.trim(),
          'description': _descriptionController.text.trim(),
          'base_price': double.parse(_priceController.text.trim()),
          'stock': int.parse(_stockController.text.trim()),
          'image_url': _uploadedImageUrl ?? '',
          'category': _categoryController.text.trim(),
        };

        final response = await http
            .post(
              Uri.parse(ApiEndpoints.vendorProductCreate()),
              headers: headers,
              body: jsonEncode(body),
            )
            .timeout(const Duration(seconds: 10));

        _handleResponse(response, 'Product added to catalog!');
      }
    } catch (e) {
      if (mounted) {
        setState(() => _isSaving = false);
        _showError('Network error: $e');
      }
    }
  }

  void _handleResponse(http.Response response, String successMessage) {
    if (!mounted) return;
    setState(() => _isSaving = false);

    if (response.statusCode == 200 || response.statusCode == 201) {
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(
          content: Text(successMessage),
          backgroundColor: Colors.green,
          behavior: SnackBarBehavior.floating,
          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
        ),
      );
      Navigator.pop(context, true); // pop + signal refresh
    } else {
      _showError('Server rejected request (${response.statusCode}): ${response.body}');
    }
  }

  void _showError(String msg) {
    if (!mounted) return;
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(
        content: Text(msg),
        backgroundColor: Colors.redAccent,
        behavior: SnackBarBehavior.floating,
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
      ),
    );
  }

  // ── Build ─────────────────────────────────────────────────────────

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: AppTheme.bgColor,
      appBar: AppBar(
        title: Text(
          _isEditMode ? 'Edit Product' : 'Add Product',
          style: const TextStyle(color: Colors.black, fontWeight: FontWeight.bold),
        ),
        backgroundColor: Colors.white,
        elevation: 0,
        leading: IconButton(
          icon: const Icon(Icons.arrow_back_rounded, color: Colors.black),
          onPressed: () => Navigator.pop(context),
        ),
      ),
      body: SingleChildScrollView(
        physics: const BouncingScrollPhysics(),
        padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 24),
        child: Form(
          key: _formKey,
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              // ── Image Picker Card ────────────────────────────────
              _buildImagePicker(),
              const SizedBox(height: 24),

              // ── Fields ───────────────────────────────────────────
              _buildField(
                controller: _nameController,
                hint: 'Product Name',
                icon: Icons.label_outline,
                validator: (v) => v == null || v.trim().isEmpty ? 'Name is required' : null,
              ),
              const SizedBox(height: 16),
              if (!_isEditMode) ...[
                _buildField(
                  controller: _skuController,
                  hint: 'SKU (optional)',
                  icon: Icons.qr_code_2_outlined,
                ),
                const SizedBox(height: 16),
              ],
              _buildField(
                controller: _descriptionController,
                hint: 'Description',
                icon: Icons.description_outlined,
                maxLines: 3,
              ),
              const SizedBox(height: 16),
              Row(
                children: [
                  Expanded(
                    child: _buildField(
                      controller: _priceController,
                      hint: 'Base Price',
                      icon: Icons.attach_money_rounded,
                      keyboardType: TextInputType.number,
                      validator: (v) {
                        if (v == null || v.trim().isEmpty) return 'Price is required';
                        if (double.tryParse(v.trim()) == null) return 'Enter a valid number';
                        return null;
                      },
                    ),
                  ),
                  const SizedBox(width: 16),
                  Expanded(
                    child: _buildField(
                      controller: _stockController,
                      hint: 'Stock',
                      icon: Icons.inventory_outlined,
                      keyboardType: TextInputType.number,
                      validator: (v) {
                        if (v == null || v.trim().isEmpty) return 'Stock is required';
                        if (int.tryParse(v.trim()) == null) return 'Enter a valid integer';
                        return null;
                      },
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 16),
              _buildCategoryField(),
              const SizedBox(height: 20),

              // ── Featured toggle ──────────────────────────────────
              Container(
                decoration: BoxDecoration(
                  color: Colors.white,
                  borderRadius: BorderRadius.circular(18),
                  boxShadow: [
                    BoxShadow(color: Colors.black.withOpacity(0.02), blurRadius: 8, offset: const Offset(0, 4)),
                  ],
                ),
                child: SwitchListTile(
                  title: const Text('Featured Product', style: TextStyle(fontWeight: FontWeight.bold)),
                  value: _isFeatured,
                  activeColor: AppTheme.limeAccent,
                  activeTrackColor: Colors.black,
                  onChanged: (v) => setState(() => _isFeatured = v),
                  shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(18)),
                ),
              ),
              const SizedBox(height: 32),

              // ── Submit Button ────────────────────────────────────
              SizedBox(
                width: double.infinity,
                height: 56,
                child: ElevatedButton(
                  onPressed: (_isSaving || _isUploadingImage) ? null : _submit,
                  style: ElevatedButton.styleFrom(
                    backgroundColor: AppTheme.blackAccent,
                    foregroundColor: AppTheme.limeAccent,
                    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(20)),
                    elevation: 0,
                  ),
                  child: (_isSaving || _isUploadingImage)
                      ? const SizedBox(
                          width: 24,
                          height: 24,
                          child: CircularProgressIndicator(color: AppTheme.limeAccent, strokeWidth: 2.5),
                        )
                      : Text(
                          _isEditMode ? 'Save Changes' : 'Add to Catalog',
                          style: const TextStyle(fontSize: 16, fontWeight: FontWeight.bold),
                        ),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }

  // ── Image Picker Widget ───────────────────────────────────────────

  Widget _buildImagePicker() {
    final hasLocalFile = _pickedImageFile != null;
    final hasRemoteUrl = _uploadedImageUrl != null && _uploadedImageUrl!.isNotEmpty;

    return GestureDetector(
      onTap: _isUploadingImage ? null : _showImageSourceSheet,
      child: AnimatedContainer(
        duration: const Duration(milliseconds: 300),
        curve: Curves.easeInOut,
        height: hasLocalFile || hasRemoteUrl ? 200 : 130,
        decoration: BoxDecoration(
          color: Colors.white,
          borderRadius: BorderRadius.circular(20),
          border: Border.all(
            color: hasLocalFile || hasRemoteUrl
                ? AppTheme.limeAccent
                : Colors.grey.shade200,
            width: hasLocalFile || hasRemoteUrl ? 2 : 1.5,
          ),
          boxShadow: [
            BoxShadow(
              color: Colors.black.withOpacity(0.04),
              blurRadius: 12,
              offset: const Offset(0, 4),
            ),
          ],
        ),
        clipBehavior: Clip.antiAlias,
        child: _buildImagePickerContent(hasLocalFile, hasRemoteUrl),
      ),
    );
  }

  Widget _buildImagePickerContent(bool hasLocalFile, bool hasRemoteUrl) {
    // Uploading spinner overlay
    if (_isUploadingImage) {
      return Stack(
        fit: StackFit.expand,
        children: [
          if (hasLocalFile)
            Image.file(_pickedImageFile!, fit: BoxFit.cover),
          Container(
            color: Colors.black.withOpacity(0.45),
            child: const Column(
              mainAxisAlignment: MainAxisAlignment.center,
              children: [
                CircularProgressIndicator(color: Colors.white, strokeWidth: 2.5),
                SizedBox(height: 12),
                Text('Uploading…',
                    style: TextStyle(color: Colors.white, fontWeight: FontWeight.w600)),
              ],
            ),
          ),
        ],
      );
    }

    // Show local file preview
    if (hasLocalFile) {
      return Stack(
        fit: StackFit.expand,
        children: [
          Image.file(_pickedImageFile!, fit: BoxFit.cover),
          // Upload success badge
          if (hasRemoteUrl)
            Positioned(
              top: 10,
              right: 10,
              child: Container(
                padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 5),
                decoration: BoxDecoration(
                  color: Colors.green,
                  borderRadius: BorderRadius.circular(20),
                ),
                child: const Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    Icon(Icons.cloud_done_rounded, color: Colors.white, size: 14),
                    SizedBox(width: 4),
                    Text('Uploaded', style: TextStyle(color: Colors.white, fontSize: 11, fontWeight: FontWeight.bold)),
                  ],
                ),
              ),
            ),
          // Edit overlay
          Positioned(
            bottom: 0,
            left: 0,
            right: 0,
            child: Container(
              padding: const EdgeInsets.symmetric(vertical: 8),
              color: Colors.black.withOpacity(0.5),
              child: const Row(
                mainAxisAlignment: MainAxisAlignment.center,
                children: [
                  Icon(Icons.edit_outlined, color: Colors.white, size: 14),
                  SizedBox(width: 6),
                  Text('Tap to change', style: TextStyle(color: Colors.white, fontSize: 12)),
                ],
              ),
            ),
          ),
        ],
      );
    }

    // Show remote URL preview (edit mode existing image)
    if (hasRemoteUrl) {
      return Stack(
        fit: StackFit.expand,
        children: [
          Image.network(
            _uploadedImageUrl!,
            fit: BoxFit.cover,
            errorBuilder: (_, __, ___) => _buildEmptyPickerState(),
          ),
          Positioned(
            bottom: 0,
            left: 0,
            right: 0,
            child: Container(
              padding: const EdgeInsets.symmetric(vertical: 8),
              color: Colors.black.withOpacity(0.5),
              child: const Row(
                mainAxisAlignment: MainAxisAlignment.center,
                children: [
                  Icon(Icons.edit_outlined, color: Colors.white, size: 14),
                  SizedBox(width: 6),
                  Text('Tap to change', style: TextStyle(color: Colors.white, fontSize: 12)),
                ],
              ),
            ),
          ),
        ],
      );
    }

    // Empty state
    return _buildEmptyPickerState();
  }

  Widget _buildEmptyPickerState() {
    return Column(
      mainAxisAlignment: MainAxisAlignment.center,
      children: [
        Container(
          width: 52,
          height: 52,
          decoration: BoxDecoration(
            color: Colors.grey.shade100,
            borderRadius: BorderRadius.circular(16),
          ),
          child: Icon(Icons.add_photo_alternate_outlined,
              size: 28, color: Colors.grey.shade500),
        ),
        const SizedBox(height: 10),
        Text(
          'Add Product Image',
          style: TextStyle(
            fontWeight: FontWeight.w700,
            fontSize: 14,
            color: Colors.grey.shade700,
          ),
        ),
        const SizedBox(height: 4),
        Text(
          'Gallery or Camera',
          style: TextStyle(fontSize: 12, color: Colors.grey.shade400),
        ),
      ],
    );
  }

  // ── Reusable Field ────────────────────────────────────────────────

  Widget _buildField({
    required TextEditingController controller,
    required String hint,
    required IconData icon,
    bool obscureText = false,
    int maxLines = 1,
    TextInputType keyboardType = TextInputType.text,
    String? Function(String?)? validator,
  }) {
    return Container(
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(18),
        boxShadow: [
          BoxShadow(color: Colors.black.withOpacity(0.02), blurRadius: 8, offset: const Offset(0, 4)),
        ],
      ),
      child: TextFormField(
        controller: controller,
        obscureText: obscureText,
        maxLines: maxLines,
        keyboardType: keyboardType,
        validator: validator,
        style: const TextStyle(color: AppTheme.blackAccent, fontSize: 14, fontWeight: FontWeight.bold),
        decoration: InputDecoration(
          prefixIcon: Icon(icon, color: Colors.grey.shade400, size: 20),
          hintText: hint,
          hintStyle: TextStyle(color: Colors.grey.shade400, fontSize: 14, fontWeight: FontWeight.normal),
          border: OutlineInputBorder(
            borderRadius: BorderRadius.circular(18),
            borderSide: BorderSide.none,
          ),
          contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 16),
        ),
      ),
    );
  }

  Widget _buildCategoryField() {
    return Container(
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(18),
        boxShadow: [
          BoxShadow(color: Colors.black.withOpacity(0.02), blurRadius: 8, offset: const Offset(0, 4)),
        ],
      ),
      child: TextFormField(
        controller: _categoryController,
        style: const TextStyle(color: AppTheme.blackAccent, fontSize: 14, fontWeight: FontWeight.bold),
        decoration: InputDecoration(
          prefixIcon: Icon(Icons.category_outlined, color: Colors.grey.shade400, size: 20),
          hintText: 'Category',
          hintStyle: TextStyle(color: Colors.grey.shade400, fontSize: 14, fontWeight: FontWeight.normal),
          border: OutlineInputBorder(
            borderRadius: BorderRadius.circular(18),
            borderSide: BorderSide.none,
          ),
          contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 16),
          suffixIcon: PopupMenuButton<String>(
            icon: const Icon(Icons.arrow_drop_down_rounded, color: Colors.grey),
            onSelected: (value) => setState(() => _categoryController.text = value),
            itemBuilder: (_) => _presetCategories
                .map((c) => PopupMenuItem(value: c, child: Text(c)))
                .toList(),
          ),
        ),
      ),
    );
  }
}