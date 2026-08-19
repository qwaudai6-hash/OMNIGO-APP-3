package service

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type RegisterRequest struct {
	Name       string `json:"name" binding:"required"`
	Email      string `json:"email" binding:"required,email"`
	Password   string `json:"password" binding:"required,min=6"`
	Role       string `json:"role" binding:"required"`
	Region     string `json:"region"`
	CnicURL    string `json:"cnic_url"`
	LicenseURL string `json:"license_url"`
}

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

type AuthResponse struct {
	Token      string `json:"token"`
	TrackingID string `json:"tracking_id"`
	Role       string `json:"role"`
}

type AuthService struct {
	db *pgxpool.Pool
}

func NewAuthService(dbPool *pgxpool.Pool) *AuthService {
	return &AuthService{db: dbPool}
}

func generateTrackingID(role string) string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	randomNum := r.Intn(900000) + 100000 // 6-digit random number
	var prefix string
	switch role {
	case "customer":
		prefix = "CUST"
	case "rider":
		prefix = "RIDR"
	case "vendor":
		prefix = "VEND"
	default:
		prefix = "USER"
	}
	return fmt.Sprintf("%s-%d", prefix, randomNum)
}

// Register creates a new user inside the PostgreSQL DB after checking for duplicate email records.
func (s *AuthService) Register(ctx context.Context, req RegisterRequest) (string, error) {
	// 1. Relational Check: Validate email unique constraint
	var exists bool
	checkQuery := "SELECT EXISTS(SELECT 1 FROM users WHERE email = $1)"
	err := s.db.QueryRow(ctx, checkQuery, req.Email).Scan(&exists)
	if err != nil {
		return "", fmt.Errorf("failed verifying email availability: %w", err)
	}
	if exists {
		return "", errors.New("CONFLICT_DUPLICATE_EMAIL: this email is already registered")
	}

	// 2. Hash Password securely using bcrypt
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed generating secure password hash: %w", err)
	}

	// 3. Setup user metadata parameters
	trackingID := generateTrackingID(req.Role)
	region := req.Region
	if region == "" {
		region = "PK"
	}

	// Riders/Vendors require verification by default
	isVerified := true
	if req.Role == "rider" || req.Role == "vendor" {
		isVerified = false
	}

	// 4. Secure Insert Transaction
	insertQuery := `
		INSERT INTO users (tracking_id, email, full_name, password_hash, role, region, cnic_url, license_url, is_verified)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err = s.db.Exec(ctx, insertQuery,
		trackingID,
		req.Email,
		req.Name,
		string(hashedPassword),
		req.Role,
		region,
		req.CnicURL,
		req.LicenseURL,
		isVerified,
	)
	if err != nil {
		return "", fmt.Errorf("failed executing database insert: %w", err)
	}

	return trackingID, nil
}

// Login verifies user credentials and returns tracking parameters.
func (s *AuthService) Login(ctx context.Context, req LoginRequest) (AuthResponse, error) {
	var id string
	var trackingID string
	var passwordHash string
	var role string
	var isVerified bool

	query := "SELECT id, tracking_id, password_hash, role, is_verified FROM users WHERE email = $1"
	err := s.db.QueryRow(ctx, query, req.Email).Scan(&id, &trackingID, &passwordHash, &role, &isVerified)
	if err != nil {
		return AuthResponse{}, errors.New("UNAUTHORIZED_BAD_CREDENTIALS: invalid email or password")
	}

	// Verify Hash matching
	err = bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password))
	if err != nil {
		return AuthResponse{}, errors.New("UNAUTHORIZED_BAD_CREDENTIALS: invalid email or password")
	}

	// Verification guard (Riders / Vendors must be approved)
	if !isVerified {
		return AuthResponse{}, errors.New("FORBIDDEN_PENDING_VERIFICATION: account is registered but pending admin verification")
	}

	// Return mock token for validation along with tracking session details
	token := fmt.Sprintf("jwt_token_session_%s_%d", trackingID, time.Now().Unix())
	return AuthResponse{
		Token:      token,
		TrackingID: trackingID,
		Role:       role,
	}, nil
}
