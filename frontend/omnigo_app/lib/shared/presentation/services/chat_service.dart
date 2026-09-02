import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';

import '../../../core/network/api_endpoints.dart';
import '../../../core/network/websocket_client.dart';

/// A single chat message in an order thread.
class ChatMessage {

  factory ChatMessage.fromJson(Map<String, dynamic> json) {
    return ChatMessage(
      id: (json['id'] ?? json['message_id'] ?? '').toString(),
      orderId: (json['order_id'] ?? '').toString(),
      senderId: (json['sender_id'] ?? '').toString(),
      receiverId: (json['receiver_id'] ?? '').toString(),
      content: (json['content'] ?? '').toString(),
      isRead: (json['is_read'] ?? json['isRead'] ?? false) as bool,
      createdAt: DateTime.tryParse((json['created_at'] ?? '').toString()) ?? DateTime.now(),
      updatedAt: DateTime.tryParse((json['updated_at'] ?? '').toString()),
    );
  }
  const ChatMessage({
    required this.id,
    required this.orderId,
    required this.senderId,
    required this.receiverId,
    required this.content,
    required this.isRead,
    required this.createdAt,
    this.updatedAt,
  });

  final String id;
  final String orderId;
  final String senderId;
  final String receiverId;
  final String content;
  final bool isRead;
  final DateTime createdAt;
  final DateTime? updatedAt;

  bool isMine(String myUserId) => senderId == myUserId;
}

/// A row in the chat list screen.
class ChatConversation {

  factory ChatConversation.fromJson(Map<String, dynamic> json) {
    return ChatConversation(
      orderId: (json['order_id'] ?? '').toString(),
      otherUserId: (json['other_user_id'] ?? '').toString(),
      otherUserRole: (json['other_user_role'] ?? '').toString(),
      otherUserName: (json['other_user_name'] ?? '').toString(),
      lastMessage: (json['last_message'] ?? '').toString(),
      lastMessageAt: DateTime.tryParse((json['last_message_at'] ?? '').toString()) ?? DateTime.now(),
      unreadCount: (json['unread_count'] ?? 0) as int,
      lastSenderIsMe: (json['last_sender_is_me'] ?? false) as bool,
    );
  }
  const ChatConversation({
    required this.orderId,
    required this.otherUserId,
    required this.otherUserRole,
    required this.otherUserName,
    required this.lastMessage,
    required this.lastMessageAt,
    required this.unreadCount,
    required this.lastSenderIsMe,
  });

  final String orderId;
  final String otherUserId;
  final String otherUserRole;
  final String otherUserName;
  final String lastMessage;
  final DateTime lastMessageAt;
  final int unreadCount;
  final bool lastSenderIsMe;
}

/// ChatService is the single source of truth for in-app chat across all
/// three apps (customer, vendor, rider). It talks to:
///   - `/api/v1/chat/conversations` for the chat list
///   - `/api/v1/chat/messages` for the per-thread history
///   - `/api/v1/chat/messages` (POST) for sending
///   - `/api/v1/chat/messages/:orderId/read` for read-receipts
///   - `/api/v1/chat/unread` for the bottom-nav badge
///
/// Outbound messages are published to Redis `chat.broadcast` by the
/// backend, which the WebSocket gateway fans out to both participants in
/// realtime. Inbound chat messages are received via the shared
/// WebSocketClient and re-published on [incoming] for UI to subscribe.
class ChatService {
  ChatService._();
  static final ChatService instance = ChatService._();

  // Streams ----------------------------------------------------------------
  // `conversations` fires when the chat list changes (new message /
  // read-receipt). UI screens subscribe to repaint.
  final StreamController<List<ChatConversation>> _conversationsController =
      StreamController<List<ChatConversation>>.broadcast();
  Stream<List<ChatConversation>> get conversations =>
      _conversationsController.stream;

  // `messages` fires when a new message arrives for a thread. The chat
  // room screen filters by `orderId` to only show its own thread.
  final StreamController<ChatMessage> _messageController =
      StreamController<ChatMessage>.broadcast();
  Stream<ChatMessage> get incoming => _messageController.stream;

  // `unreadCount` fires whenever the unread badge should change.
  final StreamController<int> _unreadController = StreamController<int>.broadcast();
  Stream<int> get unreadCount => _unreadController.stream;

  StreamSubscription<dynamic>? _wsSubscription;
  String? _myUserId;

  // ── Public API ─────────────────────────────────────────────────────────

  /// Bind the chat service to the shared WebSocket so incoming chat
  /// messages are forwarded to UI. Safe to call multiple times.
  Future<void> bindToWebSocket(WebSocketClient ws) async {
    await _wsSubscription?.cancel();
    _wsSubscription = ws.stream.listen((raw) {
      if (raw is! String) return;
      try {
        final frame = jsonDecode(raw) as Map<String, dynamic>;
        if (frame['action'] == 'CHAT_MESSAGE') {
          final msg = ChatMessage.fromJson(frame);
          _messageController.add(msg);
        }
      } catch (_) {
        // ignore non-chat frames
      }
    });
  }

  /// Cancel WebSocket subscription and reset binding state.
  Future<void> unbind() async {
    await _wsSubscription?.cancel();
    _wsSubscription = null;
  }

  /// Dispose of resources, subscriptions, and stream controllers.
  Future<void> dispose() async {
    await unbind();
    await _conversationsController.close();
    await _messageController.close();
    await _unreadController.close();
  }

  /// Cache the JWT-derived user id so [isMine] works without hitting
  /// SharedPreferences on every render.
  Future<void> setUserId(String id) async {
    _myUserId = id;
  }

  String get myUserId => _myUserId ?? '';

  /// Fetch the chat list (conversations) for the current user.
  Future<List<ChatConversation>> fetchConversations({int page = 1}) async {
    final prefs = await SharedPreferences.getInstance();
    final token = prefs.getString('jwt_token') ?? '';
    final resp = await http.get(
      Uri.parse('${ApiEndpoints.chatConversations}?page=$page'),
      headers: {'Authorization': 'Bearer $token'},
    ).timeout(const Duration(seconds: 8));
    if (resp.statusCode != 200) {
      throw Exception('Failed to fetch conversations: ${resp.statusCode}');
    }
    final data = jsonDecode(resp.body) as Map<String, dynamic>;
    final list = (data['data'] as List<dynamic>? ?? const [])
        .map((e) => ChatConversation.fromJson(e as Map<String, dynamic>))
        .toList();
    _conversationsController.add(list);
    final unread = (data['unread_total'] ?? 0) as int;
    _unreadController.add(unread);
    return list;
  }

  /// Fetch the message history for a single order thread.
  Future<List<ChatMessage>> fetchMessages(String orderId, {int page = 1}) async {
    final prefs = await SharedPreferences.getInstance();
    final token = prefs.getString('jwt_token') ?? '';
    final resp = await http.get(
      Uri.parse('${ApiEndpoints.chatMessages}?order_id=$orderId&page=$page'),
      headers: {'Authorization': 'Bearer $token'},
    ).timeout(const Duration(seconds: 8));
    if (resp.statusCode != 200) {
      throw Exception('Failed to fetch messages: ${resp.statusCode}');
    }
    final data = jsonDecode(resp.body) as Map<String, dynamic>;
    final list = (data['data'] as List<dynamic>? ?? const [])
        .map((e) => ChatMessage.fromJson(e as Map<String, dynamic>))
        .toList();
    return list;
  }

  /// Send a chat message over HTTP. The backend publishes to Redis
  /// `chat.broadcast`, which the WebSocket gateway fans out to the
  /// receiver. Loops back to the sender too so the local message is
  /// committed atomically.
  Future<ChatMessage> sendMessage({
    required String orderId,
    required String receiverId,
    required String content,
  }) async {
    final prefs = await SharedPreferences.getInstance();
    final token = prefs.getString('jwt_token') ?? '';
    final resp = await http.post(
      Uri.parse(ApiEndpoints.chatMessages),
      headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer $token',
      },
      body: jsonEncode({
        'order_id': orderId,
        'receiver_id': receiverId,
        'content': content,
      }),
    ).timeout(const Duration(seconds: 8));
    if (resp.statusCode != 200) {
      throw Exception('Failed to send message: ${resp.statusCode}');
    }
    final data = jsonDecode(resp.body) as Map<String, dynamic>;
    return ChatMessage.fromJson(data['data'] as Map<String, dynamic>);
  }

  /// Mark every message in this thread addressed to me as read.
  Future<void> markRead(String orderId) async {
    final prefs = await SharedPreferences.getInstance();
    final token = prefs.getString('jwt_token') ?? '';
    await http.put(
      Uri.parse('${ApiEndpoints.chatMessages}/$orderId/read'),
      headers: {'Authorization': 'Bearer $token'},
    ).timeout(const Duration(seconds: 5));
  }

  /// Cheap poll for the bottom-nav badge. Calls every 15s from the
  /// chat button widget.
  Future<int> fetchUnreadCount() async {
    final prefs = await SharedPreferences.getInstance();
    final token = prefs.getString('jwt_token') ?? '';
    final resp = await http.get(
      Uri.parse(ApiEndpoints.chatUnread),
      headers: {'Authorization': 'Bearer $token'},
    ).timeout(const Duration(seconds: 5));
    if (resp.statusCode != 200) return 0;
    final data = jsonDecode(resp.body) as Map<String, dynamic>;
    final n = (data['unread_count'] ?? 0) as int;
    _unreadController.add(n);
    return n;
  }

  /// Forward a WS-broadcast message to subscribers. Useful when the chat
  /// service is wired to a non-standard WS source.
  void deliverIncoming(ChatMessage m) {
    _messageController.add(m);
  }
}
