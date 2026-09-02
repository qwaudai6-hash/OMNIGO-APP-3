import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';
import '../../../../core/theme/app_theme.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/services/session_registry.dart';

/// EditProfileScreen allows the authenticated customer to update their
/// full name, phone, and address. Email is read-only (unique constraint
/// — changing it requires a re-verification flow, out of scope).
///
/// On submit:
///   1. PATCH /api/v1/auth/profile with the changed fields.
///   2. On 200, refresh SessionRegistry via updateProfile().
///   3. Pop back to the profile tab.
class EditProfileScreen extends StatefulWidget {
  const EditProfileScreen({super.key});

  @override
  EditProfileScreenState createState() => EditProfileScreenState();
}

class EditProfileScreenState extends State<EditProfileScreen> {
  final _formKey = GlobalKey<FormState>();
  final _nameController = TextEditingController();
  final _phoneController = TextEditingController();
  final _addressController = TextEditingController();

  bool _isSaving = false;

  @override
  void initState() {
    super.initState();
    // Pre-fill from the in-memory session cache.
    _nameController.text = SessionRegistry.instance.fullName ?? '';
    _phoneController.text = SessionRegistry.instance.phone ?? '';
    _addressController.text = SessionRegistry.instance.address ?? '';
  }

  @override
  void dispose() {
    _nameController.dispose();
    _phoneController.dispose();
    _addressController.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    if (!_formKey.currentState!.validate()) return;
    setState(() => _isSaving = true);
    unawaited(HapticFeedback.mediumImpact());

    try {
      final prefs = await SharedPreferences.getInstance();
      final token = prefs.getString('jwt_token') ?? '';

      // Build partial-update body — only send fields that changed.
      final body = <String, dynamic>{};

      final newName = _nameController.text.trim();
      if (newName.isNotEmpty && newName != SessionRegistry.instance.fullName) {
        body['full_name'] = newName;
      }
      final newPhone = _phoneController.text.trim();
      if (newPhone != (SessionRegistry.instance.phone ?? '')) {
        body['phone'] = newPhone;
      }
      final newAddress = _addressController.text.trim();
      if (newAddress != (SessionRegistry.instance.address ?? '')) {
        body['address'] = newAddress;
      }

      if (body.isEmpty) {
        // Nothing changed — just pop.
        if (mounted) Navigator.pop(context);
        return;
      }

      final response = await http
          .patch(
            Uri.parse('${ApiEndpoints.authBase}/auth/profile'),
            headers: {
              'Content-Type': 'application/json',
              'Authorization': 'Bearer $token',
            },
            body: jsonEncode(body),
          )
          .timeout(const Duration(seconds: 10));

      if (!mounted) return;

      if (response.statusCode == 200) {
        final data = jsonDecode(response.body) as Map<String, dynamic>;
        // Refresh the in-memory + persistent session cache.
        await SessionRegistry.instance.updateProfile(
          fullName: data['full_name']?.toString(),
          phone: data['phone']?.toString(),
          address: data['address']?.toString(),
        );

        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
            content: Text('Profile updated successfully!'),
            backgroundColor: Colors.green,
            behavior: SnackBarBehavior.floating,
          ),
        );
        Navigator.pop(context, true);
      } else {
        if (!mounted) return;
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
            content: Text('Update failed (${response.statusCode}): ${response.body}'),
            backgroundColor: Colors.redAccent,
            behavior: SnackBarBehavior.floating,
          ),
        );
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
            content: Text('Network error: $e'),
            backgroundColor: Colors.redAccent,
            behavior: SnackBarBehavior.floating,
          ),
        );
      }
    } finally {
      if (mounted) setState(() => _isSaving = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: AppTheme.bgColor,
      appBar: AppBar(
        title: const Text('Edit Profile', style: TextStyle(color: Colors.black, fontWeight: FontWeight.bold)),
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
              // Avatar header
              Center(
                child: Container(
                  padding: const EdgeInsets.all(20),
                  decoration: const BoxDecoration(
                    color: AppTheme.blackAccent,
                    shape: BoxShape.circle,
                  ),
                  child: const Icon(Icons.person, size: 48, color: AppTheme.limeAccent),
                ),
              ),
              const SizedBox(height: 32),

              // Email (read-only)
              _buildReadOnlyField(
                label: 'Email (read-only)',
                value: SessionRegistry.instance.email ?? 'Not provided',
                icon: Icons.email_outlined,
              ),
              const SizedBox(height: 16),

              // Full Name
              _buildField(
                controller: _nameController,
                hint: 'Full Name',
                icon: Icons.person_outline_rounded,
                validator: (v) => v == null || v.trim().isEmpty ? 'Full name is required' : null,
              ),
              const SizedBox(height: 16),

              // Phone
              _buildField(
                controller: _phoneController,
                hint: 'Phone Number',
                icon: Icons.phone_outlined,
                keyboardType: TextInputType.phone,
                validator: (v) {
                  if (v == null || v.trim().isEmpty) return 'Phone number is required';
                  final stripped = v.trim().replaceAll(RegExp(r'[\s\-\(\)]'), '');
                  if (!RegExp(r'^\+?\d{7,15}$').hasMatch(stripped)) return 'Enter a valid phone number (7–15 digits)';
                  return null;
                },
              ),
              const SizedBox(height: 16),

              // Address
              _buildField(
                controller: _addressController,
                hint: 'Delivery Address',
                icon: Icons.home_outlined,
                maxLines: 3,
                validator: (v) => v == null || v.trim().isEmpty ? 'Delivery address is required' : null,
              ),
              const SizedBox(height: 32),

              // Submit
              SizedBox(
                width: double.infinity,
                height: 56,
                child: ElevatedButton(
                  onPressed: _isSaving ? null : _submit,
                  style: ElevatedButton.styleFrom(
                    backgroundColor: AppTheme.blackAccent,
                    foregroundColor: AppTheme.limeAccent,
                    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(20)),
                    elevation: 0,
                  ),
                  child: _isSaving
                      ? const SizedBox(
                          width: 24,
                          height: 24,
                          child: CircularProgressIndicator(color: AppTheme.limeAccent, strokeWidth: 2.5),
                        )
                      : const Text('Save Changes', style: TextStyle(fontSize: 16, fontWeight: FontWeight.bold)),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }

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

  Widget _buildReadOnlyField({
    required String label,
    required String value,
    required IconData icon,
  }) {
    return Container(
      padding: const EdgeInsets.all(16),
      decoration: BoxDecoration(
        color: Colors.grey.shade100,
        borderRadius: BorderRadius.circular(18),
      ),
      child: Row(
        children: [
          Icon(icon, color: Colors.grey.shade500, size: 20),
          const SizedBox(width: 12),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(label, style: TextStyle(color: Colors.grey.shade500, fontSize: 11)),
                const SizedBox(height: 2),
                Text(value, style: const TextStyle(color: AppTheme.blackAccent, fontSize: 14, fontWeight: FontWeight.bold)),
              ],
            ),
          ),
        ],
      ),
    );
  }
}