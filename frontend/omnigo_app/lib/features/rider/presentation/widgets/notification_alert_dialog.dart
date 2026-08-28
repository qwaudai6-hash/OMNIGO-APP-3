import 'dart:async';
import 'package:flutter/material.dart';
import 'package:audioplayers/audioplayers.dart';

class NotificationAlertDialog extends StatefulWidget {

  const NotificationAlertDialog({
    super.key,
    required this.gigData,
    required this.onAccept,
    required this.onDecline,
  });
  final Map<String, dynamic> gigData;
  final VoidCallback onAccept;
  final VoidCallback onDecline;

  @override
  NotificationAlertDialogState createState() => NotificationAlertDialogState();
}

class NotificationAlertDialogState extends State<NotificationAlertDialog> {
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
      if (_secondsLeft > 0) {
        setState(() {
          _secondsLeft--;
        });
      } else {
        _timer?.cancel();
        widget.onDecline(); // Auto decline/close on timeout
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
    return Dialog(
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(20)),
      elevation: 10,
      backgroundColor: Colors.white,
      child: Padding(
        padding: const EdgeInsets.all(20.0),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.delivery_dining, size: 60, color: Colors.blueAccent),
            const SizedBox(height: 15),
            const Text(
              "New Delivery Request!",
              style: TextStyle(fontSize: 22, fontWeight: FontWeight.bold),
              textAlign: TextAlign.center,
            ),
            const SizedBox(height: 10),
            Text(
              "Earning: PKR ${widget.gigData['rider_earning']}",
              style: const TextStyle(fontSize: 18, color: Colors.green, fontWeight: FontWeight.w600),
            ),
            if (widget.gigData['delivery_fee'] != null) ...[
              const SizedBox(height: 6),
              Text(
                "Delivery Fee: PKR ${widget.gigData['delivery_fee']}",
                style: TextStyle(fontSize: 14, color: Colors.grey.shade600, fontWeight: FontWeight.w500),
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
                    foregroundColor: Colors.grey, side: const BorderSide(color: Colors.grey),
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
                    backgroundColor: Colors.blueAccent,
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
