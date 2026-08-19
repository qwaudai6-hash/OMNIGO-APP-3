import 'dart:async';

import 'package:flutter/material.dart';

import '../services/chat_service.dart';

/// ChatRoomScreen is the per-order chat thread. It surfaces the order
/// tracking id at the top so both parties always know which order's
/// handover/dispute this chat is about, and polls for new messages
/// every 5s while the screen is open (the WS gateway also pushes
/// updates but polling is a belt-and-suspenders fallback).
class ChatRoomScreen extends StatefulWidget {
  const ChatRoomScreen({
    super.key,
    required this.orderId,
    required this.otherUserId,
    required this.otherUserName,
    required this.otherUserRole,
  });

  final String orderId;
  final String otherUserId;
  final String otherUserName;
  final String otherUserRole;

  @override
  State<ChatRoomScreen> createState() => _ChatRoomScreenState();
}

class _ChatRoomScreenState extends State<ChatRoomScreen> {
  final List<ChatMessage> _messages = [];
  final TextEditingController _inputController = TextEditingController();
  final ScrollController _scrollController = ScrollController();
  bool _isSending = false;
  bool _isLoading = false;
  StreamSubscription<ChatMessage>? _msgSub;
  Timer? _pollTimer;

  @override
  void initState() {
    super.initState();
    _fetchHistory();
    _markRead();
    // Subscribe to incoming WS messages — only repaint if it's our
    // thread.
    _msgSub = ChatService.instance.incoming.listen((msg) {
      if (msg.orderId != widget.orderId) return;
      if (!mounted) return;
      setState(() {
        // De-duplicate: the backend echoes our outbound message back
        // over WS, so ignore the echo if it's already in the list.
        if (!_messages.any((m) => m.id == msg.id)) {
          _messages.add(msg);
        }
      });
      _scrollToBottom();
      _markRead();
    });
    _pollTimer = Timer.periodic(const Duration(seconds: 5), (_) {
      if (mounted) _fetchHistory();
    });
  }

  @override
  void dispose() {
    _msgSub?.cancel();
    _pollTimer?.cancel();
    _inputController.dispose();
    _scrollController.dispose();
    super.dispose();
  }

  Future<void> _fetchHistory() async {
    if (_isLoading) return;
    setState(() => _isLoading = true);
    try {
      final list = await ChatService.instance.fetchMessages(widget.orderId);
      if (!mounted) return;
      setState(() {
        _messages
          ..clear()
          ..addAll(list);
      });
      _scrollToBottom();
    } catch (_) {
      // Silent — periodic poll will retry.
    } finally {
      if (mounted) setState(() => _isLoading = false);
    }
  }

  Future<void> _markRead() async {
    try {
      await ChatService.instance.markRead(widget.orderId);
    } catch (_) {}
  }

  Future<void> _send() async {
    final text = _inputController.text.trim();
    if (text.isEmpty || _isSending) return;
    setState(() => _isSending = true);
    try {
      final msg = await ChatService.instance.sendMessage(
        orderId: widget.orderId,
        receiverId: widget.otherUserId,
        content: text,
      );
      _inputController.clear();
      if (!mounted) return;
      setState(() {
        // Add locally so the echo from WS (which may take 50-200ms) doesn't
        // show an empty bubble for a second.
        if (!_messages.any((m) => m.id == msg.id)) {
          _messages.add(msg);
        }
      });
      _scrollToBottom();
    } catch (e) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Send failed: $e')),
      );
    } finally {
      if (mounted) setState(() => _isSending = false);
    }
  }

  void _scrollToBottom() {
    if (!_scrollController.hasClients) return;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!_scrollController.hasClients) return;
      _scrollController.animateTo(
        _scrollController.position.maxScrollExtent,
        duration: const Duration(milliseconds: 200),
        curve: Curves.easeOut,
      );
    });
  }

  String _formatTime(DateTime t) {
    final hh = t.hour.toString().padLeft(2, '0');
    final mm = t.minute.toString().padLeft(2, '0');
    return '$hh:$mm';
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
        return Icons.help_outline;
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.white,
      appBar: AppBar(
        backgroundColor: Colors.white,
        elevation: 0,
        foregroundColor: Colors.black,
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          onPressed: () => Navigator.of(context).pop(),
        ),
        title: Row(
          children: [
            CircleAvatar(
              radius: 18,
              backgroundColor: const Color(0xFFCAFF33),
              child: Icon(_iconForRole(widget.otherUserRole), size: 18, color: Colors.black),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    widget.otherUserName,
                    style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 16),
                    overflow: TextOverflow.ellipsis,
                  ),
                  Text(
                    'Order #${widget.orderId}',
                    style: const TextStyle(fontSize: 11, color: Colors.grey),
                    overflow: TextOverflow.ellipsis,
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
      body: SafeArea(
        child: Column(
          children: [
            Expanded(
              child: _messages.isEmpty && !_isLoading
                  ? Center(
                      child: Column(
                        mainAxisAlignment: MainAxisAlignment.center,
                        children: [
                          Icon(Icons.chat_bubble_outline,
                              size: 56, color: Colors.grey.shade400),
                          const SizedBox(height: 12),
                          const Text(
                            'No messages yet. Say hello!',
                            style: TextStyle(color: Colors.grey),
                          ),
                        ],
                      ),
                    )
                  : ListView.builder(
                      controller: _scrollController,
                      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
                      itemCount: _messages.length,
                      itemBuilder: (_, i) => _buildBubble(_messages[i]),
                    ),
            ),
            _buildComposer(),
          ],
        ),
      ),
    );
  }

  Widget _buildBubble(ChatMessage m) {
    final isMine = m.isMine(ChatService.instance.myUserId);
    return Align(
      alignment: isMine ? Alignment.centerRight : Alignment.centerLeft,
      child: Container(
        margin: const EdgeInsets.symmetric(vertical: 4),
        padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 10),
        constraints: BoxConstraints(
          maxWidth: MediaQuery.of(context).size.width * 0.75,
        ),
        decoration: BoxDecoration(
          color: isMine ? const Color(0xFFCAFF33) : Colors.grey.shade100,
          borderRadius: BorderRadius.only(
            topLeft: const Radius.circular(16),
            topRight: const Radius.circular(16),
            bottomLeft: isMine ? const Radius.circular(16) : const Radius.circular(2),
            bottomRight: isMine ? const Radius.circular(2) : const Radius.circular(16),
          ),
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              m.content,
              style: TextStyle(
                color: isMine ? Colors.black : Colors.black87,
                fontSize: 15,
              ),
            ),
            const SizedBox(height: 4),
            Text(
              _formatTime(m.createdAt),
              style: TextStyle(
                fontSize: 10,
                color: isMine ? Colors.black54 : Colors.grey.shade600,
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _buildComposer() {
    return Container(
      decoration: BoxDecoration(
        color: Colors.white,
        border: Border(top: BorderSide(color: Colors.grey.shade300)),
      ),
      padding: EdgeInsets.only(
        left: 8,
        right: 8,
        top: 8,
        bottom: MediaQuery.of(context).viewInsets.bottom + 8,
      ),
      child: SafeArea(
        top: false,
        child: Row(
          children: [
            Expanded(
              child: TextField(
                controller: _inputController,
                textInputAction: TextInputAction.send,
                onSubmitted: (_) => _send(),
                decoration: InputDecoration(
                  hintText: 'Message...',
                  filled: true,
                  fillColor: Colors.grey.shade100,
                  contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
                  border: OutlineInputBorder(
                    borderRadius: BorderRadius.circular(20),
                    borderSide: BorderSide.none,
                  ),
                ),
              ),
            ),
            const SizedBox(width: 8),
            Material(
              color: _isSending ? Colors.grey.shade300 : Colors.black,
              shape: const CircleBorder(),
              child: InkWell(
                customBorder: const CircleBorder(),
                onTap: _isSending ? null : _send,
                child: Padding(
                  padding: const EdgeInsets.all(12),
                  child: _isSending
                      ? const SizedBox(
                          width: 20,
                          height: 20,
                          child: CircularProgressIndicator(
                            strokeWidth: 2,
                            color: Colors.black,
                          ),
                        )
                      : const Icon(Icons.send, color: Color(0xFFCAFF33)),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}
