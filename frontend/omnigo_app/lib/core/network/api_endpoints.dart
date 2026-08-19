/// Centralized API endpoint registry.
///
/// ARCHITECTURE: Flutter talks to EXACTLY ONE public URL — the API Gateway.
/// The gateway (cmd/gateway) is the single public Railway service. All
/// microservices (auth, product, order, payment, ride, delivery, ai, ws)
/// are private and only reachable through the gateway via Railway private
/// networking. Flutter never knows about individual service ports.
///
///   Flutter  →  https://omnigo-app-production.up.railway.app  →  Gateway  →  internal services
///
/// Override via `--dart-define=API_HOST=10.0.2.2` for local dev (gateway on :8000).
class ApiEndpoints {
  ApiEndpoints._();

  // ── Host Resolution ──────────────────────────────────────────────
  static final String _host = _resolveHost();

  static String _resolveHost() {
    const override = String.fromEnvironment('API_HOST');
    if (override.isNotEmpty) return override;

    // Production Railway Domain
    return 'omnigo-app-production.up.railway.app';
  }

  static bool get _isLocal =>
      _host.contains('10.0.2.2') || _host.contains('127.0.0.1');

  // ── Service Base URLs ──────────────────────────────────────────
  // Local: Gateway on port 8000 (HTTP) — use --dart-define=API_HOST=10.0.2.2
  // Prod: Railway handles TLS (HTTPS) on default port 443
  static String get gatewayBase =>
      _isLocal ? 'http://$_host:8000' : 'https://$_host';

  static String get authBase => '$gatewayBase/api/v1';
  static String get vendorBase => '$gatewayBase/api/v1';
  static String get productBase => '$gatewayBase/api/v1';
  static String get adminBase => '$gatewayBase/api/v1';
  static String get deliveryBase => '$gatewayBase/api/v1';
  static String get orderBase => '$gatewayBase/api/v1';
  static String get rideBase => '$gatewayBase/api/v1';
  static String get paymentBase => '$gatewayBase/api/v1';
  static String get geoBase => '$gatewayBase/api/v1';

  // WebSocket goes through the gateway in both local and production.
  static String get wsUrl =>
      _isLocal ? 'ws://$_host:8000/ws' : 'wss://$_host/ws';

  // ── Auth Service ─────────────────────────────────────────────────
  static String authLogin() => '$authBase/auth/login';
  static String authRegister() => '$authBase/auth/register';
  static String authRefresh() => '$authBase/auth/refresh';
  static String authProfile() => '$authBase/auth/profile';

  // ── Auth flow (forgot password, email verification, 2FA) ─────────
  static String authForgotPassword() => '$authBase/auth/forgot-password';
  static String authResetPassword() => '$authBase/auth/reset-password';
  static String authVerifyEmail() => '$authBase/auth/verify-email';
  static String authSendVerificationEmail() =>
      '$authBase/auth/verify-email/send';
  static String auth2FAEnroll() => '$authBase/auth/2fa/enroll';
  static String auth2FAVerifyEnrollment() =>
      '$authBase/auth/2fa/verify-enrollment';
  static String auth2FADisable() => '$authBase/auth/2fa/disable';
  static String authDeviceToken() => '$authBase/auth/device-token';
  static String authKycUpload() => '$authBase/auth/kyc';
  static String authVendorVerify() => '$authBase/auth/vendor/verify';

  // ── Vendor Store ─────────────────────────────────────────────────
  static String vendorStoreMe() => '$vendorBase/vendor/stores/me';
  static String vendorStore(String trackingId) =>
      '$vendorBase/stores/$trackingId';

  // ── Vendor Metrics & Wallet ──────────────────────────────────────
  static String vendorMetrics(String vendorId) =>
      '$vendorBase/vendor/metrics?vendor_id=$vendorId';
  static String vendorWallet(String vendorId) =>
      '$paymentBase/payments/vendor/wallet/$vendorId';
  static String vendorEscrowHolds(String vendorId) =>
      '$paymentBase/payments/escrow/holds/$vendorId';
  static String vendorPayouts(String vendorId) =>
      '$paymentBase/payments/vendor/payouts/$vendorId';

  // ── Product Catalog (public) ─────────────────────────────────────
  static String productsList({
    int limit = 100,
    int offset = 0,
    String search = '',
    String category = '',
    String sort = '',
    double minPrice = 0.0,
    double maxPrice = 0.0,
  }) {
    final params = <String>[];
    params.add('limit=$limit');
    params.add('offset=$offset');
    if (search.isNotEmpty) params.add('search=${Uri.encodeComponent(search)}');
    if (category.isNotEmpty && category != 'All') {
      params.add('category=${Uri.encodeComponent(category)}');
    }
    if (sort.isNotEmpty) params.add('sort=${Uri.encodeComponent(sort)}');
    if (minPrice > 0) params.add('min_price=$minPrice');
    if (maxPrice > 0) params.add('max_price=$maxPrice');
    return '$productBase/products?${params.join('&')}';
  }

  static String productById(String productId) =>
      '$productBase/products/$productId';
  static String productRecommendations(String productId) =>
      '$productBase/products/$productId/recommendations';
  static String productCreate() => '$productBase/products/';

  // ── Vendor Product CRUD ──────────────────────────────────────────
  static String vendorProducts({int limit = 20, int offset = 0}) =>
      '$productBase/vendor/products?limit=$limit&offset=$offset';
  static String vendorProductCreate() => '$productBase/vendor/products/';
  static String vendorProductUpdate(String productId) =>
      '$productBase/vendor/products/$productId';
  static String vendorProductDelete(String productId) =>
      '$productBase/vendor/products/$productId';
  static String productStockToggle(String productId) =>
      '$productBase/vendor/products/$productId/stock';

  // ── Wishlist (Favorites) ─────────────────────────────────────────
  static String wishlistToggle(String productId) =>
      '$productBase/wishlist/$productId';
  static String wishlistList() => '$productBase/wishlist/';
  static String wishlistRemove(String productId) =>
      '$productBase/wishlist/$productId';

  // ── Reviews ──────────────────────────────────────────────────────
  static String reviewCreate() => '$productBase/reviews/';
  static String reviewList(String productId) =>
      '$productBase/reviews/$productId';
  static String reviewSummary(String productId) =>
      '$productBase/reviews/$productId/summary';

  // ── Ratings (NEW — Week 3) ────────────────────────────────────────
  static String ratingCreate() => '$orderBase/ratings/';
  static String ratingForUser(String trackingId) =>
      '$orderBase/ratings/$trackingId';

  // ── Chat v1 (NEW — Week 3) ─────────────────────────────────────────
  static String chatSend() => '$orderBase/chat/messages';
  static String chatList({String? thread, int limit = 50, int offset = 0}) {
    final params = <String>['limit=$limit', 'offset=$offset'];
    if (thread != null && thread.isNotEmpty) {
      params.add('thread=${Uri.encodeComponent(thread)}');
    }
    return '$orderBase/chat/messages?${params.join('&')}';
  }

  // ── Chat v2 (conversation list + unread badge) ────────────────────
  static String get chatMessages => '$orderBase/chat/messages';
  static String get chatConversations => '$orderBase/chat/conversations';
  static String get chatUnread => '$orderBase/chat/unread';
  static String chatMarkRead(String orderId) =>
      '$orderBase/chat/messages/$orderId/read';

  // ── Payments (Order & Payment Orchestrator Service) ──────────────
  static String payfastPayment() => '$paymentBase/payments/payfast/payment';
  static String payfast3DSCallback() => '$paymentBase/payments/payfast/3ds_callback';
  static String savedCards() => '$paymentBase/payments/cards';
  static String savedCard(String cardId) => '$paymentBase/payments/cards/$cardId';
  static String defaultSavedCard() => '$paymentBase/payments/cards/default';
  static String payfastCharge() => '$orderBase/wallet/payfast/charge';
  static String payfastCallback() => '$orderBase/wallet/payfast/callback';

  // ── Customer Mobile Wallet (JazzCash / EasyPaisa / PayFast) ────
  static String customerWallet(String trackingId) =>
      '$orderBase/wallet/customer/$trackingId';
  static String customerWalletLoad() => '$orderBase/wallet/customer/load';
  static String customerWalletLoadCallback() =>
      '$orderBase/wallet/customer/load/callback';
  static String jazzcashInitiate() => '$paymentBase/payments/jazzcash/initiate';
  static String jazzcashCallback() => '$paymentBase/payments/jazzcash/callback';
  static String easypaisaInitiate() => '$paymentBase/payments/easypaisa/initiate';
  static String easypaisaCallback() => '$paymentBase/payments/easypaisa/callback';
  static String orderCheckout() => '$orderBase/orders/';
  static String orderConfirm() => '$orderBase/orders/confirm';
  static String orderHandover() => '$orderBase/orders/handover';
  static String customerOrders(String customerId) =>
      '$orderBase/orders/customer/$customerId';
  static String vendorOrders(String vendorId) =>
      '$orderBase/orders/vendor/$vendorId';
  static String updateOrderStatus(String orderId) =>
      '$orderBase/orders/$orderId/status';

  // ── Refunds / Cancellations (Order Service) ──────────────────────
  static String refundRequest() => '$orderBase/finance/refund';
  static String cancelOrder() => '$orderBase/finance/cancel';
  static String refundStatus(String orderId) =>
      '$orderBase/finance/refund/$orderId';

  // ── Stripe Card Checkout (Order Service) ───────────────────────────
  static String stripeCheckout() => '$orderBase/payment/checkout';

  // ── Delivery Gigs ────────────────────────────────────────────────
  static String deliveryGigAccept() => '$deliveryBase/delivery/gig/accept';
  static String uploadProof() => '$deliveryBase/delivery/gig/upload-proof';

  // ── Ride Bidding (Customer ↔ Rider) ─────────────────────────────
  static String rideSubmitBid(String rideId) =>
      '$rideBase/rides/$rideId/bid';
  static String rideListBids(String rideId) =>
      '$rideBase/rides/$rideId/bids';
  static String rideAcceptBid(String rideId) =>
      '$rideBase/rides/$rideId/accept-bid';

  // ── Delivery Bidding (Rider ↔ Vendor) ───────────────────────────
  static String deliveryBid() => '$rideBase/ride/bid';
  static String deliveryBidCounter() => '$rideBase/ride/bid/counter';
  static String deliverySurgeHeatmap() => '$deliveryBase/delivery/surge-heatmap';

  // ── AI Engine (Frequently Bought Together) ──────────────────────
  static String aiFrequentlyBoughtTogether() =>
      '$orderBase/ai/frequently-bought-together';

  // ── Admin Analytics (Demand Heatmap) ───────────────────────────
  static String adminDemandHeatmap() => '$adminBase/admin/analytics/demand-heatmap';

  // ── Admin Verifications ────────────────────────────────────────
  static String adminVerificationDetail(String id) =>
      '$adminBase/admin/verifications/$id';
  static String adminVerificationSubmit(String id) =>
      '$adminBase/admin/verifications/$id/submit';
  static String adminVerificationApprove(String id) =>
      '$adminBase/admin/verifications/$id/approve';
  static String adminVerificationReject(String id) =>
      '$adminBase/admin/verifications/$id/reject';

  // ── Admin Disputes ─────────────────────────────────────────────
  static String adminDisputeList() => '$paymentBase/payments/disputes';
  static String adminDisputeResolve(String id) =>
      '$paymentBase/admin/disputes/$id/resolve';

  // ── Admin COD Settlement ──────────────────────────────────────
  static String adminCodSettle() => '$paymentBase/payments/cod/settlement';

  // ── Ratings Display ────────────────────────────────────────────
  static String ratingListFor(String trackingId) =>
      '$orderBase/ratings/$trackingId';

  // ── Product Image File Upload ─────────────────────────────────
  static String productImageUpload() => '$productBase/products/upload-image';
  static String deliveryGigStatusUpdate(String gigId) =>
      '$deliveryBase/delivery/gig/$gigId/status';
  static String deliveryGigCancel() => '$deliveryBase/delivery/gig/cancel';
  static String deliveryGigRoute(String gigId) =>
      '$deliveryBase/delivery/gig/$gigId/route';
  static String deliveryGigUploadProof() =>
      '$deliveryBase/delivery/gig/upload-proof';
  static String deliveryGigDispute() => '$deliveryBase/delivery/gig/dispute';
  static String deliveryLocationUpdate() => '$deliveryBase/delivery/location';
  static String rideEstimate() => '$deliveryBase/ride/estimate';

  // ── Ride Lifecycle (NEW — Week 1, B11) ────────────────────────────
  static String rideCreate() => '$rideBase/rides/';
  static String rideById(String rideId) => '$rideBase/rides/$rideId';
  static String rideAccept(String rideId) => '$rideBase/rides/$rideId/accept';
  static String rideUpdateStatus(String rideId) =>
      '$rideBase/rides/$rideId/status';
  static String rideComplete(String rideId) =>
      '$rideBase/rides/$rideId/complete';
  static String rideCancel(String rideId) => '$rideBase/rides/$rideId/cancel';

  // ── Rider Wallet ─────────────────────────────────────────────────
  static String riderWallet(String riderId) =>
      '$orderBase/wallet/rider/$riderId';

  // ── COD + JazzCash + EasyPaisa (Payment Orchestrator :8092) ──────
  static String codConfirm() => '$paymentBase/payments/cod/confirm';
  static String codPayNow() => '$paymentBase/payments/cod/pay-now';
  static String codSettlement() => '$paymentBase/payments/cod/settlement';
  static String codDebts(String riderId) =>
      '$paymentBase/payments/cod/debts?rider_id=$riderId';

  // JazzCash & EasyPaisa status queries
  static String jazzcashStatus(String txnRef) =>
      '$paymentBase/payments/jazzcash/status/$txnRef';
  static String easypaisaStatus(String txnRef) =>
      '$paymentBase/payments/easypaisa/status/$txnRef';

  // Disputes
  static String disputeCreate() => '$paymentBase/payments/disputes';
  static String disputeById(String id) => '$paymentBase/payments/disputes/$id';
  static String disputeList({String? status}) {
    final params = <String>[];
    if (status != null && status.isNotEmpty) params.add('status=$status');
    final qs = params.isEmpty ? '' : '?${params.join('&')}';
    return '$paymentBase/payments/disputes$qs';
  }

  // Admin: Lineage + KYC + Finance
  static String adminLineage(String orderId) =>
      '$adminBase/admin/lineage/$orderId';
  static String adminLineageFull(String orderId) =>
      '$adminBase/admin/lineage/$orderId/full';
  static String adminUsersPending() => '$adminBase/admin/users/pending';
  static String adminUserApprove(String trackingId) =>
      '$adminBase/admin/users/$trackingId/approve';
  static String adminUsers({String? role, int limit = 50, int offset = 0}) {
    final params = <String>['limit=$limit', 'offset=$offset'];
    if (role != null && role.isNotEmpty) params.add('role=$role');
    return '$adminBase/admin/users?${params.join('&')}';
  }

  static String adminFinanceLedgerKpis() =>
      '$adminBase/admin/finance/ledger-kpis';
  static String adminFinanceDailyRevenue({int days = 30, String? method}) {
    final params = <String>['days=$days'];
    if (method != null && method.isNotEmpty) {
      params.add('payment_method=$method');
    }
    return '$adminBase/admin/finance/daily-revenue?${params.join('&')}';
  }

  static String adminFinancePayments({int limit = 50}) =>
      '$adminBase/admin/finance/payments?limit=$limit';
  static String adminFinanceApiKeys() => '$adminBase/admin/finance/api-keys';
  static String adminFinanceApiKeySet() => '$adminBase/admin/finance/api-keys';
  static String adminFinanceApiKeyDelete(String provider, String keyName) =>
      '$adminBase/admin/finance/api-keys/$provider/$keyName';

  // ── Map Service (MapLibre) ───────────────────────────────────────
  static String get mapBase => '$gatewayBase/api/v1/map';

  /// Style JSON served by internal map-service. Auth token is appended
  /// so the gateway can validate the request before serving the style.
  /// If no token is provided, the style is served without auth (useful for
  /// public style previews or when the gateway handles auth differently).
  static String mapStyle([String? token]) {
    if (token == null || token.isEmpty) {
      return '$mapBase/style.json';
    }
    return '$mapBase/style.json?access_token=${Uri.encodeComponent(token)}';
  }

  /// Proxy a MapTiler / self-hosted tile through the internal map-service.
  /// [source] is 'tiles' for now; can be extended to 'satellite', 'terrain'.
  static String mapTileProxy(int z, int x, int y, {String source = 'tiles'}) =>
      '$mapBase/tiles/$source/$z/$x/$y';

  /// Proxy glyph and sprite requests.
  static String mapGlyphs(String fontstack, int start, int end) =>
      '$mapBase/glyphs/$fontstack/$start-$end.pbf';

  static String mapSprites({required String id, String? extension}) {
    final ext = extension ?? 'json';
    return '$mapBase/sprites/$id@2x.$ext';
  }

  // ── AI Security & Self-Healing Control Center ───────────────────
  static String adminAiAuditOverview() => '$adminBase/admin/ai/audit-overview';
  static String adminAiAutoHeal() => '$adminBase/admin/i/auto-heal';

  // Geocoding — uses the dedicated geoBase (unified with vendor/admin)
  static String geocodingSearch(String q) =>
      '$geoBase/geocoding/search?q=${Uri.encodeComponent(q)}';
  static String geocodingReverse(double lat, double lng) =>
      '$geoBase/geo/reverse?lat=$lat&lng=$lng';
}
