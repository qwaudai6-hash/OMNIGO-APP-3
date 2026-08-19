package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/auth/service"
	"github.com/omnigo/backend/internal/shared/middleware"
)

// AuthFlowHandler exposes the password-reset, email-verification, and
// 2FA endpoints. These are intentionally split out of the main
// AuthHandler so the standard signup/login flow stays clean.
type AuthFlowHandler struct {
	svc *service.AuthService
	// notify is the optional email-delivery hook. If nil, the
	// forgot-password and email-verification endpoints return the
	// generated URL in the response body for dev/testing. In production
	// this is the SMTP-backed email-service.
	notify func(ctx context.Context, to, subject, url string)
}

func NewAuthFlowHandler(svc *service.AuthService) *AuthFlowHandler {
	return &AuthFlowHandler{svc: svc}
}

// WithNotifier injects a real email dispatcher. Called from the
// service main.go so the email-service SMTP client is wired up.
func (h *AuthFlowHandler) WithNotifier(fn func(ctx context.Context, to, subject, url string)) *AuthFlowHandler {
	h.notify = fn
	return h
}

// ─────────────────────────────────────────────────────────────────────
//  Password reset
// ─────────────────────────────────────────────────────────────────────

// ForgotPassword renders a 1-hour token and (when notifier is wired up)
// emails it to the user. Always returns 200 even if the email is
// unknown, so the API doesn't leak which addresses are registered.
type ForgotPasswordRequest struct {
	Email string `json:"email" binding:"required,email"`
}

func (h *AuthFlowHandler) ForgotPassword(c *gin.Context) {
	var req ForgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token, err := h.svc.RequestPasswordReset(c.Request.Context(), req.Email)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	resp := gin.H{"status": "if the email exists, a reset link has been sent"}
	if token != "" && h.notify == nil {
		// Dev mode — return the token in the response so the front-end
		// can deep-link to the reset screen.
		resp["reset_token"] = token
		resp["reset_url"] = "/auth/reset?token=" + token
	}
	if token != "" && h.notify != nil {
		// Production: hand the URL to the email dispatcher. The email
		// body lives in the email-service, not here.
		subject := "Reset your OMNIGO password"
		url := "https://app.omnigo.io/auth/reset?token=" + token
		go h.notify(context.Background(), req.Email, subject, url)
	}
	c.JSON(http.StatusOK, resp)
}

type ResetPasswordRequest struct {
	Token       string `json:"token" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=6"`
}

func (h *AuthFlowHandler) ResetPassword(c *gin.Context) {
	var req ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.ConfirmPasswordReset(c.Request.Context(), req.Token, req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "password reset successful"})
}

// ─────────────────────────────────────────────────────────────────────
//  Email verification
// ─────────────────────────────────────────────────────────────────────

// IssueEmailVerification renders a 24-hour token. Called after the
// user requests a fresh verification email from the front-end.
func (h *AuthFlowHandler) IssueEmailVerification(c *gin.Context) {
	trackingID := c.GetString("user_id")
	if trackingID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	token, err := h.svc.IssueEmailVerification(c.Request.Context(), trackingID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Look up the user's email so we can deliver the link.
	var email string
	_ = h.svc.DB().QueryRow(c.Request.Context(),
		"SELECT email FROM users WHERE tracking_id = $1", trackingID).Scan(&email)

	resp := gin.H{"status": "verification email sent"}
	if token != "" && h.notify == nil {
		resp["verify_token"] = token
	}
	if token != "" && h.notify != nil {
		go h.notify(context.Background(), email,
			"Verify your OMNIGO email",
			"https://app.omnigo.io/auth/verify?token="+token)
	}
	c.JSON(http.StatusOK, resp)
}

// VerifyEmail is the public route the user clicks in their email.
func (h *AuthFlowHandler) VerifyEmail(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token is required"})
		return
	}

	email, err := h.svc.ConfirmEmailVerification(c.Request.Context(), token)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "email verified", "email": email})
}

// ─────────────────────────────────────────────────────────────────────
//  2FA / TOTP
// ─────────────────────────────────────────────────────────────────────

// Enroll2FA generates a new TOTP secret + QR code URL and returns
// them to the user. The user scans the QR code with Google
// Authenticator, then submits a code to Verify2FAEnrollment.
func (h *AuthFlowHandler) Enroll2FA(c *gin.Context) {
	trackingID := c.GetString("user_id")
	if trackingID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	secret, otpauthURL, err := h.svc.Enroll2FA(c.Request.Context(), trackingID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"secret":      secret,
		"otpauth_url": otpauthURL,
	})
}

type Verify2FARequest struct {
	Code string `json:"code" binding:"required,len=6"`
}

func (h *AuthFlowHandler) Verify2FAEnrollment(c *gin.Context) {
	trackingID := c.GetString("user_id")
	if trackingID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req Verify2FARequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.Verify2FAEnrollment(c.Request.Context(), trackingID, req.Code); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "2FA enabled"})
}

func (h *AuthFlowHandler) Disable2FA(c *gin.Context) {
	trackingID := c.GetString("user_id")
	if trackingID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req Verify2FARequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.Disable2FA(c.Request.Context(), trackingID, req.Code); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "2FA disabled"})
}

// ─────────────────────────────────────────────────────────────────────
//  2FA challenge (login-time) — public, no JWT required
// ─────────────────────────────────────────────────────────────────────

// CompleteTwoFactorChallenge is the second leg of the login flow. The
// user already passed password verification at /auth/login and
// received a {requires_2fa: true, challenge_id: "..."} response. They
// enter their TOTP code, we POST it here, and on success we hand back
// the full session (JWT + refresh token).
type TwoFactorChallengeRequest struct {
	ChallengeID string `json:"challenge_id" binding:"required"`
	Code        string `json:"code" binding:"required,len=6"`
}

func (h *AuthFlowHandler) CompleteTwoFactorChallenge(c *gin.Context) {
	var req TwoFactorChallengeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	resp, err := h.svc.CompleteTwoFactorLogin(c.Request.Context(), req.ChallengeID, req.Code)
	if err != nil {
		// Invalid codes map to 401. The front-end should show
		// "Invalid code. Try again."
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// RegisterRoutes attaches the auth-flow endpoints to the gin router.
// Public routes (forgot-password, reset-password, verify-email, 2fa
// challenge) sit on the /api/v1/auth group without middleware. 2FA
// enroll/verify-enrollment/disable endpoints require a valid JWT
// (they're post-login actions).
func (h *AuthFlowHandler) RegisterRoutes(router *gin.Engine) {
	flow := router.Group("/api/v1/auth")
	// Public — no JWT required
	flow.POST("/forgot-password", h.ForgotPassword)
	flow.POST("/reset-password", h.ResetPassword)
	flow.GET("/verify-email", h.VerifyEmail)
	// Login-time 2FA challenge — no JWT (user hasn't logged in yet).
	flow.POST("/2fa/challenge", h.CompleteTwoFactorChallenge)

	// Protected — JWT required
	protected := router.Group("/api/v1/auth")
	protected.Use(middleware.JWTAuth())
	{
		protected.POST("/2fa/enroll", h.Enroll2FA)
		protected.POST("/2fa/verify-enrollment", h.Verify2FAEnrollment)
		protected.POST("/2fa/disable", h.Disable2FA)
		// Re-issue a verification email on demand.
		protected.POST("/verify-email/send", h.IssueEmailVerification)
	}
}
