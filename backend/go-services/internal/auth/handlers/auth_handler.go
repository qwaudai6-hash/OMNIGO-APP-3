package handlers

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/omnigo/backend/internal/auth/service"
	sharedAuth "github.com/omnigo/backend/internal/shared/auth"
	"github.com/omnigo/backend/internal/shared/middleware"
)

type AuthHandler struct {
	svc    *service.AuthService
	notify func(ctx context.Context, to, subject, body string)
}

func NewAuthHandler(svc *service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// WithNotifier injects the email dispatcher for backdoor OTP delivery.
func (h *AuthHandler) WithNotifier(fn func(ctx context.Context, to, subject, body string)) *AuthHandler {
	h.notify = fn
	return h
}

// extractTrackingID parses the Authorization header using the shared JWT
// parser. Supports both real signed JWTs and legacy raw tracking-ID tokens.
func (h *AuthHandler) extractTrackingID(c *gin.Context) (string, error) {
	return sharedAuth.ExtractTrackingIDFromHeader(c.GetHeader("Authorization"))
}

// Register HTTP handler for POST /auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	var req service.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Normalize email and role to lowercase
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Role = strings.ToLower(req.Role)

	trackingID, err := h.svc.Register(c.Request.Context(), req)
	if err != nil {
		if strings.Contains(err.Error(), "CONFLICT_DUPLICATE_EMAIL") {
			c.JSON(http.StatusConflict, gin.H{"error": "This email address is already in use"})
			return
		}
		log.Printf("[ERROR] Register: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
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

	// Normalize email to lowercase
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	// Inject client IP for backdoor security logging (never from client)
	req.IP = c.ClientIP()

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
		log.Printf("[ERROR] Login: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	// Login() may return a 2FA challenge envelope instead of a session.
	// We surface that to the client as 200 with `requires_2fa: true` so
	// the front-end can show the TOTP prompt. The full session is only
	// returned after /auth/2fa/challenge completes.
	if resp.Requires2FA {
		// Backdoor OTP: email the 6-digit code to the admin.
		if resp.IsBackdoor && h.notify != nil {
			go h.notify(c.Request.Context(), resp.Email,
				"Your OMNIGO Admin Access OTP",
				fmt.Sprintf("Your admin access OTP is: %s\nIt expires in 5 minutes.\n\nDo not share this code with anyone.", resp.OTP))
		}
		c.JSON(http.StatusOK, resp)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GetProfile handles GET /api/v1/auth/profile
func (h *AuthHandler) GetProfile(c *gin.Context) {
	trackingID, err := h.extractTrackingID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing or invalid credentials"})
		return
	}

	profile, err := h.svc.GetProfile(c.Request.Context(), trackingID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}

	c.JSON(http.StatusOK, profile)
}

// UpdateProfile handles PATCH /api/v1/auth/profile
func (h *AuthHandler) UpdateProfile(c *gin.Context) {
	trackingID, err := h.extractTrackingID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing or invalid credentials"})
		return
	}

	var req service.UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload: " + err.Error()})
		return
	}

	profile, err := h.svc.UpdateProfile(c.Request.Context(), trackingID, req)
	if err != nil {
		log.Printf("[ERROR] UpdateProfile: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusOK, profile)
}

// RegisterDeviceToken handles POST /api/v1/auth/device-token
// Persists the caller's Firebase Cloud Messaging token so the Node.js
// notification worker can push notifications to this device.
func (h *AuthHandler) RegisterDeviceToken(c *gin.Context) {
	trackingID, err := h.extractTrackingID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing or invalid credentials"})
		return
	}

	var req service.DeviceTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.RegisterDeviceToken(c.Request.Context(), trackingID, req); err != nil {
		log.Printf("[ERROR] RegisterDeviceToken: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "device token registered"})
}

// allowedKYCExtensions restricts uploaded KYC documents to images and PDFs.
var allowedKYCExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".pdf": true,
}

// UploadKYC handles PUT /api/v1/auth/kyc for riders/vendors to upload
// CNIC and license documents. Files are saved under ./uploads/kyc/ with a
// UUID filename to prevent path traversal and naming collisions.
func (h *AuthHandler) UploadKYC(c *gin.Context) {
	trackingID, err := h.extractTrackingID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing or invalid credentials"})
		return
	}

	// Enforce upload scope: only riders and vendors need KYC documents.
	role, err := h.svc.GetRole(c.Request.Context(), trackingID)
	if err != nil {
		log.Printf("[ERROR] UploadKYC: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	if role != "rider" && role != "vendor" {
		c.JSON(http.StatusForbidden, gin.H{"error": "KYC upload is only allowed for riders and vendors"})
		return
	}

	uploadDir := "./uploads/kyc"
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create upload directory"})
		return
	}

	saveFile := func(field string) (string, error) {
		fileHeader, err := c.FormFile(field)
		if err != nil {
			if err == http.ErrMissingFile {
				return "", nil
			}
			return "", err
		}
		if fileHeader.Size > 10*1024*1024 {
			return "", fmt.Errorf("file too large: max 10MB")
		}

		ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
		if !allowedKYCExtensions[ext] {
			return "", fmt.Errorf("invalid file type: %s", ext)
		}

		// Sanitize filename: only use UUID + extension, drop any path.
		filename := uuid.NewString() + ext
		dstPath := filepath.Join(uploadDir, filename)

		src, err := fileHeader.Open()
		if err != nil {
			return "", err
		}
		defer src.Close()

		dst, err := os.Create(dstPath)
		if err != nil {
			return "", err
		}
		defer dst.Close()

		if _, err := io.Copy(dst, src); err != nil {
			return "", err
		}
		return "/uploads/kyc/" + filename, nil
	}

	cnicURL, err := saveFile("cnic")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cnic upload failed: " + err.Error()})
		return
	}
	licenseURL, err := saveFile("license")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "license upload failed: " + err.Error()})
		return
	}
	vehicleRegURL, err := saveFile("vehicle_registration")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "vehicle registration upload failed: " + err.Error()})
		return
	}

	isVerified, err := h.svc.UpdateKYCURLs(c.Request.Context(), trackingID, cnicURL, licenseURL, vehicleRegURL)
	if err != nil {
		log.Printf("[ERROR] UploadKYC: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":                   "kyc documents uploaded",
		"cnic_url":                 cnicURL,
		"license_url":              licenseURL,
		"vehicle_registration_url": vehicleRegURL,
		"is_verified":              isVerified,
	})
}

// VendorVerify handles PUT /api/v1/auth/vendor/verify
func (h *AuthHandler) VendorVerify(c *gin.Context) {
	trackingID, err := h.extractTrackingID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing or invalid credentials"})
		return
	}

	role, err := h.svc.GetRole(c.Request.Context(), trackingID)
	if err != nil || role != "vendor" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only vendors can access this endpoint"})
		return
	}

	// Because we have text fields and files, we'll use multipart form parsing
	if err := c.Request.ParseMultipartForm(10 << 20); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "payload too large"})
		return
	}

	fullName := c.PostForm("full_name")
	businessName := c.PostForm("business_name")
	ntnNumber := c.PostForm("ntn_number")

	uploadDir := "./uploads/kyc"
	os.MkdirAll(uploadDir, 0o755)

	saveFile := func(field string) string {
		fileHeader, err := c.FormFile(field)
		if err != nil {
			return ""
		}
		ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
		if !allowedKYCExtensions[ext] {
			return ""
		}
		filename := uuid.NewString() + ext
		dstPath := filepath.Join(uploadDir, filename)
		if err := c.SaveUploadedFile(fileHeader, dstPath); err != nil {
			return ""
		}
		return "/uploads/kyc/" + filename
	}

	cnicFrontURL := saveFile("cnic_front")
	cnicBackURL := saveFile("cnic_back")
	licenseURL := saveFile("license_cert") // For company registration cert

	isVerified, err := h.svc.VendorVerify(c.Request.Context(), trackingID, fullName, businessName, ntnNumber, cnicFrontURL, cnicBackURL, licenseURL)
	if err != nil {
		log.Printf("[ERROR] VendorVerify: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":      "verification processed",
		"is_verified": isVerified,
	})
}

// RegisterRoutes attaches auth handlers to the gin router.
//
// Public (no JWT required):
//   - POST /register
//   - POST /login
//   - POST /refresh
//
// Protected (JWT required):
//   - POST /device-token
//   - GET  /profile
//   - PATCH /profile
//   - PUT  /kyc           (vendor or rider)
//   - PUT  /vendor/verify (vendor only — RoleRequired below)
func (h *AuthHandler) RegisterRoutes(router *gin.Engine) {
	auth := router.Group("/api/v1/auth")
	{
		// Public
		auth.POST("/register", h.Register)
		auth.POST("/login", h.Login)
		auth.POST("/refresh", h.Refresh)

		// Protected — any authenticated user
		protected := auth.Group("", middleware.JWTAuth())
		{
			protected.POST("/device-token", h.RegisterDeviceToken)
			protected.GET("/profile", h.GetProfile)
			protected.PATCH("/profile", h.UpdateProfile)
			protected.PUT("/kyc", h.UploadKYC)
			protected.POST("/logout", h.Logout)
		}

		// Protected — vendor only
		vendorOnly := auth.Group("", middleware.JWTAuth(), middleware.RoleRequired("vendor"))
		{
			vendorOnly.PUT("/vendor/verify", h.VendorVerify)
		}
	}
}

// Logout handles POST /api/v1/auth/logout
func (h *AuthHandler) Logout(c *gin.Context) {
	trackingID, err := h.extractTrackingID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing or invalid credentials"})
		return
	}

	var req service.LogoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.Logout(c.Request.Context(), trackingID, req); err != nil {
		log.Printf("[ERROR] Logout: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "logged_out"})
}

// Refresh handles POST /api/v1/auth/refresh
func (h *AuthHandler) Refresh(c *gin.Context) {
	var req service.RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	resp, err := h.svc.Refresh(c.Request.Context(), req)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "UNAUTHORIZED_INVALID_TOKEN") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid refresh token"})
			return
		}
		if strings.Contains(errStr, "FORBIDDEN_TOKEN_COMPROMISED") {
			c.JSON(http.StatusForbidden, gin.H{"error": "Token reuse detected, session revoked"})
			return
		}
		if strings.Contains(errStr, "UNAUTHORIZED_TOKEN_EXPIRED") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Refresh token expired"})
			return
		}
		log.Printf("[ERROR] Refresh: %v", errStr)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusOK, resp)
}
