import 'dart:async';
import 'dart:io';

class UserFriendlyError {
  const UserFriendlyError({
    required this.title,
    required this.message,
    this.isNetworkIssue = false,
  });

  /// Formats raw technical exceptions into polite, user-friendly messages.
  factory UserFriendlyError.fromException(dynamic error) {
    final str = error.toString().toLowerCase();

    if (error is TimeoutException || str.contains('timeoutexception') || str.contains('timed out')) {
      return const UserFriendlyError(
        title: 'Connection Timeout',
        message: 'The server is taking longer than expected to respond. Please check your internet connection or try again in a moment.',
        isNetworkIssue: true,
      );
    }

    if (error is SocketException || str.contains('socketexception') || str.contains('connection refused') || str.contains('network is unreachable') || str.contains('failed host lookup')) {
      return const UserFriendlyError(
        title: 'Network Disconnected',
        message: 'Unable to reach OMNIGO servers. Please ensure mobile data or Wi-Fi is enabled.',
        isNetworkIssue: true,
      );
    }

    if (str.contains('401') || str.contains('unauthorized') || str.contains('invalid credentials') || str.contains('invalid email address or password') || str.contains('invalid email or password')) {
      return const UserFriendlyError(
        title: 'Authentication Failed',
        message: 'Incorrect email or password. Please verify your details and try again.',
      );
    }

    if (str.contains('403') || str.contains('forbidden_pending_verification')) {
      return const UserFriendlyError(
        title: 'Account Verification Pending',
        message: 'Your account is under review by our admin team. You will be notified once approved.',
      );
    }

    if (str.contains('409') || str.contains('conflict_duplicate_email') || str.contains('already registered') || str.contains('already exists')) {
      return const UserFriendlyError(
        title: 'Email Already Registered',
        message: 'An account with this email already exists. Please log in instead or use another email.',
      );
    }

    if (str.contains('502') || str.contains('503') || str.contains('504')) {
      return const UserFriendlyError(
        title: 'Server Maintenance',
        message: 'OMNIGO cloud servers are temporarily undergoing maintenance. Please try again shortly.',
        isNetworkIssue: true,
      );
    }

    // Try extracting clean JSON error message if present: e.g. {"error":"..."}
    final errorMatch = RegExp(r'"error"\s*:\s*"([^"]+)"').firstMatch(error.toString());
    if (errorMatch != null && errorMatch.group(1) != null) {
      final msg = errorMatch.group(1)!;
      return UserFriendlyError(
        title: 'Notice',
        message: msg,
      );
    }

    // Default polite fallback
    return const UserFriendlyError(
      title: 'Action Couldn\'t Be Completed',
      message: 'Something unexpected occurred while processing your request. Please try again later.',
    );
  }

  final String title;
  final String message;
  final bool isNetworkIssue;
}
