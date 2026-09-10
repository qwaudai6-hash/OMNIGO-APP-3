import 'dart:async';
import 'package:flutter/material.dart';
import 'package:audioplayers/audioplayers.dart';

/// NotificationAlertDialog for return pickup gigs.
/// Shows return-specific details: order ID, return reason, store address.
class ReturnGigAlertDialog extends StatefulWidget {
  final Map<String, dynamic> gigData;
  final VoidCallback onAccept;
  final VoidCallback onDecline;

  const ReturnGigAlertDialog({
    super.key,
    required this.gigData,
    required this.onAccept,
    required this.onDecline,
  });

  @override
  State<ReturnGigAlertDialog> createState() => _ReturnGigAlertDialogState();
}

class _ReturnGigAlertDialogState extends State<ReturnGigAlertDialog> {
  int _secondsLeft = 15;
  Timer? _timer;
  final AudioPlayer _audioPlayer = AudioPlayer();

  @override
  void initState() {
    super.initState();
    _startTimer();
    _playSound();
  }

  void _startTimer() {
    _timer = Timer.periodic(const Duration(seconds: 1), (timer) {
      if (!mounted) {
        timer.cancel();
        return;
      }
      if (_secondsLeft > 0) {
        setState(() => _secondsLeft--);
      } else {
        _timer?.cancel();
        widget.onDecline();
      }
    });
  }

  Future<void> _playSound() async {
    try {
      await _audioPlayer.setReleaseMode(ReleaseMode.loop);
      await _audioPlayer.play(AssetSource('sounds/ringtone.mp3'));
    } catch (e) {
      debugPrint('Audio playback error: $e');
    }
  }

  @override
  void dispose() {
    _timer?.cancel();
    _audioPlayer.stop();
    _audioPlayer.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final data = widget.gigData;
    final earning = data['rider_earning'] ?? data['return_fee_paisa'] ?? 0;
    final reason = data['reason'] ?? '';
    final customerName = data['customer_name'] ?? '';
    final customerAddress = data['customer_address'] ?? '';
    final storeAddress = data['store_address'] ?? '';

    return Dialog(
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(20)),
      elevation: 10,
      backgroundColor: Colors.white,
      child: Padding(
        padding: const EdgeInsets.all(20.0),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.assignment_return, size: 60, color: Colors.orange),
            const SizedBox(height: 15),
            const Text(
              "Return Pickup Request!",
              style: TextStyle(fontSize: 22, fontWeight: FontWeight.bold),
              textAlign: TextAlign.center,
            ),
            const SizedBox(height: 10),
            Text(
              "Earning: PKR $earning",
              style: const TextStyle(fontSize: 18, color: Colors.green, fontWeight: FontWeight.w600),
            ),
            if (reason.toString().isNotEmpty) ...[
              const SizedBox(height: 10),
              Container(
                padding: const EdgeInsets.all(10),
                decoration: BoxDecoration(
                  color: Colors.orange.shade50,
                  borderRadius: BorderRadius.circular(10),
                  border: Border.all(color: Colors.orange.shade100),
                ),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(
                      children: [
                        Icon(Icons.info_outline, size: 16, color: Colors.orange.shade700),
                        const SizedBox(width: 6),
                        Text('Return Reason', style: TextStyle(fontSize: 12, fontWeight: FontWeight.w600, color: Colors.orange.shade700)),
                      ],
                    ),
                    const SizedBox(height: 4),
                    Text(
                      reason.toString(),
                      style: TextStyle(fontSize: 13, color: Colors.orange.shade900),
                      maxLines: 3,
                      overflow: TextOverflow.ellipsis,
                    ),
                  ],
                ),
              ),
            ],
            if (customerName.toString().isNotEmpty) ...[
              const SizedBox(height: 10),
              Row(
                children: [
                  Icon(Icons.person_outline, size: 16, color: Colors.grey.shade600),
                  const SizedBox(width: 6),
                  Expanded(
                    child: Text(
                      'Pickup from: $customerName',
                      style: TextStyle(fontSize: 13, color: Colors.grey.shade700, fontWeight: FontWeight.w500),
                    ),
                  ),
                ],
              ),
            ],
            if (customerAddress.toString().isNotEmpty) ...[
              const SizedBox(height: 6),
              Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Icon(Icons.location_on_outlined, size: 16, color: Colors.grey.shade600),
                  const SizedBox(width: 6),
                  Expanded(
                    child: Text(
                      customerAddress.toString(),
                      style: TextStyle(fontSize: 12, color: Colors.grey.shade600),
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
                ],
              ),
            ],
            if (storeAddress.toString().isNotEmpty) ...[
              const SizedBox(height: 8),
              Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Icon(Icons.store_outlined, size: 16, color: Colors.blue.shade600),
                  const SizedBox(width: 6),
                  Expanded(
                    child: Text(
                      'Deliver to: $storeAddress',
                      style: TextStyle(fontSize: 12, color: Colors.blue.shade600),
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
                ],
              ),
            ],
            const SizedBox(height: 20),
            Text(
              "00:${_secondsLeft.toString().padLeft(2, '0')}",
              style: const TextStyle(fontSize: 36, fontWeight: FontWeight.bold, color: Colors.redAccent),
            ),
            const SizedBox(height: 30),
            Row(
              mainAxisAlignment: MainAxisAlignment.spaceEvenly,
              children: [
                OutlinedButton(
                  onPressed: () {
                    _timer?.cancel();
                    _audioPlayer.stop();
                    widget.onDecline();
                  },
                  style: OutlinedButton.styleFrom(
                    foregroundColor: Colors.grey,
                    side: const BorderSide(color: Colors.grey),
                    padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 12),
                  ),
                  child: const Text("Decline", style: TextStyle(fontSize: 16)),
                ),
                ElevatedButton(
                  onPressed: () {
                    _timer?.cancel();
                    _audioPlayer.stop();
                    widget.onAccept();
                  },
                  style: ElevatedButton.styleFrom(
                    backgroundColor: Colors.orange,
                    foregroundColor: Colors.white,
                    padding: const EdgeInsets.symmetric(horizontal: 30, vertical: 12),
                  ),
                  child: const Text("ACCEPT", style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold)),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}
