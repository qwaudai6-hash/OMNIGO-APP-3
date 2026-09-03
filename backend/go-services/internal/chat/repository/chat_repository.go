package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/chat/models"
	"github.com/omnigo/backend/internal/shared/database"
)

type ChatRepository interface {
	SaveMessage(ctx context.Context, msg *models.ChatMessage) error
	GetMessagesByOrder(ctx context.Context, orderID, senderID string, limit int, offset int) ([]models.ChatMessage, error)
	MarkAsRead(ctx context.Context, orderID string, receiverID string) error
	MarkAsDelivered(ctx context.Context, orderID string, receiverID string) error
	EnqueueForDelivery(ctx context.Context, messageID, orderID, receiverID string) error
	MarkDeliverySuccess(ctx context.Context, messageID string) error
	GetPendingDeliveries(ctx context.Context, limit int) ([]PendingDelivery, error)
	// ListConversations returns the most recent message per order thread
	// for a user, ordered by created_at DESC. Used by the Flutter chat
	// list screen to show all active conversations.
	ListConversations(ctx context.Context, userID string, limit int, offset int) ([]models.ChatConversation, error)
	// CountUnread returns the number of unread messages addressed to the
	// given user. Used to render the bottom-nav badge.
	CountUnread(ctx context.Context, userID string) (int, error)
	// IsPartyToOrder checks if the user is a participant in the order
	// (customer, vendor, or rider).
	IsPartyToOrder(ctx context.Context, orderID, userID string) (bool, error)
}

type PendingDelivery struct {
	MessageID  string
	OrderID    string
	ReceiverID string
}

type chatRepository struct {
	db *pgxpool.Pool
}

func NewChatRepository(db *pgxpool.Pool) ChatRepository {
	return &chatRepository{db: db}
}

func (r *chatRepository) SaveMessage(ctx context.Context, msg *models.ChatMessage) error {
	// Validate order exists
	ok, err := database.Exists(ctx, r.db, "SELECT 1 FROM orders WHERE order_tracking_id = $1", msg.OrderID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("order %s does not exist", msg.OrderID)
	}

	// Validate sender is a party to the order (customer, vendor, or rider)
	party, err := r.IsPartyToOrder(ctx, msg.OrderID, msg.SenderID)
	if err != nil {
		return err
	}
	if !party {
		return fmt.Errorf("user %s is not a party to order %s", msg.SenderID, msg.OrderID)
	}

	// Validate receiver is also a party to the order — prevents spam to non-participants
	if msg.ReceiverID != "" {
		receiverParty, err := r.IsPartyToOrder(ctx, msg.OrderID, msg.ReceiverID)
		if err != nil {
			return err
		}
		if !receiverParty {
			return fmt.Errorf("receiver %s is not a party to order %s", msg.ReceiverID, msg.OrderID)
		}
	}

	query := `
		INSERT INTO chat_messages (id, order_id, sender_id, receiver_id, content, is_read, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
		RETURNING created_at, updated_at
	`
	return r.db.QueryRow(ctx, query, msg.ID, msg.OrderID, msg.SenderID, msg.ReceiverID, msg.Content, msg.IsRead).Scan(&msg.CreatedAt, &msg.UpdatedAt)
}

// IsPartyToOrder checks if the user is a participant in the order.
func (r *chatRepository) IsPartyToOrder(ctx context.Context, orderID, userID string) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1 FROM orders
			WHERE order_tracking_id = $1
			  AND (customer_tracking_id = $2
			       OR vendor_tracking_id = $2
			       OR rider_tracking_id = $2)
		)
	`
	var exists bool
	err := r.db.QueryRow(ctx, query, orderID, userID).Scan(&exists)
	return exists, err
}

func (r *chatRepository) GetMessagesByOrder(ctx context.Context, orderID, senderID string, limit int, offset int) ([]models.ChatMessage, error) {
	// Validate sender is a party to the order
	party, err := r.IsPartyToOrder(ctx, orderID, senderID)
	if err != nil {
		return nil, err
	}
	if !party {
		return nil, fmt.Errorf("user %s is not a party to order %s", senderID, orderID)
	}

	query := `
		SELECT id, order_id, sender_id, receiver_id, content, is_read, delivered_at, read_at, created_at, updated_at
		FROM chat_messages
		WHERE order_id = $1 AND deleted_at IS NULL
		ORDER BY created_at ASC
		LIMIT $2 OFFSET $3
	`
	rows, err := r.db.Query(ctx, query, orderID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []models.ChatMessage
	for rows.Next() {
		var msg models.ChatMessage
		if err := rows.Scan(&msg.ID, &msg.OrderID, &msg.SenderID, &msg.ReceiverID, &msg.Content, &msg.IsRead, &msg.DeliveredAt, &msg.ReadAt, &msg.CreatedAt, &msg.UpdatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

func (r *chatRepository) MarkAsRead(ctx context.Context, orderID string, receiverID string) error {
	query := `
		UPDATE chat_messages
		SET is_read = true, read_at = NOW(), updated_at = NOW()
		WHERE order_id = $1 AND receiver_id = $2 AND is_read = false
	`
	_, err := r.db.Exec(ctx, query, orderID, receiverID)
	return err
}

func (r *chatRepository) MarkAsDelivered(ctx context.Context, orderID string, receiverID string) error {
	query := `
		UPDATE chat_messages
		SET delivered_at = NOW(), updated_at = NOW()
		WHERE order_id = $1 AND receiver_id = $2 AND delivered_at IS NULL
	`
	_, err := r.db.Exec(ctx, query, orderID, receiverID)
	return err
}

func (r *chatRepository) EnqueueForDelivery(ctx context.Context, messageID, orderID, receiverID string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO chat_delivery_outbox (message_id, order_id, receiver_id, status, next_retry_at)
		 VALUES ($1, $2, $3, 'pending', NOW())
		 ON CONFLICT (message_id) DO NOTHING`,
		messageID, orderID, receiverID)
	return err
}

func (r *chatRepository) MarkDeliverySuccess(ctx context.Context, messageID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE chat_delivery_outbox SET status = 'delivered' WHERE message_id = $1`, messageID)
	return err
}

func (r *chatRepository) GetPendingDeliveries(ctx context.Context, limit int) ([]PendingDelivery, error) {
	rows, err := r.db.Query(ctx,
		`SELECT message_id, order_id, receiver_id FROM chat_delivery_outbox
		 WHERE status = 'pending' AND next_retry_at <= NOW() AND attempts < 5
		 ORDER BY created_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PendingDelivery
	for rows.Next() {
		var d PendingDelivery
		if err := rows.Scan(&d.MessageID, &d.OrderID, &d.ReceiverID); err != nil {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// ListConversations returns the most recent message per order thread for
// the user, sorted by last activity. We use a window function (LATERAL
// join) to grab the latest message per order without a per-row subquery,
// keeping it O(N) on the chat_messages table.
func (r *chatRepository) ListConversations(ctx context.Context, userID string, limit int, offset int) ([]models.ChatConversation, error) {
	if limit <= 0 {
		limit = 30
	}
	// First, get distinct order_ids where this user has any message.
	// Then LATERAL-join to fetch the latest message per order. Finally
	// resolve the other participant's name + role from the users table.
	query := `
		WITH my_orders AS (
		  SELECT DISTINCT order_id
		  FROM chat_messages
		  WHERE deleted_at IS NULL
		    AND (sender_id = $1 OR receiver_id = $1)
		),
		latest AS (
		  SELECT m.order_id,
		         m.sender_id,
		         m.receiver_id,
		         m.content,
		         m.created_at
		  FROM my_orders mo
		  CROSS JOIN LATERAL (
		    SELECT sender_id, receiver_id, content, created_at
		    FROM chat_messages
		    WHERE order_id = mo.order_id AND deleted_at IS NULL
		    ORDER BY created_at DESC
		    LIMIT 1
		  ) m
		)
		SELECT l.order_id,
		       CASE WHEN l.sender_id = $1 THEN l.receiver_id ELSE l.sender_id END AS other_user_id,
		       u.role AS other_user_role,
		       u.full_name AS other_user_name,
		       l.content AS last_message,
		       l.created_at AS last_message_at,
		       (SELECT COUNT(*) FROM chat_messages
		         WHERE order_id = l.order_id
		           AND receiver_id = $1
		           AND is_read = false
		           AND deleted_at IS NULL) AS unread_count,
		       (l.sender_id = $1) AS last_sender_is_me
		FROM latest l
		LEFT JOIN users u ON u.tracking_id = CASE WHEN l.sender_id = $1 THEN l.receiver_id ELSE l.sender_id END
		ORDER BY l.created_at DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := r.db.Query(ctx, query, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.ChatConversation
	for rows.Next() {
		var c models.ChatConversation
		if err := rows.Scan(&c.OrderID, &c.OtherUserID, &c.OtherUserRole, &c.OtherUserName,
			&c.LastMessage, &c.LastMessageAt, &c.UnreadCount, &c.LastSenderIsMe); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountUnread returns the total number of unread messages for the user.
func (r *chatRepository) CountUnread(ctx context.Context, userID string) (int, error) {
	var n int
	row := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM chat_messages
		 WHERE receiver_id = $1 AND is_read = false AND deleted_at IS NULL`,
		userID)
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
