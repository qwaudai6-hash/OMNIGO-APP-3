import 'dart:convert';
import 'package:flutter/foundation.dart';
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

  static Future<Box<String>> _getBox() async {
    if (!Hive.isBoxOpen(boxName)) {
      await Hive.openBox<String>(boxName);
    }
    return Hive.box<String>(boxName);
  }

  // Save the currently active gig data
  static Future<void> saveActiveGig(Map<String, dynamic> gigData) async {
    final box = await _getBox();
    await box.put(gigKey, jsonEncode(gigData));
  }

  // Get the active gig data if it exists
  static Map<String, dynamic>? getActiveGig() {
    if (!Hive.isBoxOpen(boxName)) return null;
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
    if (!Hive.isBoxOpen(boxName)) return;
    final box = await _getBox();
    await box.delete(gigKey);
    await box.delete(pendingSyncKey);
  }

  // Save a pending status sync request to retry when online (FIFO Queue)
  static Future<void> queuePendingStatusSync(Map<String, dynamic> payload) async {
    final box = await _getBox();
    List<dynamic> queue = [];
    final raw = box.get(pendingSyncKey);
    if (raw != null) {
      try {
        queue = jsonDecode(raw) as List<dynamic>;
      } catch (e) {
        debugPrint('OfflineGigStorage: corrupted pending sync data, resetting: $e');
        queue = [];
      }
    }
    final item = Map<String, dynamic>.from(payload);
    item['idempotency_key'] ??= '${DateTime.now().millisecondsSinceEpoch}_${payload['status']}';
    item['queued_at'] ??= DateTime.now().toUtc().toIso8601String();
    item['retry_count'] = (item['retry_count'] as int? ?? 0);
    queue.add(item);
    await box.put(pendingSyncKey, jsonEncode(queue));
  }

  // Get and remove the oldest pending sync request (FIFO consume)
  static Future<Map<String, dynamic>?> consumePendingStatusSync() async {
    if (!Hive.isBoxOpen(boxName)) return null;
    final box = await _getBox();
    final data = box.get(pendingSyncKey);
    if (data != null) {
      try {
        final decoded = jsonDecode(data);
        if (decoded is List && decoded.isNotEmpty) {
          final first = decoded.removeAt(0);
          if (decoded.isEmpty) {
            await box.delete(pendingSyncKey);
          } else {
            await box.put(pendingSyncKey, jsonEncode(decoded));
          }
          return first is Map<String, dynamic> ? first : null;
        } else if (decoded is Map<String, dynamic>) {
          await box.delete(pendingSyncKey);
          return decoded;
        }
      } catch (e) {
        debugPrint('OfflineGigStorage: failed to decode pending sync, clearing: $e');
        await box.delete(pendingSyncKey);
      }
    }
    return null;
  }

  // Peek the number of pending sync events waiting in queue
  static Future<int> getPendingSyncCount() async {
    if (!Hive.isBoxOpen(boxName)) return 0;
    final box = await _getBox();
    final data = box.get(pendingSyncKey);
    if (data == null) return 0;
    try {
      final decoded = jsonDecode(data);
      if (decoded is List) return decoded.length;
      if (decoded is Map) return 1;
    } catch (_) {}
    return 0;
  }

  // Clear all pending syncs explicitly
  static Future<void> clearPendingSyncs() async {
    if (!Hive.isBoxOpen(boxName)) return;
    final box = await _getBox();
    await box.delete(pendingSyncKey);
  }
}

