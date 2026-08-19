import 'dart:convert';
import 'package:hive_flutter/hive_flutter.dart';

class OfflineGigStorage {
  static const String boxName = 'active_gig_box';
  static const String gigKey = 'current_gig';
  static const String pendingSyncKey = 'pending_status_sync';

  static Future<void> init() async {
    await Hive.initFlutter();
    await Hive.openBox<String>(boxName);
  }

  // Save the currently active gig data
  static Future<void> saveActiveGig(Map<String, dynamic> gigData) async {
    final box = Hive.box<String>(boxName);
    await box.put(gigKey, jsonEncode(gigData));
  }

  // Get the active gig data if it exists
  static Map<String, dynamic>? getActiveGig() {
    final box = Hive.box<String>(boxName);
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
    final box = Hive.box<String>(boxName);
    await box.delete(gigKey);
    await box.delete(pendingSyncKey);
  }

  // Save a pending status sync request to retry when online
  static Future<void> queuePendingStatusSync(Map<String, dynamic> payload) async {
    final box = Hive.box<String>(boxName);
    await box.put(pendingSyncKey, jsonEncode(payload));
  }

  // Get and clear pending sync request
  static Future<Map<String, dynamic>?> consumePendingStatusSync() async {
    final box = Hive.box<String>(boxName);
    final data = box.get(pendingSyncKey);
    if (data != null) {
      await box.delete(pendingSyncKey);
      return jsonDecode(data) as Map<String, dynamic>;
    }
    return null;
  }
}

