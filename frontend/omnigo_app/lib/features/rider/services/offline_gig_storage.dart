import 'dart:convert';
import 'package:hive_flutter/hive_flutter.dart';

class OfflineGigStorage {
  static const String boxName = 'active_gig_box';
  static const String gigKey = 'current_gig';
  static const String pendingSyncKey = 'pending_status_sync';

  static Future<void> init() async {
    if (!Hive.isBoxOpen(boxName)) {
      await Hive.openBox<String>(boxName);
    }
  }

  static Box<String> _getBox() {
    if (!Hive.isBoxOpen(boxName)) {
      return Hive.box<String>(boxName);
    }
    return Hive.box<String>(boxName);
  }

  // Save the currently active gig data
  static Future<void> saveActiveGig(Map<String, dynamic> gigData) async {
    final box = _getBox();
    await box.put(gigKey, jsonEncode(gigData));
  }

  // Get the active gig data if it exists
  static Map<String, dynamic>? getActiveGig() {
    if (!Hive.isBoxOpen(boxName)) return null;
    final box = _getBox();
    final data = box.get(gigKey);
    if (data != null) {
      return jsonDecode(data) as Map<String, dynamic>;
    }
    return null;
  }

  // Update the status of the current gig locally
  static Future<void> updateGigStatusLocally(String newStatus) async {
    final gig = getActiveGig();
    if (gig != null) {
      gig['status'] = newStatus;
      await saveActiveGig(gig);
    }
  }

  // Clear when gig is completed or cancelled
  static Future<void> clearActiveGig() async {
    if (!Hive.isBoxOpen(boxName)) return;
    final box = _getBox();
    await box.delete(gigKey);
    await box.delete(pendingSyncKey);
  }

  // Save a pending status sync request to retry when online
  static Future<void> queuePendingStatusSync(Map<String, dynamic> payload) async {
    final box = _getBox();
    List<dynamic> queue = [];
    final raw = box.get(pendingSyncKey);
    if (raw != null) {
      try {
        queue = jsonDecode(raw) as List<dynamic>;
      } catch (_) {
        queue = [];
      }
    }
    queue.add(payload);
    await box.put(pendingSyncKey, jsonEncode(queue));
  }

  // Get and clear pending sync request
  static Future<Map<String, dynamic>?> consumePendingStatusSync() async {
    if (!Hive.isBoxOpen(boxName)) return null;
    final box = _getBox();
    final data = box.get(pendingSyncKey);
    if (data != null) {
      try {
        final decoded = jsonDecode(data);
        if (decoded is List && decoded.isNotEmpty) {
          final first = decoded.removeAt(0) as Map<String, dynamic>;
          if (decoded.isEmpty) {
            await box.delete(pendingSyncKey);
          } else {
            await box.put(pendingSyncKey, jsonEncode(decoded));
          }
          return first;
        } else if (decoded is Map<String, dynamic>) {
          await box.delete(pendingSyncKey);
          return decoded;
        }
      } catch (_) {
        await box.delete(pendingSyncKey);
      }
    }
    return null;
  }
}

