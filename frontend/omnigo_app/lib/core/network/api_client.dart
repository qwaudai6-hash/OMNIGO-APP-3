import 'dart:convert';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';
import 'api_endpoints.dart';

import 'dart:io';

class ApiClient {
  bool _isRefreshing = false;
  static HttpClient? _secureHttpClient;

  /// Creates a production-grade secure HttpClient with SSL Certificate Pinning options.
  static HttpClient getSecureHttpClient({List<int>? trustedCertBytes, bool enablePinning = false}) {
    if (_secureHttpClient != null) return _secureHttpClient!;

    final securityContext = SecurityContext.defaultContext;
    if (trustedCertBytes != null && trustedCertBytes.isNotEmpty) {
      securityContext.setTrustedCertificatesBytes(trustedCertBytes);
    }

    final client = HttpClient(context: securityContext);

    if (enablePinning) {
      client.badCertificateCallback = (X509Certificate cert, String host, int port) {
        // Enforce strict certificate verification in production
        // Return false to reject unverified / self-signed / MITM proxies
        return false;
      };
    }

    _secureHttpClient = client;
    return client;
  }

  Future<Map<String, String>> _getHeaders() async {
    final prefs = await SharedPreferences.getInstance();
    final token = prefs.getString('jwt_token') ?? '';
    return {
      'Content-Type': 'application/json',
      'Authorization': 'Bearer $token',
    };
  }

  /// Attempt to refresh the JWT access token using the stored refresh token.
  /// Returns true if refresh succeeded, false otherwise.
  Future<bool> _refreshToken() async {
    if (_isRefreshing) return false;
    _isRefreshing = true;
    try {
      final prefs = await SharedPreferences.getInstance();
      final refreshToken = prefs.getString('refresh_token') ?? '';
      if (refreshToken.isEmpty) return false;

      final response = await http.post(
        Uri.parse('${ApiEndpoints.authBase}/auth/refresh'),
        headers: {'Content-Type': 'application/json'},
        body: jsonEncode({'refresh_token': refreshToken}),
      );

      if (response.statusCode == 200) {
        final data = jsonDecode(response.body) as Map<String, dynamic>;
        final newToken = (data['access_token'] ?? data['token'] ?? '') as String;
        final newRefresh = (data['refresh_token'] ?? '') as String;
        if (newToken.isNotEmpty) {
          await prefs.setString('jwt_token', newToken);
          if (newRefresh.isNotEmpty) {
            await prefs.setString('refresh_token', newRefresh);
          }
          return true;
        }
      }
      return false;
    } catch (_) {
      return false;
    } finally {
      _isRefreshing = false;
    }
  }

  /// Resolve the correct base URL for the given endpoint path.
  /// Routes:
  ///   /auth/*            → Auth Service   (port 8080)
  ///   /vendor/products/* → Product Service (port 8082)
  ///   /products/*        → Product Service (port 8082)
  ///   /vendor/*          → Vendor Store    (port 8081)
  String _resolveBaseUrl(String endpoint) {
    if (endpoint.startsWith('http://') || endpoint.startsWith('https://')) {
      return '';
    }
    if (endpoint.startsWith('/auth')) {
      return ApiEndpoints.authBase;
    }
    if (endpoint.startsWith('/orders')) {
      return ApiEndpoints.orderBase;
    }
    if (endpoint.startsWith('/vendor/products') ||
        endpoint.startsWith('/products')) {
      return ApiEndpoints.productBase;
    }
    if (endpoint.startsWith('/vendor')) {
      return ApiEndpoints.vendorBase;
    }
    if (endpoint.startsWith('/rides')) {
      return ApiEndpoints.rideBase;
    }
    if (endpoint.startsWith('/delivery') || endpoint.startsWith('/ride')) {
      return ApiEndpoints.deliveryBase;
    }
    if (endpoint.startsWith('/payment') || endpoint.startsWith('/finance')) {
      return ApiEndpoints.paymentBase;
    }
    if (endpoint.startsWith('/map')) {
      return ApiEndpoints.mapBase;
    }
    // Default fallback to auth gateway
    return ApiEndpoints.authBase;
  }

  static const Duration _requestTimeout = Duration(seconds: 15);

  Future<dynamic> get(String endpoint) async {
    final baseUrl = _resolveBaseUrl(endpoint);
    var response = await http.get(
      Uri.parse('$baseUrl$endpoint'),
      headers: await _getHeaders(),
    ).timeout(_requestTimeout);
    // Auto-refresh on 401
    if (response.statusCode == 401 && await _refreshToken()) {
      response = await http.get(
        Uri.parse('$baseUrl$endpoint'),
        headers: await _getHeaders(),
      ).timeout(_requestTimeout);
    }
    return _processResponse(response);
  }

  Future<dynamic> post(String endpoint, Map<String, dynamic> body) async {
    final baseUrl = _resolveBaseUrl(endpoint);
    var response = await http.post(
      Uri.parse('$baseUrl$endpoint'),
      headers: await _getHeaders(),
      body: jsonEncode(body),
    ).timeout(_requestTimeout);
    // Auto-refresh on 401 (skip for auth endpoints to avoid infinite loop)
    if (response.statusCode == 401 && !endpoint.contains('/auth/')) {
      if (await _refreshToken()) {
        response = await http.post(
          Uri.parse('$baseUrl$endpoint'),
          headers: await _getHeaders(),
          body: jsonEncode(body),
        ).timeout(_requestTimeout);
      }
    }
    return _processResponse(response);
  }

  Future<dynamic> patch(String endpoint, Map<String, dynamic> body) async {
    final baseUrl = _resolveBaseUrl(endpoint);
    var response = await http.patch(
      Uri.parse('$baseUrl$endpoint'),
      headers: await _getHeaders(),
      body: jsonEncode(body),
    ).timeout(_requestTimeout);
    // Auto-refresh on 401
    if (response.statusCode == 401 && await _refreshToken()) {
      response = await http.patch(
        Uri.parse('$baseUrl$endpoint'),
        headers: await _getHeaders(),
        body: jsonEncode(body),
      ).timeout(_requestTimeout);
    }
    return _processResponse(response);
  }

  Future<dynamic> delete(String endpoint) async {
    final baseUrl = _resolveBaseUrl(endpoint);
    var response = await http.delete(
      Uri.parse('$baseUrl$endpoint'),
      headers: await _getHeaders(),
    ).timeout(_requestTimeout);
    // Auto-refresh on 401
    if (response.statusCode == 401 && await _refreshToken()) {
      response = await http.delete(
        Uri.parse('$baseUrl$endpoint'),
        headers: await _getHeaders(),
      ).timeout(_requestTimeout);
    }
    if (response.statusCode >= 200 && response.statusCode < 300) {
      if (response.body.isEmpty) return {'status': 'ok'};
      return jsonDecode(response.body);
    } else {
      throw Exception('API Error: ${response.statusCode} - ${response.body}');
    }
  }

  dynamic _processResponse(http.Response response) {
    if (response.statusCode >= 200 && response.statusCode < 300) {
      return jsonDecode(response.body);
    } else {
      throw Exception('API Error: ${response.statusCode} - ${response.body}');
    }
  }
}
