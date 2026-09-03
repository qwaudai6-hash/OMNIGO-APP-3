package models

import (
	"time"
)

// ChatMessage represents a single text message sent between any two users (e.g. Customer and Rider).
type ChatMessage struct {
	ID          string     `json:"id"`
	OrderID     string     `json:"order_id"`
	SenderID    string     `json:"sender_id"`
	ReceiverID  string     `json:"receiver_id"`
	Content     string     `json:"content"`
	IsRead      bool       `json:"is_read"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	ReadAt      *time.Time `json:"read_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"-"`
}

// ChatThread represents an active thread. We can just use it as a response struct if needed.
type ChatThread struct {
	OrderID      string        `json:"order_id"`
	ParticipantA string        `json:"participant_a"`
	ParticipantB string        `json:"participant_b"`
	Messages     []ChatMessage `json:"messages"`
}

// ChatConversation is a single row in the chat list screen. It surfaces
// the most recent message body, the other participant's tracking id and
// role, and the unread count for the calling user.
type ChatConversation struct {
	OrderID        string    `json:"order_id"`
	OtherUserID    string    `json:"other_user_id"`
	OtherUserRole  string    `json:"other_user_role"`
	OtherUserName  string    `json:"other_user_name"`
	LastMessage    string    `json:"last_message"`
	LastMessageAt  time.Time `json:"last_message_at"`
	UnreadCount    int       `json:"unread_count"`
	LastSenderIsMe bool      `json:"last_sender_is_me"`
}
