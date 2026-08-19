import 'dart:async';

import 'package:flutter/material.dart';

import '../services/chat_service.dart';
import '../screens/chat_list_screen.dart';

/// ChatNavButton is a reusable chat icon with an unread-count badge that
/// polls every 15s. Drop it into any app bar, FAB, or bottom-nav slot
/// and it will:
///
///  1. Open the chat list screen when tapped.
///  2. Show a red badge with the unread count when > 0.
///  3. Poll the unread count endpoint in the background so the badge
///     stays fresh even when the WebSocket is offline.
class ChatNavButton extends StatefulWidget {
  const ChatNavButton({super.key, this.color = Colors.black, this.iconColor = Colors.white});

  final Color color;
  final Color iconColor;

  @override
  State<ChatNavButton> createState() => _ChatNavButtonState();
}

class _ChatNavButtonState extends State<ChatNavButton> {
  int _unread = 0;
  StreamSubscription<int>? _streamSub;
  Timer? _pollTimer;

  @override
  void initState() {
    super.initState();
    _streamSub = ChatService.instance.unreadCount.listen((n) {
      if (!mounted) return;
      setState(() => _unread = n);
    });
    _pollTimer = Timer.periodic(const Duration(seconds: 15), (_) {
      if (mounted) ChatService.instance.fetchUnreadCount();
    });
    // initial fetch — best-effort, ignore errors
    unawaited(ChatService.instance.fetchUnreadCount());
  }

  @override
  void dispose() {
    _streamSub?.cancel();
    _pollTimer?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Stack(
      clipBehavior: Clip.none,
      children: [
        IconButton(
          icon: Icon(Icons.chat_bubble_outline, color: widget.iconColor),
          onPressed: () async {
            await Navigator.of(context).push(
              MaterialPageRoute<void>(builder: (_) => const ChatListScreen()),
            );
            // Refresh on return — the user may have replied to threads.
            if (mounted) {
              unawaited(ChatService.instance.fetchUnreadCount());
            }
          },
        ),
        if (_unread > 0)
          Positioned(
            right: 4,
            top: 4,
            child: Container(
              padding: const EdgeInsets.symmetric(horizontal: 5, vertical: 2),
              decoration: BoxDecoration(
                color: const Color(0xFFCAFF33),
                borderRadius: BorderRadius.circular(10),
                border: Border.all(color: Colors.black, width: 1),
              ),
              constraints: const BoxConstraints(minWidth: 16, minHeight: 16),
              child: Text(
                _unread > 99 ? '99+' : '$_unread',
                style: const TextStyle(
                  color: Colors.black,
                  fontSize: 10,
                  fontWeight: FontWeight.bold,
                ),
                textAlign: TextAlign.center,
              ),
            ),
          ),
      ],
    );
  }
}
