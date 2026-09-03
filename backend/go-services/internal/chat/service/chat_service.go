package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/omnigo/backend/internal/chat/models"
	"github.com/omnigo/backend/internal/chat/repository"
	"github.com/redis/go-redis/v9"
)

type ChatService interface {
	SendMessage(ctx context.Context, orderID, senderID, receiverID, content string) (*models.ChatMessage, error)
	GetChatHistory(ctx context.Context, orderID, senderID string, page, limit int) ([]models.ChatMessage, error)
	MarkMessagesRead(ctx context.Context, orderID, receiverID string) error
	MarkMessagesDelivered(ctx context.Context, orderID, receiverID string) error
	ListConversations(ctx context.Context, userID string, page, limit int) ([]models.ChatConversation, error)
	CountUnread(ctx context.Context, userID string) (int, error)
}

type chatService struct {
	repo  repository.ChatRepository
	redis redis.UniversalClient
}

func NewChatService(repo repository.ChatRepository, rdb redis.UniversalClient) ChatService {
	return &chatService{
		repo:  repo,
		redis: rdb,
	}
}

func (s *chatService) SendMessage(ctx context.Context, orderID, senderID, receiverID, content string) (*models.ChatMessage, error) {
	if content == "" {
		return nil, errors.New("message content cannot be empty")
	}

	msg := &models.ChatMessage{
		ID:         uuid.NewString(),
		OrderID:    orderID,
		SenderID:   senderID,
		ReceiverID: receiverID,
		Content:    content,
		IsRead:     false,
	}

	// 1. Save to Postgres
	if err := s.repo.SaveMessage(ctx, msg); err != nil {
		return nil, err
	}

	// 2. M2: Insert into chat_delivery_outbox for reliable WS delivery
	if err := s.repo.EnqueueForDelivery(ctx, msg.ID, orderID, receiverID); err != nil {
		// Non-fatal: message is saved, WS delivery will still work via pub/sub
	}

	// 3. Publish to Redis for WebSocket Gateway to pick up
	payload, _ := json.Marshal(map[string]interface{}{
		"action":      "CHAT_MESSAGE",
		"message_id":  msg.ID,
		"order_id":    msg.OrderID,
		"sender_id":   msg.SenderID,
		"receiver_id": msg.ReceiverID,
		"content":     msg.Content,
		"created_at":  msg.CreatedAt,
	})

	if s.redis != nil {
		s.redis.Publish(ctx, "chat.broadcast", payload)
	}

	return msg, nil
}

func (s *chatService) GetChatHistory(ctx context.Context, orderID, senderID string, page, limit int) ([]models.ChatMessage, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	offset := (page - 1) * limit
	return s.repo.GetMessagesByOrder(ctx, orderID, senderID, limit, offset)
}

func (s *chatService) MarkMessagesRead(ctx context.Context, orderID, receiverID string) error {
	return s.repo.MarkAsRead(ctx, orderID, receiverID)
}

func (s *chatService) MarkMessagesDelivered(ctx context.Context, orderID, receiverID string) error {
	return s.repo.MarkAsDelivered(ctx, orderID, receiverID)
}

func (s *chatService) ListConversations(ctx context.Context, userID string, page, limit int) ([]models.ChatConversation, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 30
	}
	offset := (page - 1) * limit
	return s.repo.ListConversations(ctx, userID, limit, offset)
}

func (s *chatService) CountUnread(ctx context.Context, userID string) (int, error) {
	return s.repo.CountUnread(ctx, userID)
}
