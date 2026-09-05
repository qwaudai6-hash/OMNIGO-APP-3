import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import '../../../../core/network/api_client.dart';
import '../../../../core/services/session_registry.dart';
import 'forgot_password_screen.dart';

class LoginScreen extends StatefulWidget {
  const LoginScreen({super.key, required this.role});
  final String role;

  @override
  LoginScreenState createState() => LoginScreenState();
}

class LoginScreenState extends State<LoginScreen> {
  final _emailController = TextEditingController();
  final _passwordController = TextEditingController();
  final _formKey = GlobalKey<FormState>();
  bool _isLoading = false;
  final ApiClient _apiClient = ApiClient();

  void _login() async {
    if (!_formKey.currentState!.validate()) return;
    setState(() => _isLoading = true);
    unawaited(HapticFeedback.mediumImpact());

    try {
      final response = await _apiClient.post('/auth/login', {
        'email': _emailController.text.trim(),
        'password': _passwordController.text.trim(),
        'role': widget.role,
      });

      // 2FA gate: backend returns {requires_2fa: true, challenge_id: "..."}
      // if the user has TOTP enabled. We pop the TOTP entry dialog and
      // POST it back to /auth/2fa/challenge before issuing the session.
      if (response is Map && response['requires_2fa'] == true) {
        final challengeId = response['challenge_id']?.toString();
        if (challengeId == null) {
          throw Exception('Server returned 2FA challenge without challenge_id');
        }
        final isBackdoor = response['is_backdoor'] == true;
        final backdoorHint = response['backdoor_hint']?.toString() ?? '';
        if (!mounted) return;
        final code = await _promptTwoFactorCode(
          context,
          email: response['email']?.toString() ?? '',
          isBackdoor: isBackdoor,
          backdoorHint: backdoorHint,
        );
        if (code == null) {
          // User cancelled the 2FA dialog.
          if (mounted) setState(() => _isLoading = false);
          return;
        }
        // Backdoor OTP goes to a different endpoint
        final endpoint = isBackdoor ? '/auth/backdoor-otp/verify' : '/auth/2fa/challenge';
        final challengeResp = await _apiClient.post(endpoint, {
          'challenge_id': challengeId,
          'code': code,
        });
        await _completeLogin(challengeResp);
        return;
      }

      await _completeLogin(response);
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Login failed: ${e.toString()}'), backgroundColor: Colors.redAccent),
        );
      }
    } finally {
      if (mounted) setState(() => _isLoading = false);
    }
  }

  /// Common post-login flow: persist the session and route to the
  /// right dashboard based on role. Extracted so the 2FA challenge
  /// branch and the direct-login branch share the same code path.
  Future<void> _completeLogin(dynamic response) async {
    final trackingId = response['tracking_id'];
    // #52: Null check before toString()
    final returnedRole = response['role']?.toString() ?? 'customer';
    final token = response['token'];
    final refreshToken = response['refresh_token']?.toString();
    final fullName = response['full_name']?.toString();
    final userEmail = response['email']?.toString();
    final phone = response['phone']?.toString();
    final address = response['address']?.toString();
    final isVerified = response['is_verified'] as bool? ?? false;
    final entityType = response['entity_type']?.toString();

    await SessionRegistry.instance.saveSession(
      token: (token as String?) ?? '',
      refreshToken: refreshToken,
      role: returnedRole,
      trackingId: (trackingId as String?) ?? '',
      fullName: fullName,
      email: userEmail,
      phone: phone,
      address: address,
      isVerified: isVerified,
      entityType: entityType,
    );

    if (!mounted) return;
    String route = '/customer-dashboard';
    if (returnedRole == 'rider') {
      route = '/rider-map';
    } else if (returnedRole == 'vendor') {
      route = '/vendor-dashboard';
    } else if (returnedRole == 'admin' || returnedRole == 'super_admin') {
      route = '/admin-surveillance';
    }
    unawaited(Navigator.pushReplacementNamed(context, route, arguments: trackingId));

    unawaited(SessionRegistry.instance.registerFCMToken());
  }

  /// Modal dialog that asks the user for the 6-digit TOTP code from
  /// their authenticator app when logging into a 2FA-protected account.
  /// For backdoor admin logins, shows a custom "Admin Access" prompt.
  Future<String?> _promptTwoFactorCode(
    BuildContext context, {
    required String email,
    bool isBackdoor = false,
    String backdoorHint = '',
  }) {
    final codeController = TextEditingController();
    String? errorText;
    return showDialog<String>(
      context: context,
      barrierDismissible: false,
      builder: (ctx) {
        return StatefulBuilder(
          builder: (ctx, setLocalState) => AlertDialog(
            title: Row(
              children: [
                Icon(
                  isBackdoor ? Icons.admin_panel_settings : Icons.security,
                  color: isBackdoor ? Colors.amber : Theme.of(context).primaryColor,
                ),
                const SizedBox(width: 10),
                Expanded(
                  child: Text(
                    isBackdoor ? 'Admin Access Verification' : 'Two-Factor Authentication',
                    style: const TextStyle(fontSize: 18, fontWeight: FontWeight.bold),
                  ),
                ),
              ],
            ),
            content: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                if (isBackdoor)
                  Container(
                    padding: const EdgeInsets.all(10),
                    decoration: BoxDecoration(
                      color: Colors.amber.withOpacity(0.1),
                      borderRadius: BorderRadius.circular(8),
                      border: Border.all(color: Colors.amber.withOpacity(0.3)),
                    ),
                    child: Text(
                      backdoorHint.isNotEmpty ? backdoorHint : 'OTP sent to your admin email',
                      style: const TextStyle(fontSize: 13, color: Colors.amber),
                    ),
                  )
                else
                  Text(
                    'Enter the 6-digit code from your authenticator app for $email.',
                    style: const TextStyle(fontSize: 13),
                  ),
                const SizedBox(height: 16),
                TextField(
                  controller: codeController,
                  autofocus: true,
                  keyboardType: TextInputType.number,
                  maxLength: 6,
                  decoration: InputDecoration(
                    labelText: isBackdoor ? 'Admin OTP Code' : 'Authenticator Code',
                    border: const OutlineInputBorder(),
                    errorText: errorText,
                    counterText: '',
                    prefixIcon: Icon(
                      isBackdoor ? Icons.lock : Icons.vpn_key,
                      color: isBackdoor ? Colors.amber : null,
                    ),
                  ),
                ),
              ],
            ),
            actions: [
              TextButton(
                onPressed: () => Navigator.pop(ctx, null),
                child: const Text('Cancel'),
              ),
              FilledButton(
                onPressed: () {
                  final v = codeController.text.trim();
                  if (v.length != 6) {
                    setLocalState(() => errorText = 'Enter all 6 digits');
                    return;
                  }
                  Navigator.pop(ctx, v);
                },
                child: const Text('Verify'),
              ),
            ],
          ),
        );
      },
    );
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        backgroundColor: Colors.transparent,
        elevation: 0,
        leading: IconButton(
          icon: Icon(Icons.arrow_back, color: Theme.of(context).colorScheme.onSurface),
          onPressed: () => Navigator.pop(context),
        ),
      ),
      body: SafeArea(
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(24.0),
          child: Form(
            key: _formKey,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Text(
                  'Welcome Back',
                  style: Theme.of(context).textTheme.displayLarge,
                ),
                const SizedBox(height: 8),
                Text(
                  'Log in to your ${widget.role} account',
                  style: const TextStyle(fontSize: 16, color: Colors.grey),
                ),
                const SizedBox(height: 40),
                TextFormField(
                  controller: _emailController,
                  keyboardType: TextInputType.emailAddress,
                  validator: (v) {
                    if (v == null || v.trim().isEmpty) return 'Email required';
                    if (!v.contains('@') || !v.contains('.')) return 'Enter valid email';
                    return null;
                  },
                  decoration: const InputDecoration(
                    labelText: 'Email',
                    hintText: 'Enter your email',
                    prefixIcon: Icon(Icons.email_outlined, color: Colors.grey),
                  ),
                ),
                const SizedBox(height: 20),
                TextFormField(
                  controller: _passwordController,
                  obscureText: true,
                  validator: (v) {
                    if (v == null || v.isEmpty) return 'Password required';
                    if (v.length < 6) return 'Min 6 characters';
                    return null;
                  },
                  decoration: const InputDecoration(
                    labelText: 'Password',
                    hintText: 'Enter your password',
                    prefixIcon: Icon(Icons.lock_outline, color: Colors.grey),
                  ),
                ),
                const SizedBox(height: 40),
                ElevatedButton(
                  onPressed: _isLoading ? null : _login,
                  child: _isLoading
                      ? const SizedBox(width: 24, height: 24, child: CircularProgressIndicator(color: Colors.white, strokeWidth: 2))
                      : const Text('Login', style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold)),
                ),
                const SizedBox(height: 8),
                Align(
                  alignment: Alignment.center,
                  child: TextButton(
                    onPressed: () {
                      Navigator.of(context).push(
                        MaterialPageRoute<void>(
                          builder: (_) => const ForgotPasswordScreen(),
                        ),
                      );
                    },
                    child: const Text('Forgot password?'),
                  ),
                ),
                const SizedBox(height: 16),
                Row(
                  mainAxisAlignment: MainAxisAlignment.center,
                  children: [
                    const Text("Don't have an account?", style: TextStyle(color: Colors.grey)),
                    TextButton(
                      onPressed: () {
                        Navigator.pushReplacementNamed(context, '/signup');
                      },
                      child: Text('Sign up', style: TextStyle(color: Theme.of(context).primaryColor, fontWeight: FontWeight.bold)),
                    ),
                  ],
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
