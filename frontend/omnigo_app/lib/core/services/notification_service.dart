import 'dart:async';
import 'dart:io';

import 'package:firebase_messaging/firebase_messaging.dart';
import 'package:flutter/material.dart';
import 'package:flutter_local_notifications/flutter_local_notifications.dart';

/// Production-grade local-notification bridge for OMNIGO.
///
/// Creates Android notification channels and surfaces foreground FCM messages
/// as local notifications so users see alerts while the app is open. On iOS it
/// requests alert/badge/sound permissions via APNs / FCM.
class NotificationService {
  static final NotificationService _instance = NotificationService._internal();
  factory NotificationService() => _instance;
  NotificationService._internal();

  final FlutterLocalNotificationsPlugin _notifications =
      FlutterLocalNotificationsPlugin();

  bool _initialized = false;

  /// Android channel IDs mirrored in [AndroidManifest.xml] meta-data and
  /// telemetry background-service config.
  static const String _defaultChannelId = 'omnigo_default';
  static const String _riderChannelId = 'rider_telemetry';
  static const String _orderChannelId = 'order_updates';

  /// Initializes channels, requests permissions, and wires foreground FCM
  /// messages to local notifications.
  Future<void> initialize() async {
    if (_initialized) return;

    const AndroidInitializationSettings androidInit =
        AndroidInitializationSettings('@mipmap/ic_launcher');

    const DarwinInitializationSettings iosInit = DarwinInitializationSettings(
      requestAlertPermission: true,
      requestBadgePermission: true,
      requestSoundPermission: true,
    );

    const InitializationSettings initSettings = InitializationSettings(
      android: androidInit,
      iOS: iosInit,
      macOS: iosInit,
    );

    await _notifications.initialize(
      initSettings,
      onDidReceiveNotificationResponse: _onNotificationTapped,
    );

    if (Platform.isAndroid) {
      await _createAndroidChannels();
    }

    // Surface foreground FCM messages as local notifications.
    FirebaseMessaging.onMessage.listen(_handleForegroundMessage);

    _initialized = true;
  }

  Future<void> _createAndroidChannels() async {
    final plugin = _notifications
        .resolvePlatformSpecificImplementation<
            AndroidFlutterLocalNotificationsPlugin>();
    if (plugin == null) return;

    await plugin.createNotificationChannelGroup(
      const AndroidNotificationChannelGroup('omnigo', 'OMNIGO'),
    );

    const channels = <AndroidNotificationChannel>[
      AndroidNotificationChannel(
        _defaultChannelId,
        'General',
        description: 'General OMNIGO alerts and announcements',
        importance: Importance.high,
        groupId: 'omnigo',
      ),
      AndroidNotificationChannel(
        _riderChannelId,
        'Rider Telemetry',
        description: 'Live gig and ride assignments for riders',
        importance: Importance.high,
        groupId: 'omnigo',
        enableVibration: true,
        enableLights: true,
      ),
      AndroidNotificationChannel(
        _orderChannelId,
        'Order Updates',
        description: 'Order confirmations, rider assignments, and delivery status',
        importance: Importance.high,
        groupId: 'omnigo',
        enableVibration: true,
      ),
    ];

    for (final channel in channels) {
      await plugin.createNotificationChannel(channel);
    }
  }

  void _handleForegroundMessage(RemoteMessage message) {
    final notification = message.notification;
    if (notification == null) return;

    final channelId = _channelIdFor(message.data);

    _notifications.show(
      message.hashCode,
      notification.title,
      notification.body,
      NotificationDetails(
        android: AndroidNotificationDetails(
          channelId,
          _channelNameFor(channelId),
          channelDescription: _channelDescriptionFor(channelId),
          importance: Importance.high,
          priority: Priority.high,
          icon: '@mipmap/ic_launcher',
        ),
        iOS: const DarwinNotificationDetails(
          presentAlert: true,
          presentBadge: true,
          presentSound: true,
        ),
      ),
      payload: message.data['route']?.toString(),
    );
  }

  static String _channelIdFor(Map<String, dynamic> data) {
    final type = data['type']?.toString().toLowerCase();
    if (type == 'gig' || type == 'ride') return _riderChannelId;
    if (type == 'order') return _orderChannelId;
    return _defaultChannelId;
  }

  static String _channelNameFor(String channelId) {
    switch (channelId) {
      case _riderChannelId:
        return 'Rider Telemetry';
      case _orderChannelId:
        return 'Order Updates';
      default:
        return 'General';
    }
  }

  static String _channelDescriptionFor(String channelId) {
    switch (channelId) {
      case _riderChannelId:
        return 'Live gig and ride assignments for riders';
      case _orderChannelId:
        return 'Order confirmations, rider assignments, and delivery status';
      default:
        return 'General OMNIGO alerts and announcements';
    }
  }

  GlobalKey<NavigatorState>? navigatorKey;

  void _onNotificationTapped(NotificationResponse response) {
    final route = response.payload;
    if (route == null || route.isEmpty) return;
    debugPrint('[Notification tap] payload=$route');
    if (navigatorKey?.currentState != null) {
      navigatorKey!.currentState!.pushNamed(route);
    }
  }

  /// Requests iOS notification permissions explicitly.
  Future<bool> requestIOSPermissions() async {
    if (!Platform.isIOS && !Platform.isMacOS) return true;
    final settings = await _notifications
        .resolvePlatformSpecificImplementation<
            IOSFlutterLocalNotificationsPlugin>()
        ?.requestPermissions(alert: true, badge: true, sound: true);
    return settings ?? false;
  }
}
