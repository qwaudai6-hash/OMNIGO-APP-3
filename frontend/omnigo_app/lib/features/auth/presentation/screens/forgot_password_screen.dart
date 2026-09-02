import 'dart:async';

import 'package:flutter/material.dart';
import 'package:qr_flutter/qr_flutter.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/network/api_endpoints.dart';

/// ForgotPasswordScreen — two-step flow:
///
///   1. User enters their email → POST /auth/forgot-password
///      Backend returns a token (in dev) or emails the reset link
///      (in production). The screen deep-links to the reset URL.
///   2. User opens the reset link, lands on this screen with the
///      token pre-filled. They enter a new password and submit.
///      POST /auth/reset-password with the token + new password.
///
/// In production we would never show the token in the response. We
/// surface it here so the dev experience is self-contained.
class ForgotPasswordScreen extends StatefulWidget {
  const ForgotPasswordScreen({super.key, this.token});

  /// Pre-filled token when the user clicks the link from their email.
  final String? token;

  @override
  State<ForgotPasswordScreen> createState() => _ForgotPasswordScreenState();
}

class _ForgotPasswordScreenState extends State<ForgotPasswordScreen> {
  final _emailController = TextEditingController();
  final _passwordController = TextEditingController();
  final _confirmController = TextEditingController();
  late final TextEditingController _tokenController =
      TextEditingController(text: widget.token ?? '');
  final _formKey = GlobalKey<FormState>();
  bool _isLoading = false;
  String _stage = 'request'; // 'request' | 'reset' | 'done'
  String? _returnedToken;

  Future<void> _requestReset() async {
    if (!_formKey.currentState!.validate()) return;
    setState(() => _isLoading = true);
    try {
      final api = ApiClient();
      final resp = await api.post(
        ApiEndpoints.authForgotPassword(),
        {'email': _emailController.text.trim()},
      );
      final token = resp is Map ? resp['reset_token']?.toString() : null;
      if (!mounted) return;
      setState(() {
        _returnedToken = token;
        _stage = 'reset';
      });
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed: $e')),
        );
      }
    } finally {
      if (mounted) setState(() => _isLoading = false);
    }
  }

  Future<void> _confirmReset() async {
    if (!_formKey.currentState!.validate()) return;
    setState(() => _isLoading = true);
    try {
      final api = ApiClient();
      await api.post(
        ApiEndpoints.authResetPassword(),
        {
          'token': _tokenController.text.trim(),
          'new_password': _passwordController.text,
        },
      );
      if (!mounted) return;
      setState(() => _stage = 'done');
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
            content: Text('Password reset successful. Sign in.'),
            backgroundColor: Colors.green,
          ),
        );
        Navigator.of(context).pop();
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Reset failed: $e')),
        );
      }
    } finally {
      if (mounted) setState(() => _isLoading = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.white,
      appBar: AppBar(
        backgroundColor: Colors.white,
        elevation: 0,
        foregroundColor: Colors.black,
        title: const Text('Forgot Password'),
      ),
      body: SafeArea(
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(24),
          child: _stage == 'request'
              ? _buildRequestStage()
              : _stage == 'reset'
                  ? _buildResetStage()
                  : _buildDoneStage(),
        ),
      ),
    );
  }

  Widget _buildRequestStage() {
    return Form(
      key: _formKey,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const SizedBox(height: 24),
          const Icon(Icons.lock_open_outlined, size: 64, color: Colors.black),
          const SizedBox(height: 16),
          const Text(
            'Enter your email and we will send a reset link.',
            textAlign: TextAlign.center,
            style: TextStyle(fontSize: 16),
          ),
          const SizedBox(height: 32),
          TextFormField(
            controller: _emailController,
            keyboardType: TextInputType.emailAddress,
            decoration: const InputDecoration(
              labelText: 'Email',
              border: OutlineInputBorder(),
            ),
            validator: (v) => (v == null || !v.contains('@')) ? 'Invalid email' : null,
          ),
          const SizedBox(height: 24),
          ElevatedButton(
            onPressed: _isLoading ? null : _requestReset,
            style: ElevatedButton.styleFrom(
              backgroundColor: Colors.black,
              foregroundColor: const Color(0xFFCAFF33),
              padding: const EdgeInsets.symmetric(vertical: 16),
            ),
            child: _isLoading
                ? const SizedBox(
                    height: 20,
                    width: 20,
                    child: CircularProgressIndicator(
                      strokeWidth: 2,
                      color: Color(0xFFCAFF33),
                    ),
                  )
                : const Text('Send Reset Link'),
          ),
        ],
      ),
    );
  }

  Widget _buildResetStage() {
    return Form(
      key: _formKey,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const SizedBox(height: 16),
          const Text(
            'A reset link has been sent. In dev, the token is:',
            textAlign: TextAlign.center,
          ),
          if (_returnedToken != null) ...[
            const SizedBox(height: 12),
            Container(
              padding: const EdgeInsets.all(12),
              decoration: BoxDecoration(
                color: Colors.grey.shade100,
                borderRadius: BorderRadius.circular(8),
              ),
              child: SelectableText(
                _returnedToken!,
                style: const TextStyle(fontFamily: 'monospace', fontSize: 12),
              ),
            ),
          ],
          const SizedBox(height: 24),
          TextFormField(
            controller: _tokenController,
            decoration: const InputDecoration(
              labelText: 'Reset Token',
              border: OutlineInputBorder(),
            ),
            validator: (v) => v == null || v.isEmpty ? 'Token required' : null,
          ),
          const SizedBox(height: 16),
          TextFormField(
            controller: _passwordController,
            obscureText: true,
            decoration: const InputDecoration(
              labelText: 'New Password',
              border: OutlineInputBorder(),
            ),
            validator: (v) => (v == null || v.length < 6)
                ? 'At least 6 characters'
                : null,
          ),
          const SizedBox(height: 16),
          TextFormField(
            controller: _confirmController,
            obscureText: true,
            decoration: const InputDecoration(
              labelText: 'Confirm Password',
              border: OutlineInputBorder(),
            ),
            validator: (v) => v != _passwordController.text
                ? 'Passwords do not match'
                : null,
          ),
          const SizedBox(height: 24),
          ElevatedButton(
            onPressed: _isLoading ? null : _confirmReset,
            style: ElevatedButton.styleFrom(
              backgroundColor: Colors.black,
              foregroundColor: const Color(0xFFCAFF33),
              padding: const EdgeInsets.symmetric(vertical: 16),
            ),
            child: _isLoading
                ? const SizedBox(
                    height: 20,
                    width: 20,
                    child: CircularProgressIndicator(
                      strokeWidth: 2,
                      color: Color(0xFFCAFF33),
                    ),
                  )
                : const Text('Reset Password'),
          ),
        ],
      ),
    );
  }

  Widget _buildDoneStage() {
    return Column(
      children: [
        const SizedBox(height: 48),
        const Icon(Icons.check_circle_outline, color: Colors.green, size: 96),
        const SizedBox(height: 16),
        const Text(
          'Password Reset',
          style: TextStyle(fontSize: 24, fontWeight: FontWeight.bold),
        ),
        const SizedBox(height: 8),
        const Text(
          'Your password has been changed. Please sign in with your new password.',
          textAlign: TextAlign.center,
        ),
        const SizedBox(height: 32),
        ElevatedButton(
          onPressed: () => Navigator.of(context).pop(),
          style: ElevatedButton.styleFrom(
            backgroundColor: Colors.black,
            foregroundColor: const Color(0xFFCAFF33),
          ),
          child: const Text('Back to Login'),
        ),
      ],
    );
  }
}

/// TwoFactorSetupScreen — let the user enable TOTP 2FA. We POST
/// /auth/2fa/enroll and display the QR code; the user scans it with
/// Google Authenticator, types a current code, and POST
/// /auth/2fa/verify-enrollment. The secret is shown in plain text
/// as a fallback for Authenticator import via typing.
class TwoFactorSetupScreen extends StatefulWidget {
  const TwoFactorSetupScreen({super.key});

  @override
  State<TwoFactorSetupScreen> createState() => _TwoFactorSetupScreenState();
}

class _TwoFactorSetupScreenState extends State<TwoFactorSetupScreen> {
  final _codeController = TextEditingController();
  bool _isLoading = false;
  String? _secret;
  String? _otpauthURL;
  bool _secretRevealed = false;

  @override
  void initState() {
    super.initState();
    _enroll();
  }

  Future<void> _enroll() async {
    setState(() => _isLoading = true);
    try {
      final api = ApiClient();
      final resp = await api.post(ApiEndpoints.auth2FAEnroll(), {});
      final data = resp as Map<String, dynamic>;
      if (!mounted) return;
      setState(() {
        _secret = data['secret']?.toString();
        _otpauthURL = data['otpauth_url']?.toString();
      });
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('2FA enrollment failed: $e')),
        );
      }
    } finally {
      if (mounted) setState(() => _isLoading = false);
    }
  }

  Future<void> _verify() async {
    if (_codeController.text.length != 6) {
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Enter the 6-digit code from the app')),
      );
      return;
    }
    setState(() => _isLoading = true);
    try {
      final api = ApiClient();
      await api.post(
        ApiEndpoints.auth2FAVerifyEnrollment(),
        {'code': _codeController.text.trim()},
      );
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
            content: Text('2FA enabled successfully'),
            backgroundColor: Colors.green,
          ),
        );
        Navigator.of(context).pop(true);
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Invalid code: $e')),
        );
      }
    } finally {
      if (mounted) setState(() => _isLoading = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.white,
      appBar: AppBar(
        backgroundColor: Colors.white,
        elevation: 0,
        foregroundColor: Colors.black,
        title: const Text('Enable 2FA'),
      ),
      body: SafeArea(
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(24),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const SizedBox(height: 16),
              const Text(
                'Step 1: Scan this QR code in Google Authenticator, Authy, or 1Password.',
                textAlign: TextAlign.center,
                style: TextStyle(fontSize: 14),
              ),
              const SizedBox(height: 24),
              if (_otpauthURL != null)
                Center(
                  child: Container(
                    padding: const EdgeInsets.all(16),
                    color: Colors.white,
                    child: QrImageView(
                      data: _otpauthURL!,
                      size: 220,
                      backgroundColor: Colors.white,
                    ),
                  ),
                ),
              const SizedBox(height: 16),
              if (_secret != null)
                Center(
                  child: GestureDetector(
                    onTap: () => setState(() => _secretRevealed = !_secretRevealed),
                    child: Container(
                      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
                      decoration: BoxDecoration(
                        color: Colors.grey.shade100,
                        borderRadius: BorderRadius.circular(8),
                      ),
                      child: Text(
                        _secretRevealed ? _secret! : 'tap to reveal secret',
                        style: TextStyle(
                          fontFamily: 'monospace',
                          fontSize: 12,
                          color: _secretRevealed ? Colors.black : Colors.grey,
                        ),
                      ),
                    ),
                  ),
                ),
              const SizedBox(height: 32),
              const Text(
                'Step 2: Enter the 6-digit code generated by the app.',
                textAlign: TextAlign.center,
                style: TextStyle(fontSize: 14),
              ),
              const SizedBox(height: 16),
              TextField(
                controller: _codeController,
                keyboardType: TextInputType.number,
                maxLength: 6,
                decoration: const InputDecoration(
                  labelText: 'Verification Code',
                  border: OutlineInputBorder(),
                  counterText: '',
                ),
              ),
              const SizedBox(height: 24),
              ElevatedButton(
                onPressed: _isLoading ? null : _verify,
                style: ElevatedButton.styleFrom(
                  backgroundColor: Colors.black,
                  foregroundColor: const Color(0xFFCAFF33),
                  padding: const EdgeInsets.symmetric(vertical: 16),
                ),
                child: _isLoading
                    ? const SizedBox(
                        height: 20,
                        width: 20,
                        child: CircularProgressIndicator(
                          strokeWidth: 2,
                          color: Color(0xFFCAFF33),
                        ),
                      )
                    : const Text('Enable 2FA'),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
