import 'dart:async';
import 'dart:convert';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';
import 'api_endpoints.dart';

import 'dart:io';

class ApiClient {
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

  static Completer<bool>? _refreshCompleter;

  /// Attempt to refresh the JWT access token using the stored refresh token.
  /// Uses a shared Completer so all concurrent 401 callers wait on the single
  /// active refresh request instead of failing.
  Future<bool> _refreshToken() async {
    if (_refreshCompleter != null) {
      return _refreshCompleter!.future;
    }
    final completer = Completer<bool>();
    _refreshCompleter = completer;

    try {
      final prefs = await SharedPreferences.getInstance();
      final refreshToken = prefs.getString('refresh_token') ?? '';
      if (refreshToken.isEmpty) {
        completer.complete(false);
        return false;
      }

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
          completer.complete(true);
          return true;
        }
      }
      completer.complete(false);
      return false;
    } catch (_) {
      completer.complete(false);
      return false;
    } finally {
      _refreshCompleter = null;
    }
  }

  /// Resolve the correct base URL for the given endpoint path.
  /// Routes:
  ///   /auth/*            → Auth Service   (ApiEndpoints.authBase)
  ///   /orders/*          → Order Service  (ApiEndpoints.orderBase)
  ///   /vendor/products/* → Product Service (ApiEndpoints.productBase)
  ///   /products/*        → Product Service (ApiEndpoints.productBase)
  ///   /vendor/*          → Vendor Store   (ApiEndpoints.vendorBase)
  ///   /rides/*           → Ride Service   (ApiEndpoints.rideBase)
  ///   /delivery/*, /ride/* → Delivery Service (ApiEndpoints.deliveryBase)
  ///   /payment/*, /finance/*, /payments/* → Payment Orchestrator (ApiEndpoints.paymentBase)
  ///   /admin/*           → Admin Service  (ApiEndpoints.adminBase)
  ///   /geo/*, /geocoding/* → Geo Service  (ApiEndpoints.geoBase)
  ///   /map/*             → Map Service    (ApiEndpoints.gatewayBase/api/v1)
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
    if (endpoint.startsWith('/payment') ||
        endpoint.startsWith('/finance') ||
        endpoint.startsWith('/payments')) {
      return ApiEndpoints.paymentBase;
    }
    if (endpoint.startsWith('/admin')) {
      return ApiEndpoints.adminBase;
    }
    if (endpoint.startsWith('/geo') || endpoint.startsWith('/geocoding')) {
      return ApiEndpoints.geoBase;
    }
    if (endpoint.startsWith('/map')) {
      return '${ApiEndpoints.gatewayBase}/api/v1';
    }
    if (endpoint.startsWith('/api/v1')) {
      return ApiEndpoints.gatewayBase;
    }
    // Default fallback to auth gateway
    return ApiEndpoints.authBase;
  }

  /// Construct the final request URI safely preventing double `/api/v1` prefixes.
  Uri _buildUri(String endpoint) {
    if (endpoint.startsWith('http://') || endpoint.startsWith('https://')) {
      return Uri.parse(endpoint);
    }
    final baseUrl = _resolveBaseUrl(endpoint);
    if (endpoint.startsWith('/api/v1') && baseUrl.endsWith('/api/v1')) {
      return Uri.parse('$baseUrl${endpoint.substring(7)}');
    }
    if (baseUrl.endsWith('/') && endpoint.startsWith('/')) {
      return Uri.parse('$baseUrl${endpoint.substring(1)}');
    }
    return Uri.parse('$baseUrl$endpoint');
  }

  static const Duration _requestTimeout = Duration(seconds: 15);

  Future<dynamic> get(String endpoint) async {
    final uri = _buildUri(endpoint);
    var response = await http.get(
      uri,
      headers: await _getHeaders(),
    ).timeout(_requestTimeout);
    // Auto-refresh on 401 (skip for auth endpoints)
    if (response.statusCode == 401 && !endpoint.contains('/auth/') && await _refreshToken()) {
      response = await http.get(
        uri,
        headers: await _getHeaders(),
      ).timeout(_requestTimeout);
    }
    return _processResponse(response);
  }

  Future<dynamic> post(String endpoint, Map<String, dynamic> body) async {
    final uri = _buildUri(endpoint);
    var response = await http.post(
      uri,
      headers: await _getHeaders(),
      body: jsonEncode(body),
    ).timeout(_requestTimeout);
    // Auto-refresh on 401 (skip for auth endpoints to avoid infinite loop)
    if (response.statusCode == 401 && !endpoint.contains('/auth/')) {
      if (await _refreshToken()) {
        response = await http.post(
          uri,
          headers: await _getHeaders(),
          body: jsonEncode(body),
        ).timeout(_requestTimeout);
      }
    }
    return _processResponse(response);
  }

  Future<dynamic> put(String endpoint, Map<String, dynamic> body) async {
    final uri = _buildUri(endpoint);
    var response = await http.put(
      uri,
      headers: await _getHeaders(),
      body: jsonEncode(body),
    ).timeout(_requestTimeout);
    // Auto-refresh on 401
    if (response.statusCode == 401 && !endpoint.contains('/auth/')) {
      if (await _refreshToken()) {
        response = await http.put(
          uri,
          headers: await _getHeaders(),
          body: jsonEncode(body),
        ).timeout(_requestTimeout);
      }
    }
    return _processResponse(response);
  }

  Future<dynamic> patch(String endpoint, Map<String, dynamic> body) async {
    final uri = _buildUri(endpoint);
    var response = await http.patch(
      uri,
      headers: await _getHeaders(),
      body: jsonEncode(body),
    ).timeout(_requestTimeout);
    // SP-FL-04: same /auth/ guard as post()/put()/delete() — refreshing on a
    // failed auth-endpoint response causes an infinite refresh loop.
    if (response.statusCode == 401 && !endpoint.contains('/auth/') && await _refreshToken()) {
      response = await http.patch(
        uri,
        headers: await _getHeaders(),
        body: jsonEncode(body),
      ).timeout(_requestTimeout);
    }
    return _processResponse(response);
  }

  Future<dynamic> delete(String endpoint) async {
    final uri = _buildUri(endpoint);
    var response = await http.delete(
      uri,
      headers: await _getHeaders(),
    ).timeout(_requestTimeout);
    if (response.statusCode == 401 && !endpoint.contains('/auth/') && await _refreshToken()) {
      response = await http.delete(
        uri,
        headers: await _getHeaders(),
      ).timeout(_requestTimeout);
    }
    return _processResponse(response);
  }

  dynamic _processResponse(http.Response response) {
    if (response.statusCode >= 200 && response.statusCode < 300) {
      // BUG-20 FIX: Handle empty response bodies (e.g. 204 No Content).
      if (response.body.isEmpty) return {'status': 'ok'};
      return jsonDecode(response.body);
    } else {
      throw Exception('API Error: ${response.statusCode} - ${response.body}');
    }
  }
}
