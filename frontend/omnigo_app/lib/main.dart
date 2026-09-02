import 'dart:async';
import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:provider/provider.dart';
import 'package:flutter_stripe/flutter_stripe.dart';
import 'package:firebase_core/firebase_core.dart';
import 'package:firebase_messaging/firebase_messaging.dart';
import 'core/services/cart_provider.dart';
import 'core/services/session_registry.dart';
import 'core/services/notification_service.dart';
import 'core/network/websocket_client.dart';
import 'core/theme/app_theme.dart';
import 'core/di/service_locator.dart';
import 'features/auth/presentation/screens/login_screen.dart';
import 'features/auth/presentation/screens/dynamic_signup_screen.dart';
import 'features/customer/presentation/screens/customer_dashboard_screen.dart';
import 'features/vendor/presentation/screens/vendor_dashboard_screen.dart';
import 'features/vendor/presentation/screens/vendor_live_map_screen.dart';
import 'features/vendor/presentation/screens/vendor_inventory_screen.dart';
import 'features/vendor/presentation/screens/vendor_analytics_screen.dart';
import 'features/rider/presentation/screens/rider_map_screen.dart';
import 'features/rider/presentation/screens/rider_wallet_screen.dart';
import 'features/admin/presentation/screens/admin_surveillance_screen.dart';
import 'features/admin/presentation/screens/admin_finance_screen.dart';
import 'features/admin/presentation/screens/admin_ai_control_center_screen.dart';
import 'features/admin/presentation/screens/admin_orders_screen.dart';
import 'features/admin/presentation/screens/admin_disputes_screen.dart';
import 'features/admin/presentation/screens/admin_wallet_overview_screen.dart';
import 'features/admin/presentation/screens/admin_analytics_screen.dart';
import 'features/admin/presentation/screens/admin_rider_cod_screen.dart';
import 'features/admin/presentation/screens/admin_rider_gps_screen.dart';
import 'features/admin/presentation/screens/admin_vendor_payouts_screen.dart';
import 'features/admin/presentation/screens/admin_stripe_events_screen.dart';
import 'features/admin/presentation/screens/admin_saved_cards_screen.dart';
import 'features/admin/presentation/screens/admin_ledger_screen.dart';
import 'features/admin/presentation/screens/admin_reconciliation_screen.dart';
import 'features/admin/presentation/screens/admin_export_screen.dart';

import 'package:sentry_flutter/sentry_flutter.dart';

// Background FCM handler — must be top-level function
@pragma('vm:entry-point')
Future<void> _firebaseMessagingBackgroundHandler(RemoteMessage message) async {
  await Firebase.initializeApp();
  debugPrint('[FCM Background] ${message.messageId}: ${message.notification?.title}');
}

void main() async {
  WidgetsFlutterBinding.ensureInitialized();

  // Register shared singletons before any UI or service needs them.
  setupServiceLocator();

  // Initialize Firebase for push notifications
  try {
    await Firebase.initializeApp();
    FirebaseMessaging.onBackgroundMessage(_firebaseMessagingBackgroundHandler);
    await NotificationService().initialize();
  } catch (e) {
    debugPrint("[Firebase Init Warning]: $e");
  }

  // Initialize Stripe with publishable key.
  // Inject via build arg: --dart-define=STRIPE_PUBLISHABLE_KEY=pk_test_...
  Stripe.publishableKey = const String.fromEnvironment('STRIPE_PUBLISHABLE_KEY');
  unawaited(Stripe.instance.applySettings());

  try {
    // TimeoutException will be thrown if hydration takes more than 1500ms
    await SessionRegistry.instance.hydrate().timeout(
      const Duration(milliseconds: 1500),
    );
  } catch (e) {
    debugPrint("[Session Boot Warning]: SharedPreferences hydration failed or timed out: $e");
    // Fallback safely to guest/unauthenticated state to prevent engine boot lock
  }

  // Register FCM device token if user is already logged in
  if (SessionRegistry.instance.isLoggedIn) {
    unawaited(SessionRegistry.instance.registerFCMToken());
  }

  await SentryFlutter.init(
    (options) {
      // Inject via build arg: --dart-define=SENTRY_DSN=https://...
      // Never commit the DSN in source control.
      options.dsn = const String.fromEnvironment('SENTRY_DSN');
      // Set tracesSampleRate to 1.0 to capture 100% of transactions for performance monitoring.
      // We recommend adjusting this value in production.
      options.tracesSampleRate = 1.0;
    },
    appRunner: () => runApp(
      ChangeNotifierProvider(
        create: (context) => CartProvider()..loadCart(),
        child: const OmnigoApp(),
      ),
    ),
  );
}

class OmnigoApp extends StatefulWidget {
  const OmnigoApp({super.key});

  @override
  State<OmnigoApp> createState() => _OmnigoAppState();
}

class _OmnigoAppState extends State<OmnigoApp> with WidgetsBindingObserver {
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);

    // Edge-to-edge on Android so system bars draw behind app content.
    // On iOS this call is a no-op; SafeArea widgets handle insets there.
    if (!kIsWeb && (Platform.isAndroid || Platform.isIOS)) {
      SystemChrome.setEnabledSystemUIMode(SystemUiMode.edgeToEdge);
      SystemChrome.setSystemUIOverlayStyle(
        const SystemUiOverlayStyle(
          systemNavigationBarColor: Colors.transparent,
          statusBarColor: Colors.transparent,
          statusBarIconBrightness: Brightness.dark,
          statusBarBrightness: Brightness.light,
        ),
      );
    }
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  /// On Android, when the app goes to background the OS aggressively pauses
  /// the WebSocket. We mirror that on the Dart side to avoid noisy reconnect
  /// failures, then resume the link when the app is foregrounded.
  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    final ws = sl<WebSocketClient>();
    switch (state) {
      case AppLifecycleState.paused:
      case AppLifecycleState.detached:
      case AppLifecycleState.hidden:
        ws.pause();
        break;
      case AppLifecycleState.resumed:
        ws.resume();
        break;
      case AppLifecycleState.inactive:
        // Brief (e.g. incoming call overlay). Do not pause — keep telemetry.
        break;
    }
  }

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'OMNIGO',
      navigatorKey: NotificationService.navigatorKey,
      theme: AppTheme.lightTheme,
      debugShowCheckedModeBanner: false,
      initialRoute: '/',
      builder: (context, child) {
        // Use the full display on phones/tablets; only frame a mobile-sized
        // preview on large desktop / web canvases for local development.
        final shortestSide = MediaQuery.of(context).size.shortestSide;
        final isDesktopCanvas = kIsWeb || !Platform.isAndroid && !Platform.isIOS;
        final shouldFrame = isDesktopCanvas && shortestSide > 600;

        if (!shouldFrame) {
          return child ?? const SizedBox.shrink();
        }

        return Scaffold(
          backgroundColor: Colors.grey.shade900,
          body: Center(
            child: ClipRRect(
              borderRadius: BorderRadius.circular(40),
              child: Container(
                width: 400,
                height: 850,
                decoration: const BoxDecoration(
                  color: Colors.white,
                  boxShadow: [
                    BoxShadow(color: Colors.black26, blurRadius: 30, spreadRadius: 10),
                  ],
                ),
                child: child,
              ),
            ),
          ),
        );
      },
      onGenerateRoute: (settings) {
        final args = settings.arguments as String?;
        final name = settings.name;
        
        final isLoggedIn = SessionRegistry.instance.isLoggedIn;
        final role = SessionRegistry.instance.role;
        final trackingId = SessionRegistry.instance.trackingId;

        // Centralized Multi-Tenant Route Guard
        if (name == '/vendor-dashboard' || name == '/vendor-live-map' || name == '/vendor-inventory' || name == '/vendor-analytics') {
          if (!isLoggedIn) {
            // Not authenticated: Redirect to signup landing page in login mode
            return MaterialPageRoute(
              builder: (context) => const DynamicSignupScreen(startInLoginMode: true),
            );
          }
          if (role != 'vendor') {
            // Mismatched role check: redirect immediately to respective active page to prevent redirect loops
            return MaterialPageRoute(
              builder: (context) => role == 'rider'
                  ? RiderMapScreen(trackingId: trackingId ?? '')
                  : CustomerDashboardScreen(trackingId: trackingId ?? 'CUST-0000'),
            );
          }
        }

        // Admin Route Guard
        if (name == '/admin-surveillance' || name == '/admin-finance' || name == '/admin-ai-control' || name == '/admin-orders' || name == '/admin-disputes' || name == '/admin-wallet-overview' || name == '/admin-analytics' || name == '/admin-rider-cod' || name == '/admin-rider-gps' || name == '/admin-vendor-payouts' || name == '/admin-stripe-events' || name == '/admin-saved-cards' || name == '/admin-ledger' || name == '/admin-reconciliation' || name == '/admin-export') {
          if (!isLoggedIn) {
            return MaterialPageRoute(
              builder: (context) => const DynamicSignupScreen(startInLoginMode: true),
            );
          }
          if (role != 'admin' && role != 'super_admin') {
            return MaterialPageRoute(
              builder: (context) => role == 'rider'
                  ? RiderMapScreen(trackingId: trackingId ?? '')
                  : role == 'vendor'
                      ? VendorDashboardScreen(trackingId: trackingId ?? 'VEND-0000')
                      : CustomerDashboardScreen(trackingId: trackingId ?? 'CUST-0000'),
            );
          }
        }

        if (settings.name == '/login') {
          return MaterialPageRoute(
            builder: (context) => LoginScreen(role: args ?? 'customer'),
          );
        }
        if (settings.name == '/signup') {
          return MaterialPageRoute(
            builder: (context) => const DynamicSignupScreen(),
          );
        }
        if (settings.name == '/customer-dashboard') {
          return MaterialPageRoute(
            builder: (context) => CustomerDashboardScreen(trackingId: args ?? trackingId ?? 'CUST-0000'),
          );
        }
        if (settings.name == '/vendor-dashboard') {
          return MaterialPageRoute(
            builder: (context) => VendorDashboardScreen(
              trackingId: args ?? trackingId ?? 'VEND-0000',
            ),
          );
        }
        if (settings.name == '/vendor-live-map') {
          return MaterialPageRoute(
            builder: (context) => const VendorLiveMapScreen(),
          );
        }
        if (settings.name == '/vendor-inventory') {
          return MaterialPageRoute(
            builder: (context) => VendorInventoryScreen(
              vendorTrackingId: args ?? trackingId ?? 'VEND-0000',
            ),
          );
        }
        if (settings.name == '/vendor-analytics') {
          return MaterialPageRoute(
            builder: (context) => VendorAnalyticsScreen(
              vendorTrackingId: args ?? trackingId ?? 'VEND-0000',
            ),
          );
        }
        if (settings.name == '/rider-map') {
          return MaterialPageRoute(
            builder: (context) => RiderMapScreen(trackingId: args ?? trackingId ?? ''),
          );
        }
        if (settings.name == '/rider-wallet') {
          return MaterialPageRoute(
            builder: (context) => RiderWalletScreen(trackingId: args ?? trackingId ?? ''),
          );
        }
        if (settings.name == '/admin-surveillance') {
          return MaterialPageRoute(
            builder: (context) => const AdminSurveillanceScreen(),
          );
        }
        if (settings.name == '/admin-finance') {
          return MaterialPageRoute(
            builder: (context) => const AdminFinanceScreen(),
          );
        }
        if (settings.name == '/admin-ai-control') {
          return MaterialPageRoute(
            builder: (context) => const AdminAiControlCenterScreen(),
          );
        }
        if (settings.name == '/admin-orders') {
          return MaterialPageRoute(
            builder: (context) => const AdminOrdersScreen(),
          );
        }
        if (settings.name == '/admin-disputes') {
          return MaterialPageRoute(
            builder: (context) => const AdminDisputesScreen(),
          );
        }
        if (settings.name == '/admin-wallet-overview') {
          return MaterialPageRoute(
            builder: (context) => const AdminWalletOverviewScreen(),
          );
        }
        if (settings.name == '/admin-analytics') {
          return MaterialPageRoute(
            builder: (context) => const AdminAnalyticsScreen(),
          );
        }
        if (settings.name == '/admin-rider-cod') {
          return MaterialPageRoute(
            builder: (context) => const AdminRiderCodScreen(),
          );
        }
        if (settings.name == '/admin-rider-gps') {
          return MaterialPageRoute(
            builder: (context) => const AdminRiderGpsScreen(),
          );
        }
        if (settings.name == '/admin-vendor-payouts') {
          return MaterialPageRoute(
            builder: (context) => const AdminVendorPayoutsScreen(),
          );
        }
        if (settings.name == '/admin-stripe-events') {
          return MaterialPageRoute(
            builder: (context) => const AdminStripeEventsScreen(),
          );
        }
        if (settings.name == '/admin-saved-cards') {
          return MaterialPageRoute(
            builder: (context) => const AdminSavedCardsScreen(),
          );
        }
        if (settings.name == '/admin-ledger') {
          return MaterialPageRoute(
            builder: (context) => const AdminLedgerScreen(),
          );
        }
        if (settings.name == '/admin-reconciliation') {
          return MaterialPageRoute(
            builder: (context) => const AdminReconciliationScreen(),
          );
        }
        if (settings.name == '/admin-export') {
          return MaterialPageRoute(
            builder: (context) => const AdminExportScreen(),
          );
        }
        // #57: Return a proper 404 page instead of null
        return MaterialPageRoute(
          builder: (context) => Scaffold(
            appBar: AppBar(title: const Text('Page Not Found')),
            body: const Center(
              child: Column(
                mainAxisAlignment: MainAxisAlignment.center,
                children: [
                  Icon(Icons.error_outline, size: 64, color: Colors.grey),
                  SizedBox(height: 16),
                  Text('404 - Page Not Found', style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold)),
                  SizedBox(height: 8),
                  Text('The page you are looking for does not exist.'),
                ],
              ),
            ),
          ),
        );
      },
      routes: {
        '/': (context) => const DynamicSignupScreen(),
      },
    );
  }
}
