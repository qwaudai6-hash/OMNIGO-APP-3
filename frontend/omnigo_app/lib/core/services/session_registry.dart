import 'dart:io' show Platform;
import 'package:flutter/foundation.dart' show kIsWeb;
import 'package:shared_preferences/shared_preferences.dart';
import 'package:firebase_messaging/firebase_messaging.dart';
import 'package:flutter_background_service/flutter_background_service.dart';
import '../network/api_client.dart';

class SessionRegistry {
  SessionRegistry._privateConstructor();
  static final SessionRegistry instance = SessionRegistry._privateConstructor();

  String? _token;
  String? _refreshToken;
  String? _role;
  String? _trackingId;
  String? _fullName;
  String? _email;
  String? _phone;
  String? _address;
  bool _isVerified = false;
  String? _entityType;
  bool _isHydrated = false;

  String? get token => _token;
  String? get refreshToken => _refreshToken;
  String? get role => _role;
  String? get trackingId => _trackingId;
  String? get fullName => _fullName;
  String? get email => _email;
  String? get phone => _phone;
  String? get address => _address;
  bool get isVerified => _isVerified;
  String? get entityType => _entityType;
  bool get isHydrated => _isHydrated;

  bool get isVendorLoggedIn => _token != null && _role == 'vendor' && _trackingId != null;
  bool get isLoggedIn => _token != null && _trackingId != null;

  // Hydrate from SharedPreferences on app startup with a strict try-catch block
  Future<void> hydrate() async {
    try {
      final prefs = await SharedPreferences.getInstance();
      _token = prefs.getString('jwt_token');
      _refreshToken = prefs.getString('refresh_token');
      _role = prefs.getString('role');
      _trackingId = prefs.getString('tracking_id');
      _fullName = prefs.getString('full_name');
      _email = prefs.getString('email');
      _phone = prefs.getString('phone');
      _address = prefs.getString('address');
      _isVerified = prefs.getBool('is_verified') ?? false;
      _entityType = prefs.getString('entity_type');
      _isHydrated = true;
    } catch (e) {
      // Self-healing recovery mechanism in case of disk corruption
      await clear();
    }
  }

  // Atomically save session properties
  Future<void> saveSession({
    required String token,
    String? refreshToken,
    required String role,
    required String trackingId,
    String? fullName,
    String? email,
    String? phone,
    String? address,
    bool? isVerified,
    String? entityType,
  }) async {
    _token = token;
    if (refreshToken != null) _refreshToken = refreshToken;
    _role = role;
    _trackingId = trackingId;
    if (fullName != null) _fullName = fullName;
    if (email != null) _email = email;
    if (phone != null) _phone = phone;
    if (address != null) _address = address;
    if (isVerified != null) _isVerified = isVerified;
    if (entityType != null) _entityType = entityType;
    _isHydrated = true;

    try {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('jwt_token', token);
      if (refreshToken != null) await prefs.setString('refresh_token', refreshToken);
      await prefs.setString('role', role);
      await prefs.setString('tracking_id', trackingId);
      if (fullName != null) await prefs.setString('full_name', fullName);
      if (email != null) await prefs.setString('email', email);
      if (phone != null) await prefs.setString('phone', phone);
      if (address != null) await prefs.setString('address', address);
      if (isVerified != null) await prefs.setBool('is_verified', isVerified);
      if (entityType != null) await prefs.setString('entity_type', entityType);
    } catch (e) {
      // Non-fatal warning if save fails, keep in-memory cache active
    }
  }

  /// Updates the in-memory + persistent profile fields without touching
  /// the JWT token or role. Called after a successful PATCH /auth/profile.
  Future<void> updateProfile({
    String? fullName,
    String? phone,
    String? address,
  }) async {
    if (fullName != null) _fullName = fullName;
    if (phone != null) _phone = phone;
    if (address != null) _address = address;

    try {
      final prefs = await SharedPreferences.getInstance();
      if (fullName != null) await prefs.setString('full_name', fullName);
      if (phone != null) await prefs.setString('phone', phone);
      if (address != null) await prefs.setString('address', address);
    } catch (e) {
      // Non-fatal
    }
  }

  // Clear in-memory variables and storage records
  Future<void> clear() async {
    _token = null;
    _refreshToken = null;
    _role = null;
    _trackingId = null;
    _fullName = null;
    _email = null;
    _phone = null;
    _address = null;
    _isVerified = false;
    _entityType = null;
    _isHydrated = false;

    try {
      final prefs = await SharedPreferences.getInstance();
      await prefs.remove('jwt_token');
      await prefs.remove('refresh_token');
      await prefs.remove('role');
      await prefs.remove('tracking_id');
      await prefs.remove('full_name');
      await prefs.remove('email');
      await prefs.remove('phone');
      await prefs.remove('address');
      await prefs.remove('is_verified');
      await prefs.remove('entity_type');
    } catch (e) {
      // Fail-silent
    }
  }

  /// Convenience logout that also stops rider telemetry service if it is running
  /// and revokes the refresh token on the backend.
  Future<void> logout() async {
    try {
      final service = FlutterBackgroundService();
      if (await service.isRunning()) {
        service.invoke('stopService');
      }
    } catch (_) {
      // Service may not be initialized
    }
    try {
      if (_token != null && _refreshToken != null) {
        await ApiClient().post('/auth/logout', {'refresh_token': _refreshToken});
      }
    } catch (_) {
      // Non-fatal — local session must still be cleared even if backend call fails
    }
    await clear();
  }

  /// Registers the FCM device token with the backend so the Node.js
  /// notification worker can push notifications to this device.
  /// Called after successful login or on app launch if already logged in.
  Future<void> registerFCMToken() async {
    if (_token == null || _trackingId == null) return;

    try {
      final fcmToken = await FirebaseMessaging.instance.getToken();
      if (fcmToken == null) return;

      String platform = 'web';
      if (!kIsWeb) {
        if (Platform.isAndroid) {
          platform = 'android';
        } else if (Platform.isIOS) {
          platform = 'ios';
        }
      }

      await ApiClient().post('/api/v1/auth/device-token', {
        'fcm_token': fcmToken,
        'platform': platform,
      });
    } catch (e) {
      // Non-fatal — push notifications are best-effort
    }
  }
}
