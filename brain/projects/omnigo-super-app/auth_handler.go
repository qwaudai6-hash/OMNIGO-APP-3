package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/auth/service"
)

type AuthHandler struct {
	svc *service.AuthService
}

func NewAuthHandler(svc *service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// Register HTTP handler for POST /auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	var req service.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Normalize role to lowercase
	req.Role = strings.ToLower(req.Role)

	trackingID, err := h.svc.Register(c.Request.Context(), req)
	if err != nil {
		if strings.Contains(err.Error(), "CONFLICT_DUPLICATE_EMAIL") {
			c.JSON(http.StatusConflict, gin.H{"error": "This email address is already in use"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":     "Account registered successfully",
		"tracking_id": trackingID,
	})
}

// Login HTTP handler for POST /auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	var req service.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	resp, err := h.svc.Login(c.Request.Context(), req)
	if err != nil {
		if strings.Contains(err.Error(), "UNAUTHORIZED_BAD_CREDENTIALS") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid email address or password"})
			return
		}
		if strings.Contains(err.Error(), "FORBIDDEN_PENDING_VERIFICATION") {
			c.JSON(http.StatusForbidden, gin.H{"error": "Your account is pending verification by admin"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// RegisterRoutes attaches auth handlers to the gin router
func (h *AuthHandler) RegisterRoutes(router *gin.Engine) {
	auth := router.Group("/api/v1/auth")
	{
		auth.POST("/register", h.Register)
		auth.POST("/login", h.Login)
	}
}
