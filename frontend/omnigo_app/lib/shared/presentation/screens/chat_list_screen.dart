import 'dart:async';

import 'package:flutter/material.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../services/chat_service.dart';
import 'chat_room_screen.dart';

/// ChatListScreen shows every active chat thread for the current user.
/// Used as the bottom-nav chat destination in customer, vendor, and rider
/// apps. The screen is shared across all three apps — the only
/// differentiator is the role label that surfaces in the chat room
/// header.
class ChatListScreen extends StatefulWidget {
  const ChatListScreen({super.key});

  @override
  State<ChatListScreen> createState() => _ChatListScreenState();
}

class _ChatListScreenState extends State<ChatListScreen> {
  List<ChatConversation> _conversations = [];
  bool _isLoading = false;
  String? _myUserId;
  StreamSubscription<List<ChatConversation>>? _convSub;
  StreamSubscription<ChatMessage>? _msgSub;
  Timer? _pollTimer;

  @override
  void initState() {
    super.initState();
    _bootstrap();
    // Repaint whenever the chat service publishes a new list.
    _convSub = ChatService.instance.conversations.listen((list) {
      if (!mounted) return;
      setState(() => _conversations = list);
    });
    // When a new message arrives via WS, refresh the list so the most
    // recent message + ordering stays current.
    _msgSub = ChatService.instance.incoming.listen((_) {
      _fetchConversations();
    });
    // Poll every 15s so the badge stays fresh even if WS is flaky.
    _pollTimer = Timer.periodic(const Duration(seconds: 15), (_) {
      if (mounted) _fetchConversations();
    });
  }

  @override
  void dispose() {
    _convSub?.cancel();
    _msgSub?.cancel();
    _pollTimer?.cancel();
    super.dispose();
  }

  Future<void> _bootstrap() async {
    final prefs = await SharedPreferences.getInstance();
    _myUserId = prefs.getString('tracking_id');
    if (_myUserId != null) {
      await ChatService.instance.setUserId(_myUserId!);
    }
    await _fetchConversations();
  }

  Future<void> _fetchConversations() async {
    if (!mounted) return;
    setState(() => _isLoading = true);
    try {
      await ChatService.instance.fetchConversations();
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Failed to load chats: $e')),
        );
      }
    } finally {
      if (mounted) setState(() => _isLoading = false);
    }
  }

  Future<void> _openConversation(ChatConversation conv) async {
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => ChatRoomScreen(
          orderId: conv.orderId,
          otherUserId: conv.otherUserId,
          otherUserName: conv.otherUserName.isNotEmpty
              ? conv.otherUserName
              : conv.otherUserId,
          otherUserRole: conv.otherUserRole,
        ),
      ),
    );
    // After returning from the room, refresh list (read-receipts may
    // have cleared the unread badge).
    unawaited(_fetchConversations());
  }

  Future<void> _startNewChat() async {
    final otherUserId = await _promptText(
      label: 'Other party tracking ID',
      hint: 'CUST-..., VEND-..., RIDR-...',
    );
    if (otherUserId == null || otherUserId.isEmpty) return;

    final orderId = await _promptText(
      label: 'Order tracking ID',
      hint: 'ORD-...',
    );
    if (orderId == null || orderId.isEmpty) return;

    if (!mounted) return;
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => ChatRoomScreen(
          orderId: orderId,
          otherUserId: otherUserId,
          otherUserName: otherUserId,
          otherUserRole: 'unknown',
        ),
      ),
    );
    unawaited(_fetchConversations());
  }

  Future<String?> _promptText({required String label, required String hint}) async {
    final controller = TextEditingController();
    return showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: Text(label),
        content: TextField(
          controller: controller,
          decoration: InputDecoration(hintText: hint),
          autofocus: true,
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('Cancel')),
          FilledButton(
            onPressed: () => Navigator.pop(ctx, controller.text.trim()),
            child: const Text('Open'),
          ),
        ],
      ),
    );
  }

  String _formatTime(DateTime t) {
    final now = DateTime.now();
    final diff = now.difference(t);
    if (diff.inSeconds < 60) return 'now';
    if (diff.inMinutes < 60) return '${diff.inMinutes}m';
    if (diff.inHours < 24) return '${diff.inHours}h';
    if (diff.inDays < 7) return '${diff.inDays}d';
    return '${t.year}-${t.month.toString().padLeft(2, '0')}-${t.day.toString().padLeft(2, '0')}';
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.white,
      appBar: AppBar(
        title: const Text('Chats', style: TextStyle(fontWeight: FontWeight.bold)),
        backgroundColor: Colors.white,
        elevation: 0,
        foregroundColor: Colors.black,
        actions: [
          IconButton(
            icon: const Icon(Icons.refresh),
            onPressed: _fetchConversations,
          ),
        ],
      ),
      floatingActionButton: FloatingActionButton.extended(
        onPressed: _startNewChat,
        backgroundColor: Colors.black,
        foregroundColor: const Color(0xFFCAFF33),
        icon: const Icon(Icons.chat_bubble_outline),
        label: const Text('New Chat'),
      ),
      body: _isLoading && _conversations.isEmpty
          ? const Center(child: CircularProgressIndicator(color: Color(0xFFCAFF33)))
          : _conversations.isEmpty
              ? _buildEmpty()
              : RefreshIndicator(
                  color: const Color(0xFFCAFF33),
                  onRefresh: _fetchConversations,
                  child: ListView.separated(
                    itemCount: _conversations.length,
                    separatorBuilder: (_, __) => const Divider(height: 1),
                    itemBuilder: (_, i) => _buildRow(_conversations[i]),
                  ),
                ),
    );
  }

  Widget _buildEmpty() {
    return Center(
      child: Column(
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          Icon(Icons.chat_bubble_outline, size: 64, color: Colors.grey.shade400),
          const SizedBox(height: 16),
          const Text('No chats yet', style: TextStyle(fontSize: 18, color: Colors.grey)),
          const SizedBox(height: 8),
          const Padding(
            padding: EdgeInsets.symmetric(horizontal: 32),
            child: Text(
              'When you place an order or accept a gig, your customer / vendor / rider will show up here.',
              textAlign: TextAlign.center,
              style: TextStyle(color: Colors.grey),
            ),
          ),
        ],
      ),
    );
  }

  Widget _buildRow(ChatConversation conv) {
    final hasUnread = conv.unreadCount > 0;
    return ListTile(
      onTap: () => _openConversation(conv),
      leading: CircleAvatar(
        backgroundColor: hasUnread
            ? const Color(0xFFCAFF33)
            : Colors.grey.shade200,
        child: Icon(
          _iconForRole(conv.otherUserRole),
          color: hasUnread ? Colors.black : Colors.grey.shade600,
        ),
      ),
      title: Text(
        conv.otherUserName.isNotEmpty ? conv.otherUserName : conv.otherUserId,
        style: TextStyle(
          fontWeight: hasUnread ? FontWeight.bold : FontWeight.w500,
        ),
      ),
      subtitle: Text(
        conv.lastMessage.isEmpty ? '(no messages yet)' : conv.lastMessage,
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
        style: TextStyle(
          color: hasUnread ? Colors.black87 : Colors.grey.shade600,
          fontWeight: hasUnread ? FontWeight.w500 : FontWeight.normal,
        ),
      ),
      trailing: Column(
        crossAxisAlignment: CrossAxisAlignment.end,
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          Text(
            _formatTime(conv.lastMessageAt),
            style: TextStyle(
              fontSize: 11,
              color: hasUnread ? Colors.black : Colors.grey.shade500,
            ),
          ),
          const SizedBox(height: 4),
          if (hasUnread)
            Container(
              padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
              decoration: const BoxDecoration(
                color: Color(0xFFCAFF33),
                shape: BoxShape.rectangle,
                borderRadius: BorderRadius.all(Radius.circular(10)),
              ),
              child: Text(
                '${conv.unreadCount}',
                style: const TextStyle(
                  color: Colors.black,
                  fontSize: 11,
                  fontWeight: FontWeight.bold,
                ),
              ),
            ),
        ],
      ),
    );
  }

  IconData _iconForRole(String role) {
    switch (role) {
      case 'vendor':
        return Icons.store_mall_directory_rounded;
      case 'rider':
        return Icons.two_wheeler_rounded;
      case 'customer':
        return Icons.person_outline;
      default:
        return Icons.person_outline;
    }
  }
}
