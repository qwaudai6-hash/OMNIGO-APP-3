package handlers

import (
	"net/http"
	"strconv"

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

func (h *ChatHandler) SendMessage(c *gin.Context) {
	// Extract SenderID from JWT token middleware.
	senderID := c.GetString("user_id")
	if senderID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request format"})
		return
	}

	msg, err := h.chatSvc.SendMessage(c.Request.Context(), req.OrderID, senderID, req.ReceiverID, req.Content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send message"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "sent",
		"data":    msg,
	})
}

func (h *ChatHandler) GetHistory(c *gin.Context) {
	orderID := c.Query("order_id")
	if orderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order_id query param is required"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))

	messages, err := h.chatSvc.GetChatHistory(c.Request.Context(), orderID, page)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get chat history"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": messages,
		"page": page,
	})
}

func (h *ChatHandler) MarkRead(c *gin.Context) {
	orderID := c.Param("orderId")
	receiverID := c.GetString("user_id")
	if receiverID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	if err := h.chatSvc.MarkMessagesRead(c.Request.Context(), orderID, receiverID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark as read"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "marked read"})
}

// ListConversations returns the chat list for the calling user — every
// distinct order thread with the most recent message preview, the other
// party's tracking id + name + role, and the unread count.
func (h *ChatHandler) ListConversations(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	convs, err := h.chatSvc.ListConversations(c.Request.Context(), userID, page)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list conversations"})
		return
	}

	unread, _ := h.chatSvc.CountUnread(c.Request.Context(), userID)
	c.JSON(http.StatusOK, gin.H{
		"data":         convs,
		"page":         page,
		"unread_total": unread,
	})
}

// UnreadCount returns just the unread badge value. Cheap endpoint so the
// bottom-nav widget can poll this every 15s without re-fetching the
// whole conversation list.
func (h *ChatHandler) UnreadCount(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	n, err := h.chatSvc.CountUnread(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count unread"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"unread_count": n})
}
