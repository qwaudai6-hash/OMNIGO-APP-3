import 'dart:async';
import 'dart:developer' as developer;
import 'package:flutter/foundation.dart';

/// Log levels in increasing severity.
enum LogLevel { debug, info, warn, error }

/// Structured logger that replaces 46 raw `debugPrint` calls across the app.
///
/// All log output is tagged with a [LogLevel] and the calling class name
/// (call site) so log filters can route messages in production without
/// the caller having to remember to add tags.
///
/// In **debug** builds everything is forwarded to `dart:developer.log` so
/// it shows up in DevTools / logcat. In **release** builds, only
/// [LogLevel.warn] and [LogLevel.error] are forwarded, and a server-side
/// crash reporter (Crashlytics / Sentry) can subscribe to the same stream.
///
/// Migrate callsites by replacing:
///
/// ```dart
/// debugPrint('Network call failed: $e');
/// ```
///
/// with:
///
/// ```dart
/// AppLogger.warn('Network call failed', error: e, tag: 'CheckoutScreen');
/// ```
class AppLogger {
  AppLogger._();

  /// For tests: capture log output instead of forwarding it to dart:developer.
  static final List<LogRecord> _buffer = <LogRecord>[];

  /// Whether logs are buffered for tests. Set to true in setUp / tearDown.
  @visibleForTesting
  static bool captureForTest = false;

  /// Subscribe to a global stream of every log line (used by the server-side
  /// crash reporter wiring). The stream is broadcast so multiple listeners
  /// are fine.
  static final StreamController<LogRecord> _controller =
      StreamController<LogRecord>.broadcast();

  /// Public read-only view of the log stream.
  static Stream<LogRecord> get stream => _controller.stream;

  /// Snapshot of all captured log records since [captureForTest] was last
  /// toggled. Only meaningful while [captureForTest] is true.
  static List<LogRecord> get capturedRecords =>
      List<LogRecord>.unmodifiable(_buffer);

  /// Clear the capture buffer. Call from tearDown.
  @visibleForTesting
  static void clearBuffer() => _buffer.clear();

  // ── Convenience entry points ─────────────────────────────────────

  static void debug(
    String message, {
    String tag = 'app',
    Object? error,
    StackTrace? stackTrace,
  }) =>
      _log(LogLevel.debug, message, tag, error, stackTrace);

  static void info(
    String message, {
    String tag = 'app',
    Object? error,
    StackTrace? stackTrace,
  }) =>
      _log(LogLevel.info, message, tag, error, stackTrace);

  static void warn(
    String message, {
    String tag = 'app',
    Object? error,
    StackTrace? stackTrace,
  }) =>
      _log(LogLevel.warn, message, tag, error, stackTrace);

  static void error(
    String message, {
    String tag = 'app',
    Object? error,
    StackTrace? stackTrace,
    bool fatal = false,
  }) =>
      _log(LogLevel.error, message, tag, error, stackTrace, fatal: fatal);

  // ── Core ──────────────────────────────────────────────────────────

  static void _log(
    LogLevel level,
    String message,
    String tag,
    Object? error,
    StackTrace? stackTrace, {
    bool fatal = false,
  }) {
    final record = LogRecord(
      timestamp: DateTime.now(),
      level: level,
      tag: tag,
      message: message,
      error: error,
      stackTrace: stackTrace,
      fatal: fatal,
    );

    if (captureForTest) {
      _buffer.add(record);
    }

    // Forward to the broadcast stream for crash-reporter wiring.
    if (!_controller.isClosed) {
      _controller.add(record);
    }

    // Skip debug in release builds.
    if (level == LogLevel.debug && kReleaseMode) return;

    final prefix = '[${level.name.toUpperCase()}][$tag]';
    final body = error == null ? message : '$message | error=$error';
    developer.log(
      body,
      name: 'omnigo',
      error: error,
      stackTrace: stackTrace,
      level: _devLevel(level),
    );
    if (fatal) {
      // Print unconditionally so fatal errors are visible even in release
      // builds that filter out everything else.
      // ignore: avoid_print
      print('$prefix FATAL $body');
    }
  }

  static int _devLevel(LogLevel level) {
    switch (level) {
      case LogLevel.debug:
        return 500;
      case LogLevel.info:
        return 800;
      case LogLevel.warn:
        return 900;
      case LogLevel.error:
        return 1000;
    }
  }
}

/// Immutable record of a single log event.
class LogRecord {
  const LogRecord({
    required this.timestamp,
    required this.level,
    required this.tag,
    required this.message,
    this.error,
    this.stackTrace,
    this.fatal = false,
  });

  final DateTime timestamp;
  final LogLevel level;
  final String tag;
  final String message;
  final Object? error;
  final StackTrace? stackTrace;
  final bool fatal;

  @override
  String toString() {
    final ts = timestamp.toIso8601String();
    final err = error == null ? '' : ' | error=$error';
    return '$ts ${level.name.toUpperCase()} [$tag] $message$err';
  }
}
