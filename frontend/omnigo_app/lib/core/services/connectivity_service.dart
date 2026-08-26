import 'dart:async';
import 'package:connectivity_plus/connectivity_plus.dart';

class ConnectivityService {
  final Connectivity _connectivity = Connectivity();
  
  // Checks current hardware-level connection status.
  Future<bool> isOnline() async {
    try {
      final result = await _connectivity.checkConnectivity();
      return _evaluateConnection(result);
    } catch (_) {
      return true; // Fallback to online so HTTP request can attempt
    }
  }

  // Returns Stream of connection updates, casting dynamic changes to solve v4/v5 library signature mismatches.
  Stream<bool> get onConnectivityChanged {
    return _connectivity.onConnectivityChanged.map((result) => _evaluateConnection(result));
  }

  bool _evaluateConnection(dynamic result) {
    if (result is List) {
      if (result.isEmpty) return false;
      for (var r in result) {
        if (r != ConnectivityResult.none) return true;
      }
      return false;
    }
    return result != ConnectivityResult.none;
  }
}
