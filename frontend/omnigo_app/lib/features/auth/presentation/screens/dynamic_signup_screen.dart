import 'dart:async';
import 'dart:io';
import 'dart:convert';
import 'package:flutter/foundation.dart' show kIsWeb;
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:geolocator/geolocator.dart';
import 'package:http/http.dart' as http;
import '../../../../core/theme/app_theme.dart';
import '../../../../core/network/api_client.dart';
import 'forgot_password_screen.dart';
import '../../../../core/network/api_endpoints.dart';
import '../../../../core/services/connectivity_service.dart';
import '../../../../core/utils/error_formatter.dart';

import '../../../../core/services/session_registry.dart';

class DynamicSignupScreen extends StatefulWidget {
  const DynamicSignupScreen({super.key, this.startInLoginMode});
  final bool? startInLoginMode;

  @override
  DynamicSignupScreenState createState() => DynamicSignupScreenState();
}

class DynamicSignupScreenState extends State<DynamicSignupScreen> {
  String _selectedRole = 'Customer';
  String _selectedEntityType = 'company'; // 'company' or 'individual' for vendor
  bool _isLogin = false; // Toggle between Login and Signup modes
  bool _isLoading = false;
  
  // Form Controllers
  final _nameController = TextEditingController();
  final _phoneController = TextEditingController();
  final _emailController = TextEditingController();
  final _passwordController = TextEditingController();
  final _addressController = TextEditingController();
  
  // Smart Address Variables
  double? _vendorLat;
  double? _vendorLng;
  bool _isFetchingLocation = false;

  // Rider specific
  String? _selectedVehicleType;
  final _vehiclePlateController = TextEditingController();
  final List<String> _vehicleTypes = ['Bike / Motorcycle', 'Car', 'Auto Rickshaw', 'Loading Vehicle'];

  // Vendor specific
  final _businessNameController = TextEditingController();

  bool _isUploadingDocs = false;

  final _formKey = GlobalKey<FormState>();
  final ApiClient _apiClient = ApiClient();
  final ConnectivityService _connectivityService = ConnectivityService();

  @override
  void initState() {
    super.initState();
    if (widget.startInLoginMode != null) {
      _isLogin = widget.startInLoginMode!;
    }
    // Auto-login checking: If session is already active, redirect instantly to dashboard
    if (SessionRegistry.instance.isLoggedIn) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        String route = '/customer-dashboard';
        if (SessionRegistry.instance.role == 'rider') {
          route = '/rider-map';
        } else if (SessionRegistry.instance.role == 'vendor') {
          route = '/vendor-dashboard';
        }
        Navigator.pushReplacementNamed(
          context,
          route,
          arguments: SessionRegistry.instance.trackingId,
        );
      });
    }
  }

  /// Reverse-geocodes coordinates to a human-readable address via the
  /// backend Nominatim proxy (keeps User-Agent / rate limits server-side).
  Future<String> _reverseGeocode(double lat, double lng) async {
    final url = Uri.parse(ApiEndpoints.geocodingReverse(lat, lng));
    // SP-FL-13: bound the call so a slow geo proxy cannot freeze signup.
    final response = await http.get(url).timeout(const Duration(seconds: 8));
    if (response.statusCode == 200) {
      final data = json.decode(response.body) as Map<String, dynamic>;
      final addr = data['address'] as Map<String, dynamic>? ?? {};
      final parts = <String>[
        if (addr['road'] != null) addr['road'] as String,
        if (addr['suburb'] != null) addr['suburb'] as String,
        if (addr['city'] != null) addr['city'] as String else if (addr['town'] != null) addr['town'] as String,
        if (addr['country'] != null) addr['country'] as String,
      ];
      return parts.join(', ');
    }
    // Fallback: return the raw display_name if structured parsing fails
    final fallback = json.decode(response.body) as Map<String, dynamic>;
    return (fallback['display_name'] as String?) ?? '$lat, $lng';
  }

  /// Fetches real coordinates using IP-based location as a fallback for 
  /// desktop platforms (Linux) where GPS hardware/plugins are unavailable.
  Future<Map<String, double>> _getIpBasedLocation() async {
    // SP-FL-13: timeout here too (third-party API — never trust its latency).
    final response = await http.get(Uri.parse('https://ipapi.co/json/')).timeout(const Duration(seconds: 6));
    if (response.statusCode == 200) {
      final data = json.decode(response.body);
      if (data['status'] == 'success') {
        return {
          'lat': (data['lat'] as num).toDouble(),
          'lng': (data['lon'] as num).toDouble(),
        };
      }
    }
    throw Exception('IP geolocation failed.');
  }

  Future<void> _fetchCurrentLocation() async {
    setState(() => _isFetchingLocation = true);
    try {
      double? lat;
      double? lng;

      try {
        if (!kIsWeb && (Platform.isLinux || Platform.isWindows)) {
          throw MissingPluginException('Geolocator not supported on this platform');
        }
        
        final bool serviceEnabled = await Geolocator.isLocationServiceEnabled();
        if (!serviceEnabled) {
          throw Exception('Location services are disabled. Please enable GPS.');
        }

        LocationPermission permission = await Geolocator.checkPermission();
        if (permission == LocationPermission.denied) {
          permission = await Geolocator.requestPermission();
          if (permission == LocationPermission.denied) {
            throw Exception('Location permissions are denied.');
          }
        }

        if (permission == LocationPermission.deniedForever) {
          throw Exception('Location permissions are permanently denied. Please enable them in Settings.');
        }

        final Position position = await Geolocator.getCurrentPosition(
          locationSettings: const LocationSettings(accuracy: LocationAccuracy.high),
        );
        lat = position.latitude;
        lng = position.longitude;
      } on MissingPluginException catch (_) {
        // Fallback to real IP-based geolocation for unsupported desktop platforms
        final ipLoc = await _getIpBasedLocation();
        lat = ipLoc['lat'];
        lng = ipLoc['lng'];
      }

      if (lat == null || lng == null) {
        throw Exception('Could not determine location.');
      }

      _vendorLat = lat;
      _vendorLng = lng;

      // Use HTTP-based reverse geocoding (works on Web, Desktop, and Mobile)
      final address = await _reverseGeocode(lat, lng);
      _addressController.text = address;

      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(const SnackBar(
          content: Text('Address fetched successfully!', style: TextStyle(color: Colors.white)),
          backgroundColor: AppTheme.softGreen,
        ),);
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(
          content: Text('Failed to fetch location: $e'),
          backgroundColor: Colors.redAccent,
        ),);
      }
    } finally {
      if (mounted) setState(() => _isFetchingLocation = false);
    }
  }

  void _selectRole(String role) {
    HapticFeedback.lightImpact();
    setState(() => _selectedRole = role);
  }

  Future<void> _submit() async {
    if (!_formKey.currentState!.validate()) return;
    
    setState(() => _isLoading = true);
    unawaited(HapticFeedback.mediumImpact());

    // Check hardware internet connection before making API calls
    final bool online = await _connectivityService.isOnline();
    if (!online) {
      setState(() => _isLoading = false);
      _showStatusMessage(
        title: 'Connection Offline',
        message: 'No internet connection detected on your device. Please verify your hardware settings and try again.',
        isSuccess: false,
      );
      return;
    }

    final email = _emailController.text.trim();
    final password = _passwordController.text.trim();
    final name = _nameController.text.trim();
    final role = _selectedRole.toLowerCase();

    try {
      if (_isLogin) {
        // --- API LOGIN ---
        final response = await _apiClient.post('/auth/login', {
          'email': email,
          'password': password,
          'role': role,
        });

        final trackingId = response['tracking_id'];
        final returnedRole = response['role'].toString();
        final token = response['token'];
        final refreshToken = response['refresh_token']?.toString();
        
        final fullName = response['full_name']?.toString();
        final userEmail = response['email']?.toString();
        final phone = response['phone']?.toString();
        final isVerified = response['is_verified'] as bool? ?? false;
        final entityType = response['entity_type']?.toString();
        final address = response['address']?.toString();

        // Save session properties in the memory cache + persistent store
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
        
        String route = '/customer-dashboard';
        if (returnedRole == 'rider') {
          route = '/rider-map';
        } else if (returnedRole == 'vendor') {
          route = '/vendor-dashboard';
        } else if (returnedRole == 'admin' || returnedRole == 'super_admin') {
          route = '/admin-surveillance';
        }

        setState(() => _isLoading = false);
        if (!mounted) return;
        unawaited(Navigator.pushReplacementNamed(context, route, arguments: trackingId));

        // Register FCM device token for push notifications
        unawaited(SessionRegistry.instance.registerFCMToken());
      } else {
        // --- API REGISTER ---
        final phone = _phoneController.text.trim();
        final body = {
          'name': name,
          'email': email,
          'password': password,
          'role': role,
          'region': 'PK',
          'phone': phone,
          'cnic_url': '',
          'license_url': '',
          'business_name': _selectedRole.toLowerCase() == 'vendor' ? _businessNameController.text.trim() : '',
          'store_name': _selectedRole.toLowerCase() == 'vendor' ? _businessNameController.text.trim() : '',
          'address': _addressController.text.trim(),
          'latitude': _selectedRole.toLowerCase() == 'vendor' ? _vendorLat : null,
          'longitude': _selectedRole.toLowerCase() == 'vendor' ? _vendorLng : null,
          'entity_type': _selectedRole.toLowerCase() == 'vendor' ? _selectedEntityType : '',
          'vehicle_type': _selectedRole.toLowerCase() == 'rider' ? (_selectedVehicleType ?? '') : '',
          'vehicle_plate_number': _selectedRole.toLowerCase() == 'rider' ? _vehiclePlateController.text.trim() : '',
        };

        await _apiClient.post('/auth/register', body);

        if (role == 'customer') {
          // Customers are verified instantly — auto-login and enter dashboard directly
          final loginResponse = await _apiClient.post('/auth/login', {
            'email': email,
            'password': password,
            'role': role,
          });

          final trackingId = loginResponse['tracking_id'];
          final returnedRole = loginResponse['role'].toString();
          final token = loginResponse['token'];
          final refreshToken = loginResponse['refresh_token']?.toString();
          final fullName = loginResponse['full_name']?.toString();
          final userEmail = loginResponse['email']?.toString();
        final userPhone = loginResponse['phone']?.toString();
        final userAddress = loginResponse['address']?.toString();
        final isVerified = loginResponse['is_verified'] as bool? ?? true;
        final entityType = loginResponse['entity_type']?.toString();

        await SessionRegistry.instance.saveSession(
          token: (token as String?) ?? '',
          refreshToken: refreshToken,
          role: returnedRole,
          trackingId: (trackingId as String?) ?? '',
          fullName: fullName,
          email: userEmail,
          phone: userPhone,
          address: userAddress,
          isVerified: isVerified,
          entityType: entityType,
        );

          setState(() => _isLoading = false);
          if (!mounted) return;
          unawaited(Navigator.pushReplacementNamed(context, '/customer-dashboard', arguments: trackingId));
          unawaited(SessionRegistry.instance.registerFCMToken());
        } else {
          // Riders / Vendors require verification submission
          setState(() => _isLoading = false);
          _showStatusMessage(
            title: 'Registration Successful',
            message: role == 'rider'
                ? 'Rider registration complete! Please submit your vehicle & license documents for verification.'
                : 'Vendor registration complete! Please submit your store verification details in your dashboard.',
            isSuccess: true,
          );
          setState(() {
            _isLogin = true;
          });
        }
      }
    } catch (e) {
      setState(() => _isLoading = false);
      debugPrint('Auth Error: $e');
      final formattedError = UserFriendlyError.fromException(e);
      _showStatusMessage(
        title: formattedError.title,
        message: formattedError.message,
        isSuccess: false,
        actionLabel: formattedError.isNetworkIssue ? 'Retry' : 'Got it',
        onAction: formattedError.isNetworkIssue ? _submit : null,
      );
    }
  }

  void _showStatusMessage({
    required String title,
    required String message,
    required bool isSuccess,
    String? actionLabel,
    VoidCallback? onAction,
  }) {
    showDialog<void>(
      context: context,
      barrierDismissible: true,
      builder: (context) => Dialog(
        backgroundColor: Colors.transparent,
        insetPadding: const EdgeInsets.symmetric(horizontal: 24, vertical: 24),
        child: Container(
          padding: const EdgeInsets.all(24),
          decoration: BoxDecoration(
            color: Colors.white,
            borderRadius: BorderRadius.circular(28),
            boxShadow: const [
              BoxShadow(
                color: Colors.black26,
                blurRadius: 30,
                offset: Offset(0, 12),
              ),
            ],
          ),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Row(
                children: [
                  Container(
                    padding: const EdgeInsets.all(10),
                    decoration: BoxDecoration(
                      color: isSuccess ? AppTheme.softGreen.withValues(alpha: 0.2) : Colors.red.shade50,
                      shape: BoxShape.circle,
                    ),
                    child: Icon(
                      isSuccess ? Icons.check_circle_rounded : Icons.wifi_off_rounded,
                      color: isSuccess ? Colors.teal : Colors.redAccent,
                      size: 24,
                    ),
                  ),
                  const SizedBox(width: 14),
                  Expanded(
                    child: Text(
                      title,
                      style: const TextStyle(
                        fontSize: 18,
                        fontWeight: FontWeight.w800,
                        color: AppTheme.blackAccent,
                        letterSpacing: -0.3,
                      ),
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 16),
              Text(
                message,
                style: const TextStyle(
                  fontSize: 14,
                  height: 1.5,
                  color: Colors.black87,
                  fontWeight: FontWeight.w400,
                ),
              ),
              const SizedBox(height: 24),
              Row(
                mainAxisAlignment: MainAxisAlignment.end,
                children: [
                  ElevatedButton(
                    style: ElevatedButton.styleFrom(
                      backgroundColor: isSuccess ? AppTheme.limeAccent : AppTheme.blackAccent,
                      foregroundColor: isSuccess ? AppTheme.blackAccent : Colors.white,
                      elevation: 0,
                      padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 12),
                      shape: RoundedRectangleBorder(
                        borderRadius: BorderRadius.circular(20),
                      ),
                    ),
                    onPressed: () {
                      Navigator.pop(context);
                      if (onAction != null) onAction();
                    },
                    child: Text(
                      actionLabel ?? 'OK',
                      style: TextStyle(
                        fontWeight: FontWeight.bold,
                        color: isSuccess ? AppTheme.blackAccent : Colors.white,
                      ),
                    ),
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }

  Color _getRoleColor() {
    if (_selectedRole == 'Customer') return AppTheme.softPink;
    if (_selectedRole == 'Rider') return AppTheme.softBlue;
    return AppTheme.softGreen;
  }

  LinearGradient _getRoleGradient() {
    if (_selectedRole == 'Customer') {
      return LinearGradient(
        colors: [AppTheme.softPink, Colors.pink.shade50],
        begin: Alignment.topLeft,
        end: Alignment.bottomRight,
      );
    }
    if (_selectedRole == 'Rider') {
      return LinearGradient(
        colors: [AppTheme.softBlue, Colors.blue.shade50],
        begin: Alignment.topLeft,
        end: Alignment.bottomRight,
      );
    }
    return LinearGradient(
      colors: [AppTheme.softGreen, Colors.teal.shade50],
      begin: Alignment.topLeft,
      end: Alignment.bottomRight,
    );
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: AppTheme.bgColor,
      body: SafeArea(
        child: SingleChildScrollView(
          physics: const BouncingScrollPhysics(),
          padding: const EdgeInsets.symmetric(horizontal: 24.0),
          child: Form(
            key: _formKey,
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                const SizedBox(height: 40),
                // Premium Dynamic Branding Logo
                Center(
                  child: Column(
                    children: [
                      Container(
                        padding: const EdgeInsets.all(16),
                        decoration: const BoxDecoration(
                          color: AppTheme.blackAccent,
                          shape: BoxShape.circle,
                        ),
                        child: const Icon(Icons.flash_on, color: AppTheme.limeAccent, size: 36),
                      ),
                      const SizedBox(height: 12),
                      const Text(
                        'OMNIGO',
                        style: TextStyle(
                          fontSize: 28,
                          fontWeight: FontWeight.w900,
                          letterSpacing: 6.0,
                          color: AppTheme.blackAccent,
                        ),
                      ),
                      const SizedBox(height: 4),
                      Text(
                        _isLogin ? 'LOG IN TO SUPERPORTAL' : 'CREATE UNIFIED ID ACCOUNT',
                        style: TextStyle(
                          color: AppTheme.blackAccent.withValues(alpha: 0.5),
                          fontSize: 11,
                          fontWeight: FontWeight.bold,
                          letterSpacing: 2.0,
                        ),
                      ),
                    ],
                  ),
                ),
                const SizedBox(height: 36),

                // Role selector pill
                _buildRolePillSelector(),
                const SizedBox(height: 28),

                // Dynamic Premium Container Card
                _buildDynamicCardContent(),
                const SizedBox(height: 28),

                // Toggle Login/Signup with animated scale transitions
                Wrap(
                  alignment: WrapAlignment.center,
                  crossAxisAlignment: WrapCrossAlignment.center,
                  children: [
                    Text(
                      _isLogin ? "Don't have an account? " : "Already have an account? ",
                      style: const TextStyle(color: Colors.grey, fontSize: 14),
                    ),
                    GestureDetector(
                      onTap: () {
                        HapticFeedback.lightImpact();
                        setState(() => _isLogin = !_isLogin);
                      },
                      child: Text(
                        _isLogin ? 'Sign Up' : 'Login',
                        style: const TextStyle(
                          color: AppTheme.blackAccent,
                          fontWeight: FontWeight.bold,
                          decoration: TextDecoration.underline,
                          fontSize: 14,
                        ),
                      ),
                    ),
                  ],
                ),
                // FEATURE FIX: the dedicated LoginScreen (and its
                // ForgotPasswordScreen flow) was unreachable dead code — nothing
                // ever navigated to /login. Users in login mode now get direct
                // access to password reset without depending on that screen.
                if (_isLogin)
                  Padding(
                    padding: const EdgeInsets.only(top: 8.0),
                    child: GestureDetector(
                      onTap: () {
                        HapticFeedback.lightImpact();
                        Navigator.of(context).push(
                          MaterialPageRoute<void>(
                            builder: (_) => const ForgotPasswordScreen(),
                          ),
                        );
                      },
                      child: const Text(
                        'Forgot password?',
                        style: TextStyle(
                          color: AppTheme.blackAccent,
                          fontWeight: FontWeight.w600,
                          fontSize: 13,
                        ),
                      ),
                    ),
                  ),
                const SizedBox(height: 40),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Widget _buildRolePillSelector() {
    return Container(
      padding: const EdgeInsets.all(6),
      decoration: BoxDecoration(
        color: AppTheme.blackAccent,
        borderRadius: BorderRadius.circular(30),
        boxShadow: const [
          BoxShadow(color: Colors.black12, blurRadius: 15, offset: Offset(0, 8)),
        ],
      ),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceBetween,
        children: [
          _buildPillItem('Customer'),
          _buildPillItem('Rider'),
          _buildPillItem('Vendor'),
        ],
      ),
    );
  }

  Widget _buildPillItem(String title) {
    final isSelected = _selectedRole == title;
    return Expanded(
      child: GestureDetector(
        onTap: () => _selectRole(title),
        child: AnimatedContainer(
          duration: const Duration(milliseconds: 250),
          curve: Curves.fastOutSlowIn,
          padding: const EdgeInsets.symmetric(vertical: 12),
          decoration: BoxDecoration(
            color: isSelected ? AppTheme.limeAccent : Colors.transparent,
            borderRadius: BorderRadius.circular(24),
          ),
          child: Text(
            title,
            textAlign: TextAlign.center,
            style: TextStyle(
              fontWeight: FontWeight.bold,
              color: isSelected ? AppTheme.blackAccent : Colors.white,
              fontSize: 13,
              letterSpacing: 1.0,
            ),
          ),
        ),
      ),
    );
  }

  Widget _buildDynamicCardContent() {
    return AnimatedContainer(
      duration: const Duration(milliseconds: 300),
      curve: Curves.easeInOut,
      padding: const EdgeInsets.all(28),
      decoration: BoxDecoration(
        gradient: _getRoleGradient(),
        borderRadius: BorderRadius.circular(38),
        boxShadow: [
          BoxShadow(
            color: _getRoleColor().withValues(alpha: 0.3),
            blurRadius: 25,
            offset: const Offset(0, 10),
          ),
        ],
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              Container(
                padding: const EdgeInsets.all(10),
                decoration: const BoxDecoration(color: Colors.white, shape: BoxShape.circle),
                child: Icon(
                  _isLogin ? Icons.vpn_key_outlined : Icons.person_add_alt_1_outlined,
                  color: AppTheme.blackAccent,
                  size: 20,
                ),
              ),
              const SizedBox(width: 12),
              Expanded(
                child: Text(
                  '$_selectedRole ${_isLogin ? "Login" : "Registration"}',
                  style: const TextStyle(
                    fontSize: 20, 
                    fontWeight: FontWeight.w900, 
                    color: AppTheme.blackAccent,
                    letterSpacing: 0.5,
                  ),
                ),
              ),
            ],
          ),
          const SizedBox(height: 24),

          // Standard credentials
          _buildCleanTextField(
            controller: _emailController,
            hint: 'Email Address',
            icon: Icons.alternate_email_outlined,
            keyboardType: TextInputType.emailAddress,
            validator: (value) {
              if (value == null || value.trim().isEmpty) return 'Email is required';
              if (!value.contains('@') || !value.contains('.')) return 'Enter a valid email';
              return null;
            },
          ),
          const SizedBox(height: 16),
          _buildCleanTextField(
            controller: _passwordController,
            hint: 'Password',
            icon: Icons.lock_outline_rounded,
            obscureText: true,
            validator: (value) {
              if (value == null || value.isEmpty) return 'Password is required';
              if (value.length < 6) return 'Password must be at least 6 characters';
              return null;
            },
          ),
          const SizedBox(height: 16),

          // Dynamic registration requirements
          if (!_isLogin) ...[
            _buildCleanTextField(
              controller: _nameController,
              hint: 'Full Name',
              icon: Icons.face_retouching_natural_outlined,
              validator: (value) => value == null || value.trim().isEmpty ? 'Full Name is required' : null,
            ),
            const SizedBox(height: 16),
            _buildCleanTextField(
              controller: _phoneController,
              hint: 'Phone Number',
              icon: Icons.phone_android_rounded,
              keyboardType: TextInputType.phone,
              validator: (value) => value == null || value.trim().isEmpty ? 'Phone is required' : null,
            ),
            const SizedBox(height: 16),
            
            if (_selectedRole == 'Customer') ...[
              _buildCleanTextField(
                controller: _addressController,
                hint: 'Home Address',
                icon: Icons.location_city_outlined,
                validator: (value) => value == null || value.trim().isEmpty ? 'Address is required' : null,
              ),
            ],
            
            if (_selectedRole == 'Rider') ...[
              _buildCleanTextField(
                controller: _addressController,
                hint: 'Home Address',
                icon: Icons.location_city_outlined,
                validator: (value) => value == null || value.trim().isEmpty ? 'Address is required' : null,
              ),
              const SizedBox(height: 16),
              DropdownButtonFormField<String>(
                key: ValueKey(_selectedVehicleType),
                value: _selectedVehicleType,
                hint: const Text('Select Vehicle Type', style: TextStyle(fontSize: 14)),
                decoration: InputDecoration(
                  prefixIcon: const Icon(Icons.two_wheeler_rounded, color: AppTheme.limeAccent),
                  filled: true,
                  fillColor: Colors.white,
                  border: OutlineInputBorder(borderRadius: BorderRadius.circular(16), borderSide: BorderSide.none),
                ),
                items: _vehicleTypes.map((type) => DropdownMenuItem(value: type, child: Text(type))).toList(),
                onChanged: (val) => setState(() => _selectedVehicleType = val),
                validator: (val) => val == null ? 'Vehicle type is required' : null,
              ),
              if (_selectedVehicleType != null) ...[
                const SizedBox(height: 16),
                _buildCleanTextField(
                  controller: _vehiclePlateController,
                  hint: 'Vehicle Plate Number (e.g. ABC-1234)',
                  icon: Icons.pin_outlined,
                  validator: (value) => value == null || value.trim().isEmpty ? 'Plate number is required' : null,
                ),
              ],
            ],
            
            if (_selectedRole == 'Vendor') ...[
              const SizedBox(height: 16),
              const Text("Entity Type", style: TextStyle(fontWeight: FontWeight.bold, fontSize: 13, color: Colors.grey)),
              const SizedBox(height: 8),
              Row(
                children: [
                  Expanded(
                    // ignore: deprecated_member_use
                    child: RadioListTile<String>(
                      title: const Text('Company', style: TextStyle(fontSize: 13)),
                      value: 'company',
                      // ignore: deprecated_member_use
                      groupValue: _selectedEntityType,
                      // ignore: deprecated_member_use
                      onChanged: (value) => setState(() => _selectedEntityType = value!),
                      fillColor: WidgetStateProperty.all(AppTheme.limeAccent),
                      contentPadding: EdgeInsets.zero,
                    ),
                  ),
                  Expanded(
                    // ignore: deprecated_member_use
                    child: RadioListTile<String>(
                      title: const Text('Individual', style: TextStyle(fontSize: 13)),
                      value: 'individual',
                      // ignore: deprecated_member_use
                      groupValue: _selectedEntityType,
                      // ignore: deprecated_member_use
                      onChanged: (value) => setState(() => _selectedEntityType = value!),
                      fillColor: WidgetStateProperty.all(AppTheme.limeAccent),
                      contentPadding: EdgeInsets.zero,
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 16),
              _buildCleanTextField(
                controller: _businessNameController,
                hint: _selectedEntityType == 'company' ? 'Business/Trade Name' : 'Shop/Store Name',
                icon: Icons.storefront_outlined,
                validator: (value) => value == null || value.trim().isEmpty ? 'Store Name is required' : null,
              ),
              const SizedBox(height: 16),
              Stack(
                alignment: Alignment.centerRight,
                children: [
                  _buildCleanTextField(
                    controller: _addressController,
                    hint: 'Store Address',
                    icon: Icons.location_on_outlined,
                    validator: (value) => value == null || value.trim().isEmpty ? 'Store Address is required' : null,
                  ),
                  Positioned(
                    right: 8,
                    child: IconButton(
                      icon: _isFetchingLocation 
                          ? const SizedBox(width: 20, height: 20, child: CircularProgressIndicator(strokeWidth: 2, color: AppTheme.limeAccent))
                          : const Icon(Icons.my_location, color: AppTheme.limeAccent),
                      onPressed: _isFetchingLocation ? null : _fetchCurrentLocation,
                      tooltip: 'Auto-fetch location',
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 8),
              const Text("You can setup your shop details and upload verification documents later in your Dashboard.", style: TextStyle(color: Colors.grey, fontSize: 12)),
            ],
            const SizedBox(height: 24),
          ],

          // Action Ingress Ingestion Button
          Row(
            mainAxisAlignment: MainAxisAlignment.spaceBetween,
            children: [
              Expanded(
                child: Text(
                  _isLogin ? 'Verify & Ingest' : 'Create Unified Account',
                  style: const TextStyle(
                    fontSize: 14,
                    fontWeight: FontWeight.bold,
                    color: AppTheme.blackAccent,
                  ),
                ),
              ),
              const SizedBox(width: 8),
              GestureDetector(
                onTap: (_isLoading || _isUploadingDocs) ? null : _submit,
                child: AnimatedContainer(
                  duration: const Duration(milliseconds: 200),
                  width: 58,
                  height: 58,
                  decoration: const BoxDecoration(
                    color: AppTheme.blackAccent,
                    shape: BoxShape.circle,
                    boxShadow: [
                      BoxShadow(color: Colors.black12, blurRadius: 10, offset: Offset(0, 4)),
                    ],
                  ),
                  child: (_isLoading || _isUploadingDocs)
                      ? const Center(
                          child: SizedBox(
                            width: 20,
                            height: 20,
                            child: CircularProgressIndicator(color: AppTheme.limeAccent, strokeWidth: 2),
                          ),
                        )
                      : const Icon(Icons.arrow_forward_rounded, color: AppTheme.limeAccent, size: 28),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }

  Widget _buildCleanTextField({
    required TextEditingController controller,
    required String hint,
    required IconData icon,
    bool obscureText = false,
    TextInputType keyboardType = TextInputType.text,
    String? Function(String?)? validator,
  }) {
    return Container(
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(18),
        boxShadow: [
          BoxShadow(
            color: Colors.black.withValues(alpha: 0.015),
            blurRadius: 8,
            offset: const Offset(0, 4),
          ),
        ],
      ),
      child: TextFormField(
        controller: controller,
        obscureText: obscureText,
        validator: validator,
        keyboardType: keyboardType,
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


}
