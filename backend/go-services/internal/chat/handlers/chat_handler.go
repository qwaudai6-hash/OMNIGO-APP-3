package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/chat/service"
)

type ChatHandler struct {
	chatSvc service.ChatService
}

func NewChatHandler(chatSvc service.ChatService) *ChatHandler {
	return &ChatHandler{chatSvc: chatSvc}
}

type SendMessageRequest struct {
	OrderID    string `json:"order_id" binding:"required"`
	ReceiverID string `json:"receiver_id" binding:"required"`
	Content    string `json:"content" binding:"required"`
}

// getSenderID extracts the authenticated user's tracking_id from the JWT
// context. The middleware stores it under "tracking_id" (not "user_id").
func getSenderID(c *gin.Context) string {
	return c.GetString("tracking_id")
}

func (h *ChatHandler) SendMessage(c *gin.Context) {
	senderID := getSenderID(c)
	if senderID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing tracking_id"})
		return
	}

	var req SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: order_id, receiver_id, and content are required"})
		return
	}

	// Basic content sanitization
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message content cannot be empty"})
		return
	}

	// Prevent self-messaging
	if senderID == req.ReceiverID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot send message to yourself"})
		return
	}

	msg, err := h.chatSvc.SendMessage(c.Request.Context(), req.OrderID, senderID, req.ReceiverID, req.Content)
	if err != nil {
		errMsg := err.Error()
		switch {
		case strings.Contains(errMsg, "does not exist"):
			c.JSON(http.StatusNotFound, gin.H{"error": errMsg})
		case strings.Contains(errMsg, "not a party"):
			c.JSON(http.StatusForbidden, gin.H{"error": "you are not a participant in this order"})
		case strings.Contains(errMsg, "receiver is not a party"):
			c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send message"})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "sent",
		"data":    msg,
	})
}

func (h *ChatHandler) GetHistory(c *gin.Context) {
	senderID := getSenderID(c)
	if senderID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing tracking_id"})
		return
	}

	orderID := c.Query("order_id")
	if orderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order_id query param is required"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit < 1 || limit > 100 {
		limit = 50
	}

	messages, err := h.chatSvc.GetChatHistory(c.Request.Context(), orderID, senderID, page, limit)
	if err != nil {
		errMsg := err.Error()
		switch {
		case strings.Contains(errMsg, "does not exist"):
			c.JSON(http.StatusNotFound, gin.H{"error": errMsg})
		case strings.Contains(errMsg, "not a party"):
			c.JSON(http.StatusForbidden, gin.H{"error": "you are not a participant in this order"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get chat history"})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":  messages,
		"page":  page,
		"limit": limit,
	})
}

func (h *ChatHandler) MarkRead(c *gin.Context) {
	orderID := c.Param("orderId")
	receiverID := getSenderID(c)
	if receiverID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing tracking_id"})
		return
	}

	if err := h.chatSvc.MarkMessagesRead(c.Request.Context(), orderID, receiverID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark messages as read"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "marked read"})
}

// MarkDelivered marks messages as delivered (device ACK).
func (h *ChatHandler) MarkDelivered(c *gin.Context) {
	orderID := c.Param("orderId")
	receiverID := getSenderID(c)
	if receiverID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing tracking_id"})
		return
	}

	if err := h.chatSvc.MarkMessagesDelivered(c.Request.Context(), orderID, receiverID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark messages as delivered"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "marked delivered"})
}

// ListConversations returns the chat list for the calling user — every
// distinct order thread with the most recent message preview, the other
// party's tracking id + name + role, and the unread count.
func (h *ChatHandler) ListConversations(c *gin.Context) {
	userID := getSenderID(c)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing tracking_id"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
	if limit < 1 || limit > 100 {
		limit = 30
	}

	convs, err := h.chatSvc.ListConversations(c.Request.Context(), userID, page, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list conversations"})
		return
	}

	unread, _ := h.chatSvc.CountUnread(c.Request.Context(), userID)
	c.JSON(http.StatusOK, gin.H{
		"data":         convs,
		"page":         page,
		"limit":        limit,
		"unread_total": unread,
	})
}

// UnreadCount returns just the unread badge value. Cheap endpoint so the
// bottom-nav widget can poll this every 15s without re-fetching the
// whole conversation list.
func (h *ChatHandler) UnreadCount(c *gin.Context) {
	userID := getSenderID(c)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing tracking_id"})
		return
	}
	n, err := h.chatSvc.CountUnread(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count unread messages"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"unread_count": n})
}
