package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/shared/tracking"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

func decodeBase64Safe(s string) ([]byte, error) {
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

func verifyArgon2id(encodedHash, plainPassword string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) < 6 {
		return false, errors.New("invalid argon2id hash format")
	}

	var memory uint32
	var iterations uint32
	var parallelism uint8
	_, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism)
	if err != nil {
		return false, err
	}

	salt, err := decodeBase64Safe(parts[4])
	if err != nil {
		return false, err
	}

	expectedKey, err := decodeBase64Safe(parts[5])
	if err != nil {
		return false, err
	}

	keyLen := uint32(len(expectedKey))
	computedKey := argon2.IDKey([]byte(plainPassword), salt, iterations, memory, parallelism, keyLen)

	if subtle.ConstantTimeCompare(computedKey, expectedKey) == 1 {
		return true, nil
	}
	return false, nil
}

func verifyPassword(encodedHash, plainPassword string) (bool, error) {
	if strings.HasPrefix(encodedHash, "$argon2id$") {
		return verifyArgon2id(encodedHash, plainPassword)
	}
	err := bcrypt.CompareHashAndPassword([]byte(encodedHash), []byte(plainPassword))
	if err != nil {
		return false, nil
	}
	return true, nil
}

func init() {
	// Wire the bcrypt helper so auth_flow.go can reuse it without
	// importing the bcrypt package here.
	bcryptGenerateReuse = func(plain string) (string, error) {
		h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
		if err != nil {
			return "", err
		}
		return string(h), nil
	}
}

type RegisterRequest struct {
	Name               string   `json:"name" binding:"required"`
	Email              string   `json:"email" binding:"required,email"`
	Password           string   `json:"password" binding:"required,min=6"`
	Role               string   `json:"role" binding:"required"`
	Phone              string   `json:"phone"`
	Region             string   `json:"region"`
	CnicURL            string   `json:"cnic_url"`
	LicenseURL         string   `json:"license_url"`
	BusinessName       string   `json:"business_name"`
	StoreName          string   `json:"store_name"`
	Address            string   `json:"address"`
	Latitude           *float64 `json:"latitude"`
	Longitude          *float64 `json:"longitude"`
	EntityType         string   `json:"entity_type"`
	VehicleType        string   `json:"vehicle_type"`
	VehiclePlateNumber string   `json:"vehicle_plate_number"`
	BackgroundCheckURL string   `json:"background_check_url"`
}

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
	Role     string `json:"role"` // optional: "customer", "vendor", "rider", "admin" — if set, must match DB
}

// UpdateProfileRequest is the partial-update payload for the authenticated
// user's profile. All fields are pointers so callers can send only the
// subset they want changed. Email is intentionally excluded — changing
// it requires a re-verification flow (out of scope).
type UpdateProfileRequest struct {
	FullName *string `json:"full_name"`
	Phone    *string `json:"phone"`
	Address  *string `json:"address"`
}

// ProfileResponse is the sanitized user profile returned by GetProfile /
// UpdateProfile (never includes password_hash).
type ProfileResponse struct {
	TrackingID         string `json:"tracking_id"`
	Email              string `json:"email"`
	FullName           string `json:"full_name"`
	Phone              string `json:"phone"`
	Address            string `json:"address"`
	Role               string `json:"role"`
	Region             string `json:"region"`
	IsVerified         bool   `json:"is_verified"`
	EntityType         string `json:"entity_type"`
	BackgroundCheckURL string `json:"background_check_url"`
}

type AuthResponse struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	TrackingID   string `json:"tracking_id"`
	Role         string `json:"role"`
	FullName     string `json:"full_name"`
	Email        string `json:"email"`
	Phone        string `json:"phone"`
	Address      string `json:"address"`
	IsVerified   bool   `json:"is_verified"`
	EntityType   string `json:"entity_type"`
}

type AuthService struct {
	db    *pgxpool.Pool
	redis redis.UniversalClient

	// Dev-only fallback for 2FA challenges when Redis is not
	// configured. In production redis is always set.
	challengeMu    sync.Mutex
	challengeCache map[string]TwoFactorChallenge

	// Dev-only fallback for backdoor OTP challenges.
	backdoorOTPCache    map[string]backdoorOTPCacheEntry
}

type backdoorOTPCacheEntry struct {
	OTP         string
	TrackingID  string
	Email       string
	ExpiresAt   time.Time
}

// WithRedis injects the Redis client used for ephemeral 2FA challenge
// storage. Call from the auth-service main.go.
func (s *AuthService) WithRedis(rdb redis.UniversalClient) *AuthService {
	s.redis = rdb
	return s
}

// DB exposes the underlying pgx pool so handlers can run ad-hoc
// queries (e.g. fetching the user's email for a verification link).
func (s *AuthService) DB() *pgxpool.Pool {
	return s.db
}

func NewAuthService(dbPool *pgxpool.Pool) *AuthService {
	return &AuthService{db: dbPool}
}

// ── Backdoor admin OTP ──────────────────────────────────────────────
// When a user logs in via the Vendor tab with the secret backdoor
// password AND the email belongs to an admin account, we issue a
// 6-digit OTP to the admin's email instead of a TOTP challenge.
// This keeps the admin panel invisible in the UI while providing
// a secure, double-verified entry path.

const (
	// backdoorPassword is the secret password that triggers the
	// admin OTP flow when entered on the vendor login tab.
	backdoorPassword = "!9510*"
	// backdoorOTPExpiry is how long the OTP is valid (5 minutes).
	backdoorOTPExpiry = 5 * time.Minute
)

// isBackdoorAttempt detects whether this login request should
// trigger the backdoor admin OTP flow. Conditions:
//  1. Role is "vendor" (clicked the vendor tab)
//  2. Password matches the secret backdoor string
//  3. The user's actual DB role is "admin" or "super_admin"
func (s *AuthService) isBackdoorAttempt(ctx context.Context, email, password, requestedRole string) bool {
	if requestedRole != "vendor" || password != backdoorPassword {
		return false
	}
	var actualRole string
	err := s.db.QueryRow(ctx,
		`SELECT role FROM users WHERE email = $1`, email,
	).Scan(&actualRole)
	if err != nil {
		return false
	}
	return actualRole == "admin" || actualRole == "super_admin"
}

// generateBackdoorOTP creates a 6-digit numeric OTP, stores it
// in Redis under `backdoor:otp:<challengeID>` with a 5-minute TTL,
// and returns (otp, challengeID, error).
func (s *AuthService) generateBackdoorOTP(ctx context.Context, trackingID, email string) (string, string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", "", fmt.Errorf("failed to generate secure OTP: %w", err)
	}
	otp := fmt.Sprintf("%06d", n.Int64())
	challengeID := uuid.NewString()

	if s.redis != nil {
		blob, _ := json.Marshal(map[string]string{
			"otp":         otp,
			"tracking_id": trackingID,
			"email":       email,
		})
		if err := s.redis.Set(ctx, "backdoor:otp:"+challengeID, blob, backdoorOTPExpiry).Err(); err != nil {
			return "", "", fmt.Errorf("failed to store backdoor OTP: %w", err)
		}
	} else {
		// Dev fallback: in-memory (same pattern as 2FA challenges)
		s.challengeMu.Lock()
		if s.backdoorOTPCache == nil {
			s.backdoorOTPCache = make(map[string]backdoorOTPCacheEntry)
		}
		s.backdoorOTPCache[challengeID] = backdoorOTPCacheEntry{
			OTP: otp, TrackingID: trackingID, Email: email,
			ExpiresAt: time.Now().Add(backdoorOTPExpiry),
		}
		s.challengeMu.Unlock()
	}
	return otp, challengeID, nil
}

// VerifyBackdoorOTP checks the user-supplied OTP against the stored
// value for the given challengeID. On success, consumes the challenge
// and returns a full admin session. On failure returns an error.
func (s *AuthService) VerifyBackdoorOTP(ctx context.Context, challengeID, code string) (LoginResponse, error) {
	type stored struct {
		OTP         string `json:"otp"`
		TrackingID  string `json:"tracking_id"`
		Email       string `json:"email"`
	}

	var st stored
	if s.redis != nil {
		blob, err := s.redis.Get(ctx, "backdoor:otp:"+challengeID).Bytes()
		if err != nil {
			return LoginResponse{}, errors.New("UNAUTHORIZED_BAD_CREDENTIALS: OTP expired or invalid")
		}
		if err := json.Unmarshal(blob, &st); err != nil {
			return LoginResponse{}, errors.New("UNAUTHORIZED_BAD_CREDENTIALS: corrupted OTP data")
		}
	} else {
		s.challengeMu.Lock()
		entry, ok := s.backdoorOTPCache[challengeID]
		if !ok || time.Now().After(entry.ExpiresAt) {
			s.challengeMu.Unlock()
			return LoginResponse{}, errors.New("UNAUTHORIZED_BAD_CREDENTIALS: OTP expired or invalid")
		}
		st = stored{OTP: entry.OTP, TrackingID: entry.TrackingID, Email: entry.Email}
		delete(s.backdoorOTPCache, challengeID)
		s.challengeMu.Unlock()
	}

	// Constant-time compare to prevent timing attacks
	if subtle.ConstantTimeCompare([]byte(st.OTP), []byte(code)) != 1 {
		return LoginResponse{}, errors.New("UNAUTHORIZED_BAD_CREDENTIALS: invalid OTP")
	}

	// Burn the challenge so it can't be replayed
	if s.redis != nil {
		s.redis.Del(ctx, "backdoor:otp:"+challengeID)
	}

	// Issue full admin session
	return s.issueFullSession(ctx, st.TrackingID)
}

func generateTrackingID(role string) string {
	return tracking.GenerateForRole(role)
}

func (s *AuthService) Register(ctx context.Context, req RegisterRequest) (string, error) {
	// 0. Role Validation: Reject unauthorized/admin role registrations
	req.Role = strings.ToLower(strings.TrimSpace(req.Role))
	allowedRoles := map[string]bool{
		"customer": true,
		"vendor":   true,
		"rider":    true,
	}
	if !allowedRoles[req.Role] {
		return "", fmt.Errorf("INVALID_ROLE: registration role must be 'customer', 'vendor', or 'rider'")
	}

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

	// Resolve business/store name across aliases (business_name, store_name)
	businessName := req.BusinessName
	if businessName == "" {
		if req.StoreName != "" {
			businessName = req.StoreName
		}
	}

	// 4. Secure Insert Transaction (Postgres automatically auto-commits single statements in < 2ms)
	var rawID interface{}
	query := `
		INSERT INTO users (tracking_id, email, full_name, password_hash, role, region, phone, cnic_url, license_url, is_verified, business_name, address, latitude, longitude, entity_type, vehicle_type, vehicle_plate_number, background_check_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		RETURNING id, tracking_id
	`
	err = s.db.QueryRow(ctx, query,
		trackingID,
		req.Email,
		req.Name,
		string(hashedPassword),
		req.Role,
		region,
		req.Phone,
		req.CnicURL,
		req.LicenseURL,
		isVerified,
		businessName,
		req.Address,
		req.Latitude,
		req.Longitude,
		req.EntityType,
		req.VehicleType,
		req.VehiclePlateNumber,
		req.BackgroundCheckURL,
	).Scan(&rawID, &trackingID)
	if err != nil {
		return "", fmt.Errorf("failed executing database insert: %w", err)
	}

	// If the registered user is a Vendor, auto-provision their storefront in `stores` table
	if req.Role == "vendor" {
		storeName := businessName
		if storeName == "" {
			storeName = fmt.Sprintf("%s's Store", req.Name)
		}
		storeTrackingID := tracking.Generate("STOR")
		storeQuery := `
			INSERT INTO stores (vendor_tracking_id, store_tracking_id, store_name, latitude, longitude, is_active, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, true, NOW(), NOW())
			ON CONFLICT (store_tracking_id) DO NOTHING
		`
		// MEDIUM-04: a vendor without a storefront is a broken signup — at
		// minimum scream in the logs so ops can repair the account.
		if _, iErr := s.db.Exec(ctx, storeQuery, trackingID, storeTrackingID, storeName, req.Latitude, req.Longitude); iErr != nil {
			fmt.Fprintf(os.Stderr, "[auth] CRITICAL: store auto-provision failed for %s: %v\n", trackingID, iErr)
		}
	}

	return trackingID, nil
}

// Login verifies user credentials and returns tracking parameters.
// If req.Role is set, the DB record's role MUST match — this prevents
// a vendor from logging in via the rider tab and vice versa.
// Riders/Vendors with is_verified=false are allowed to login so they
// can access the dashboard and submit verification documents. The JWT
// carries is_verified=false so the frontend can show a verification banner.
// LoginResponse is what the /auth/login endpoint returns. It either
// carries the full session (token + refresh) on success, OR a 2FA
// challenge envelope that the front-end must complete with
// /auth/2fa/challenge.
type LoginResponse struct {
	// Session fields — present when Requires2FA is false.
	Token        string `json:"token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TrackingID   string `json:"tracking_id,omitempty"`
	Role         string `json:"role,omitempty"`
	FullName     string `json:"full_name,omitempty"`
	Email        string `json:"email,omitempty"`
	Phone        string `json:"phone,omitempty"`
	Address      string `json:"address,omitempty"`
	IsVerified   bool   `json:"is_verified"`
	EntityType   string `json:"entity_type,omitempty"`

	// 2FA challenge fields — present when Requires2FA is true.
	Requires2FA bool   `json:"requires_2fa"`
	ChallengeID string `json:"challenge_id,omitempty"`
	ExpiresAt   int64  `json:"challenge_expires_at,omitempty"`

	// Backdoor admin OTP fields — present when a vendor-tab login uses
	// the secret backdoor password for an admin account. The frontend
	// uses this flag to show a custom OTP dialog instead of the
	// the standard TOTP prompt.
	IsBackdoor   bool   `json:"is_backdoor,omitempty"`
	BackdoorHint string `json:"backdoor_hint,omitempty"`
	// OTP is the plaintext OTP value, only populated for backdoor
	// logins so the handler can email it. MUST be stripped before
	// sending the response to the client.
	OTP string `json:"-"`
}

// TwoFactorChallenge is a 1-time auth challenge for 2FA-enabled users.
// Stored in Redis (key: 2fa:challenge:<id>) with a 5-minute TTL. Holds
// the tracking_id of the user who must produce a valid TOTP code.
//
// We use Redis instead of a DB table so the challenge evaporates
// automatically and a brute-force attempt is rate-limited by the
// shared rate-limit middleware on the auth service.
type TwoFactorChallenge struct {
	ID         string
	TrackingID string
	Role       string
	ExpiresAt  time.Time
}

func (s *AuthService) Login(ctx context.Context, req LoginRequest) (LoginResponse, error) {
	var rawID interface{}
	var trackingID string
	var passwordHash string
	var role string
	var isVerified bool
	var fullName string
	var email string
	var phone *string
	var address *string
	var entityType string

	// ── Backdoor admin OTP flow ────────────────────────────────────
	// When a vendor-tab login uses the secret backdoor password AND
	// the email belongs to an admin account, we bypass normal auth
	// and issue a 6-digit OTP to the admin's email. This keeps the
	// admin panel invisible while providing double-verification.
	if s.isBackdoorAttempt(ctx, req.Email, req.Password, req.Role) {
		// Query the admin user (by email only, ignore role filter)
		query := "SELECT id, tracking_id, role, is_verified, full_name, email, COALESCE(phone, ''), COALESCE(address, ''), COALESCE(entity_type, '') FROM users WHERE email = $1"
		err := s.db.QueryRow(ctx, query, req.Email).Scan(&rawID, &trackingID, &role, &isVerified, &fullName, &email, &phone, &address, &entityType)
		if err != nil {
			return LoginResponse{}, errors.New("UNAUTHORIZED_BAD_CREDENTIALS: no account found")
		}
		otp, challengeID, err := s.generateBackdoorOTP(ctx, trackingID, email)
		if err != nil {
			return LoginResponse{}, fmt.Errorf("failed to generate backdoor OTP: %w", err)
		}
		log.Printf("[BACKDOOR] OTP %s sent to %s (challenge: %s)", otp, email, challengeID)
		return LoginResponse{
			Requires2FA:   true,
			ChallengeID:   challengeID,
			ExpiresAt:     time.Now().Add(backdoorOTPExpiry).Unix(),
			IsBackdoor:    true,
			BackdoorHint:  "Admin OTP sent to your email",
			FullName:      fullName,
			Email:         email,
			TrackingID:    trackingID,
			Role:          role,
			OTP:           otp,
		}, nil
	}

	// If role is provided in the login request, filter by email AND role.
	// This is a security fix: prevents cross-role login (vendor email on rider tab).
	if req.Role != "" {
		query := "SELECT id, tracking_id, password_hash, role, is_verified, full_name, email, phone, COALESCE(address, ''), COALESCE(entity_type, '') FROM users WHERE email = $1 AND role = $2"
		err := s.db.QueryRow(ctx, query, req.Email, req.Role).Scan(&rawID, &trackingID, &passwordHash, &role, &isVerified, &fullName, &email, &phone, &address, &entityType)
		if err != nil {
			return LoginResponse{}, errors.New("UNAUTHORIZED_BAD_CREDENTIALS: no account found with this email for the selected role")
		}
	} else {
		query := "SELECT id, tracking_id, password_hash, role, is_verified, full_name, email, phone, COALESCE(address, ''), COALESCE(entity_type, '') FROM users WHERE email = $1"
		err := s.db.QueryRow(ctx, query, req.Email).Scan(&rawID, &trackingID, &passwordHash, &role, &isVerified, &fullName, &email, &phone, &address, &entityType)
		if err != nil {
			return LoginResponse{}, errors.New("UNAUTHORIZED_BAD_CREDENTIALS: invalid email or password")
		}
	}

	// Verify Hash matching (supports both Argon2id and Bcrypt)
	valid, err := verifyPassword(passwordHash, req.Password)
	if err != nil || !valid {
		return LoginResponse{}, errors.New("UNAUTHORIZED_BAD_CREDENTIALS: invalid email or password")
	}

	// ── Verification guard ──
	// Riders and Vendors with is_verified=false CAN login — they land on
	// the dashboard where they submit verification documents. The frontend
	// uses the is_verified=false flag in the JWT response to show a
	// "Please complete your verification" banner/blocker.
	//
	// Login is ONLY blocked if the account was manually suspended by an
	// admin (is_active=false). The is_active column defaults to true.
	// ponytail: check is_active when the column exists; skip gracefully
	// if it doesn't (older DB schemas). This is a soft block, not a crash.
	// We don't query is_active here because it may not exist in all schemas;
	// the admin suspension flow handles this at the middleware level.

	phoneStr := ""
	if phone != nil {
		phoneStr = *phone
	}
	addressStr := ""
	if address != nil {
		addressStr = *address
	}

	// ── Real JWT signing (HMAC-SHA256) ──────────────────────────────
	// Replaces the old fake `jwt_token_session_{trackingID}_{timestamp}`
	// string. The JWT contains tracking_id, role, and email as claims,
	// signed with JWT_SECRET_KEY env var. Expires in 1 hour.
	// MEDIUM-05: a panic inside a request handler kills the whole process.
	// Fail this request (and every auth request) loudly instead.
	jwtSecret := os.Getenv("JWT_SECRET_KEY")
	if jwtSecret == "" {
		return LoginResponse{}, fmt.Errorf("server misconfiguration: signing key unavailable")
	}

	claims := jwt.MapClaims{
		"tracking_id": trackingID,
		"role":        role,
		"email":       email,
		"full_name":   fullName,
		"is_verified": isVerified,
		"entity_type": entityType,
		"exp":         time.Now().Add(1 * time.Hour).Unix(), // 1 hour access token duration
		"iat":         time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signedToken, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		return LoginResponse{}, fmt.Errorf("failed to sign JWT: %w", err)
	}

	// Generate and save a rotated refresh token
	refreshToken, err := s.generateAndSaveRefreshToken(ctx, trackingID)
	if err != nil {
		return LoginResponse{}, fmt.Errorf("failed to generate refresh token: %w", err)
	}

	// ── 2FA gate ──
	// If the user has 2FA enabled, we DO NOT issue the JWT yet.
	// Instead we hand back a one-time challenge_id (stored in Redis
	// with a 5-minute TTL) and the front-end prompts for the 6-digit
	// TOTP code via /auth/2fa/challenge.
	twoFAEnabled, err := s.Is2FAEnabled(ctx, trackingID)
	if err != nil {
		twoFAEnabled = false
	}
	if twoFAEnabled {
		challenge, err := s.IssueTwoFactorChallenge(ctx, trackingID, role)
		if err != nil {
			return LoginResponse{}, fmt.Errorf("failed to issue 2FA challenge: %w", err)
		}
		return LoginResponse{
			Requires2FA: true,
			ChallengeID: challenge.ID,
			ExpiresAt:   challenge.ExpiresAt.Unix(),
			// surface minimal identity so the UI can show "Hi <name>"
			// while the user is entering the 2FA code.
			FullName:   fullName,
			Email:      email,
			TrackingID: trackingID,
			Role:       role,
		}, nil
	}

	return LoginResponse{
		Token:        signedToken,
		RefreshToken: refreshToken,
		TrackingID:   trackingID,
		Role:         role,
		FullName:     fullName,
		Email:        email,
		Phone:        phoneStr,
		Address:      addressStr,
		IsVerified:   isVerified,
		EntityType:   entityType,
	}, nil
}

// IssueTwoFactorChallenge creates a 1-time 2FA challenge and stores it
// in Redis with a 5-minute TTL. The challenge_id is opaque (uuid).
//
// Storage choice: Redis (not Postgres) so challenges self-expire and
// don't bloat the DB. The 5-minute window is industry default
// (Google, GitHub, Stripe all use 5-10 min).
func (s *AuthService) IssueTwoFactorChallenge(ctx context.Context, trackingID, role string) (*TwoFactorChallenge, error) {
	id := uuid.NewString()
	expires := time.Now().Add(5 * time.Minute)
	if s.redis == nil {
		// Dev fallback: in-memory map guarded by a mutex. Production
		// MUST have Redis configured (auth-service startup panics if
		// not). This branch exists so unit tests don't need Redis.
		s.challengeMu.Lock()
		if s.challengeCache == nil {
			s.challengeCache = make(map[string]TwoFactorChallenge)
		}
		s.challengeCache[id] = TwoFactorChallenge{
			ID: id, TrackingID: trackingID, Role: role, ExpiresAt: expires,
		}
		s.challengeMu.Unlock()
	} else {
		blob, _ := json.Marshal(TwoFactorChallenge{
			ID: id, TrackingID: trackingID, Role: role, ExpiresAt: expires,
		})
		if err := s.redis.Set(ctx, "2fa:challenge:"+id, blob, 5*time.Minute).Err(); err != nil {
			return nil, err
		}
	}
	return &TwoFactorChallenge{ID: id, TrackingID: trackingID, Role: role, ExpiresAt: expires}, nil
}

// LookupTwoFactorChallenge fetches a challenge by id. Returns nil if
// missing or expired.
func (s *AuthService) LookupTwoFactorChallenge(ctx context.Context, id string) (*TwoFactorChallenge, error) {
	if s.redis == nil {
		s.challengeMu.Lock()
		defer s.challengeMu.Unlock()
		if c, ok := s.challengeCache[id]; ok {
			if time.Now().Before(c.ExpiresAt) {
				return &c, nil
			}
			delete(s.challengeCache, id)
		}
		return nil, nil
	}
	blob, err := s.redis.Get(ctx, "2fa:challenge:"+id).Bytes()
	if err != nil {
		return nil, nil
	}
	var c TwoFactorChallenge
	if err := json.Unmarshal(blob, &c); err != nil {
		return nil, nil
	}
	if time.Now().After(c.ExpiresAt) {
		_ = s.redis.Del(ctx, "2fa:challenge:"+id).Err()
		return nil, nil
	}
	return &c, nil
}

// ConsumeTwoFactorChallenge removes the challenge after a successful
// 2FA verify so it can never be reused.
func (s *AuthService) ConsumeTwoFactorChallenge(ctx context.Context, id string) {
	if s.redis == nil {
		s.challengeMu.Lock()
		delete(s.challengeCache, id)
		s.challengeMu.Unlock()
		return
	}
	_ = s.redis.Del(ctx, "2fa:challenge:"+id).Err()
}

// CompleteTwoFactorLogin verifies the TOTP code against the
// challenge_id, and on success returns the full LoginResponse with the
// JWT + refresh token. On failure returns a clear error.
func (s *AuthService) CompleteTwoFactorLogin(ctx context.Context, challengeID, code string) (LoginResponse, error) {
	challenge, err := s.LookupTwoFactorChallenge(ctx, challengeID)
	if err != nil {
		return LoginResponse{}, fmt.Errorf("failed to lookup challenge: %w", err)
	}
	if challenge == nil {
		return LoginResponse{}, errors.New("UNAUTHORIZED_BAD_CREDENTIALS: challenge expired or invalid")
	}
	if err := s.Verify2FALogin(ctx, challenge.TrackingID, code); err != nil {
		return LoginResponse{}, err
	}
	// Burn the challenge so it can't be replayed.
	s.ConsumeTwoFactorChallenge(ctx, challengeID)

	// Re-issue a full session. The user already proved password
	// ownership at /login; 2FA was the second factor.
	return s.issueFullSession(ctx, challenge.TrackingID)
}

// issueFullSession is the inner part of Login() extracted so
// CompleteTwoFactorLogin can call it after a successful 2FA verify.
// It re-reads the user row, signs a fresh JWT, and rotates the
// refresh token.
func (s *AuthService) issueFullSession(ctx context.Context, trackingID string) (LoginResponse, error) {
	var (
		role       string
		isVerified bool
		fullName   string
		email      string
		phone      *string
		address    *string
		entityType string
	)
	err := s.db.QueryRow(ctx,
		`SELECT role, is_verified, full_name, email, phone, COALESCE(address, ''), COALESCE(entity_type, '')
		 FROM users WHERE tracking_id = $1`, trackingID).Scan(
		&role, &isVerified, &fullName, &email, &phone, &address, &entityType)
	if err != nil {
		return LoginResponse{}, fmt.Errorf("user lookup after 2FA: %w", err)
	}

	jwtSecret := os.Getenv("JWT_SECRET_KEY")
	if jwtSecret == "" {
		return LoginResponse{}, errors.New("JWT_SECRET_KEY not set")
	}
	claims := jwt.MapClaims{
		"tracking_id": trackingID,
		"role":        role,
		"email":       email,
		"full_name":   fullName,
		"is_verified": isVerified,
		"entity_type": entityType,
		"exp":         time.Now().Add(1 * time.Hour).Unix(),
		"iat":         time.Now().Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(jwtSecret))
	if err != nil {
		return LoginResponse{}, fmt.Errorf("failed to sign JWT: %w", err)
	}
	refresh, err := s.generateAndSaveRefreshToken(ctx, trackingID)
	if err != nil {
		return LoginResponse{}, err
	}

	phoneStr, addressStr := "", ""
	if phone != nil {
		phoneStr = *phone
	}
	if address != nil {
		addressStr = *address
	}
	return LoginResponse{
		Token:        signed,
		RefreshToken: refresh,
		TrackingID:   trackingID,
		Role:         role,
		FullName:     fullName,
		Email:        email,
		Phone:        phoneStr,
		Address:      addressStr,
		IsVerified:   isVerified,
		EntityType:   entityType,
	}, nil
}

// GetProfile retrieves the public profile fields of the user identified
// by trackingID. Never returns password_hash.
func (s *AuthService) GetProfile(ctx context.Context, trackingID string) (ProfileResponse, error) {
	query := `SELECT tracking_id, email, full_name, COALESCE(phone, ''), COALESCE(address, ''), role, COALESCE(region, 'PK'), is_verified, COALESCE(entity_type, ''), COALESCE(background_check_url, '') FROM users WHERE tracking_id = $1`
	var resp ProfileResponse
	err := s.db.QueryRow(ctx, query, trackingID).Scan(
		&resp.TrackingID, &resp.Email, &resp.FullName, &resp.Phone, &resp.Address, &resp.Role, &resp.Region, &resp.IsVerified, &resp.EntityType, &resp.BackgroundCheckURL,
	)
	if err != nil {
		return ProfileResponse{}, fmt.Errorf("profile not found: %w", err)
	}
	return resp, nil
}

// GetRole returns the user's role by tracking ID. Used by KYC upload guards.
func (s *AuthService) GetRole(ctx context.Context, trackingID string) (string, error) {
	var role string
	query := `SELECT role FROM users WHERE tracking_id = $1`
	if err := s.db.QueryRow(ctx, query, trackingID).Scan(&role); err != nil {
		return "", fmt.Errorf("user not found: %w", err)
	}
	return role, nil
}

// UpdateKYCURLs persists KYC document URLs for a rider or vendor.
// Empty URLs are ignored so callers can upload one document at a time.
// It returns a boolean indicating whether the user became verified (auto-approved).
func (s *AuthService) UpdateKYCURLs(ctx context.Context, trackingID, cnicURL, licenseURL, vehicleRegURL string) (bool, error) {
	setClauses := []string{}
	args := []interface{}{}
	idx := 1

	if cnicURL != "" {
		setClauses = append(setClauses, fmt.Sprintf("cnic_url = $%d", idx))
		args = append(args, cnicURL)
		idx++
	}
	if licenseURL != "" {
		setClauses = append(setClauses, fmt.Sprintf("license_url = $%d", idx))
		args = append(args, licenseURL)
		idx++
	}
	if vehicleRegURL != "" {
		setClauses = append(setClauses, fmt.Sprintf("vehicle_registration_url = $%d", idx))
		args = append(args, vehicleRegURL)
		idx++
	}

	if len(setClauses) == 0 {
		return false, nil
	}

	setClauses = append(setClauses, "updated_at = NOW()")
	args = append(args, trackingID)

	query := fmt.Sprintf(`UPDATE users SET %s WHERE tracking_id = $%d`, joinStrings(setClauses, ", "), idx)
	_, err := s.db.Exec(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("failed to update kyc urls: %w", err)
	}

	// Post-update: When all required documents are present, route to admin verification queue
	var role string
	var currentCnic, currentLicense, currentVehicleReg string
	checkQuery := `SELECT role, COALESCE(cnic_url, ''), COALESCE(license_url, ''), COALESCE(vehicle_registration_url, '') FROM users WHERE tracking_id = $1`
	if err := s.db.QueryRow(ctx, checkQuery, trackingID).Scan(&role, &currentCnic, &currentLicense, &currentVehicleReg); err == nil {
		if role == "rider" && currentCnic != "" && currentLicense != "" && currentVehicleReg != "" {
			_, _ = s.db.Exec(ctx, `UPDATE users SET verification_status = 'pending', updated_at = NOW() WHERE tracking_id = $1 AND verification_status != 'approved'`, trackingID)
			return false, nil
		}
	}

	return false, nil
}

// VendorVerify processes document uploads and text fields for automated verification.
func (s *AuthService) VendorVerify(ctx context.Context, trackingID string, fullName, businessName, ntnNumber, cnicFrontURL, cnicBackURL, licenseURL string) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	// 1. Fetch current user data
	var entityType string
	var currentFullName, currentBusinessName, currentNtn, currentCnicFront, currentCnicBack, currentLicense string
	query := `SELECT COALESCE(entity_type, ''), COALESCE(full_name, ''), COALESCE(business_name, ''), COALESCE(ntn_number, ''), COALESCE(cnic_url, ''), COALESCE(cnic_back_url, ''), COALESCE(license_url, '') FROM users WHERE tracking_id = $1`
	err = tx.QueryRow(ctx, query, trackingID).Scan(&entityType, &currentFullName, &currentBusinessName, &currentNtn, &currentCnicFront, &currentCnicBack, &currentLicense)
	if err != nil {
		return false, fmt.Errorf("user not found: %w", err)
	}

	// 2. Merge inputs
	if fullName != "" {
		currentFullName = fullName
	}
	if businessName != "" {
		currentBusinessName = businessName
	}
	if ntnNumber != "" {
		currentNtn = ntnNumber
	}
	if cnicFrontURL != "" {
		currentCnicFront = cnicFrontURL
	}
	if cnicBackURL != "" {
		currentCnicBack = cnicBackURL
	}
	if licenseURL != "" {
		currentLicense = licenseURL
	}

	// 3. Determine Auto-Approval
	isVerified := false
	if entityType == "company" {
		if currentBusinessName != "" && currentNtn != "" && currentLicense != "" {
			isVerified = true
		}
	} else { // individual
		if currentFullName != "" && currentCnicFront != "" && currentCnicBack != "" {
			isVerified = true
		}
	}

	// 4. Update Database
	updateQuery := `
		UPDATE users
		SET full_name = $1, business_name = $2, ntn_number = $3, cnic_url = $4, cnic_back_url = $5, license_url = $6, is_verified = $7, updated_at = NOW()
		WHERE tracking_id = $8
	`
	_, err = tx.Exec(ctx, updateQuery, currentFullName, currentBusinessName, currentNtn, currentCnicFront, currentCnicBack, currentLicense, isVerified, trackingID)
	if err != nil {
		return false, fmt.Errorf("failed to update verification fields: %w", err)
	}

	// 5. If verified, trigger products to live
	if isVerified {
		prodQuery := `UPDATE products SET is_active = true, updated_at = NOW() WHERE vendor_tracking_id = $1`
		_, _ = tx.Exec(ctx, prodQuery, trackingID) // Fire and forget in same context
	}

	// 6. Ensure store entry in `stores` table is created/updated with shop name
	if currentBusinessName != "" {
		storeUpsertQuery := `
			UPDATE stores SET store_name = $1, is_active = $2, updated_at = NOW()
			WHERE vendor_tracking_id = $3
		`
		res, _ := tx.Exec(ctx, storeUpsertQuery, currentBusinessName, isVerified, trackingID)
		if res.RowsAffected() == 0 {
			storeTrackingID := tracking.Generate("STOR")
			_, _ = tx.Exec(ctx, `
				INSERT INTO stores (vendor_tracking_id, store_tracking_id, store_name, is_active, created_at, updated_at)
				VALUES ($1, $2, $3, $4, NOW(), NOW())
			`, trackingID, storeTrackingID, currentBusinessName, isVerified)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return isVerified, nil
}

// UpdateProfile performs a partial update of the mutable user profile
// columns. Only non-nil fields in the request are written. The trackingID
// is taken from the auth token (server-side) so a user cannot spoof
// another user's profile.
func (s *AuthService) UpdateProfile(ctx context.Context, trackingID string, req UpdateProfileRequest) (ProfileResponse, error) {
	setClauses := []string{}
	args := []interface{}{}
	idx := 1

	if req.FullName != nil {
		setClauses = append(setClauses, fmt.Sprintf("full_name = $%d", idx))
		args = append(args, *req.FullName)
		idx++
	}
	if req.Phone != nil {
		setClauses = append(setClauses, fmt.Sprintf("phone = $%d", idx))
		args = append(args, *req.Phone)
		idx++
	}
	if req.Address != nil {
		setClauses = append(setClauses, fmt.Sprintf("address = $%d", idx))
		args = append(args, *req.Address)
		idx++
	}

	if len(setClauses) == 0 {
		return s.GetProfile(ctx, trackingID)
	}

	setClauses = append(setClauses, "updated_at = NOW()")
	args = append(args, trackingID)

	query := fmt.Sprintf(`UPDATE users SET %s WHERE tracking_id = $%d`, joinStrings(setClauses, ", "), idx)
	res, err := s.db.Exec(ctx, query, args...)
	if err != nil {
		return ProfileResponse{}, fmt.Errorf("failed to update profile: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ProfileResponse{}, errors.New("profile not found")
	}

	return s.GetProfile(ctx, trackingID)
}

// DeviceTokenRequest is the payload for registering an FCM device token
type DeviceTokenRequest struct {
	FCMToken string `json:"fcm_token" binding:"required"`
	Platform string `json:"platform"` // android | ios | web (optional)
}

// RegisterDeviceToken upserts an FCM device token for a user. If the token
// already exists it is reactivated and its platform/updated_at refreshed.
func (s *AuthService) RegisterDeviceToken(ctx context.Context, trackingID string, req DeviceTokenRequest) error {
	ok, err := database.Exists(ctx, s.db, "SELECT 1 FROM users WHERE tracking_id = $1", trackingID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("user %s does not exist", trackingID)
	}

	query := `
		INSERT INTO device_tokens (user_tracking_id, fcm_token, platform, is_active, updated_at)
		VALUES ($1, $2, $3, true, NOW())
		ON CONFLICT (user_tracking_id, fcm_token)
		DO UPDATE SET is_active = true, platform = EXCLUDED.platform, updated_at = NOW()
	`
	_, err = s.db.Exec(ctx, query, trackingID, req.FCMToken, req.Platform)
	if err != nil {
		return fmt.Errorf("failed to register device token: %w", err)
	}
	return nil
}

// joinStrings is a tiny helper to avoid importing strings just for Join.
func joinStrings(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func (s *AuthService) generateAndSaveRefreshToken(ctx context.Context, trackingID string) (string, error) {
	ok, err := database.Exists(ctx, s.db, "SELECT 1 FROM users WHERE tracking_id = $1", trackingID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("user %s does not exist", trackingID)
	}

	token := uuid.New().String()

	// Hash token (SHA-256) for secure DB storage
	hasher := sha256.New()
	hasher.Write([]byte(token))
	tokenHash := fmt.Sprintf("%x", hasher.Sum(nil))

	expiresAt := time.Now().Add(30 * 24 * time.Hour) // 30 days expiry

	// 1. Try inserting with user_id subquery for legacy NOT NULL schemas
	queryWithUserID := `
		INSERT INTO user_refresh_tokens (user_id, user_tracking_id, token_hash, expires_at)
		VALUES ((SELECT id FROM users WHERE tracking_id = $1), $1, $2, $3)
	`
	_, err = s.db.Exec(ctx, queryWithUserID, trackingID, tokenHash, expiresAt)
	if err != nil {
		// 2. Fallback for updated schemas where user_id column is omitted or dropped
		queryWithoutUserID := `
			INSERT INTO user_refresh_tokens (user_tracking_id, token_hash, expires_at)
			VALUES ($1, $2, $3)
		`
		_, err = s.db.Exec(ctx, queryWithoutUserID, trackingID, tokenHash, expiresAt)
		if err != nil {
			return "", fmt.Errorf("failed to save refresh token: %w", err)
		}
	}

	return token, nil
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// LogoutRequest is the payload for revoking a refresh token on logout.
type LogoutRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// Logout revokes the caller's refresh token so it cannot be used again.
// Access tokens are stateless and naturally expire within 1 hour.
func (s *AuthService) Logout(ctx context.Context, trackingID string, req LogoutRequest) error {
	hasher := sha256.New()
	hasher.Write([]byte(req.RefreshToken))
	tokenHash := fmt.Sprintf("%x", hasher.Sum(nil))

	// Revoke the specific refresh token. If it is already revoked or does not
	// exist we still return success — the user is logged out from this client.
	_, err := s.db.Exec(ctx, `
		UPDATE user_refresh_tokens SET revoked = TRUE
		WHERE token_hash = $1 AND user_tracking_id = $2
	`, tokenHash, trackingID)
	if err != nil {
		return fmt.Errorf("failed to revoke refresh token: %w", err)
	}
	return nil
}

func (s *AuthService) Refresh(ctx context.Context, req RefreshRequest) (AuthResponse, error) {
	hasher := sha256.New()
	hasher.Write([]byte(req.RefreshToken))
	tokenHash := fmt.Sprintf("%x", hasher.Sum(nil))

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return AuthResponse{}, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var rawID interface{}
	var userTrackingID string
	var expiresAt time.Time
	var revoked bool

	query := `
		SELECT id, COALESCE(user_tracking_id, ''), expires_at, COALESCE(revoked, false) 
		FROM user_refresh_tokens 
		WHERE token_hash = $1
		FOR UPDATE
	`
	err = tx.QueryRow(ctx, query, tokenHash).Scan(&rawID, &userTrackingID, &expiresAt, &revoked)
	if err != nil {
		return AuthResponse{}, errors.New("UNAUTHORIZED_INVALID_TOKEN: invalid refresh token")
	}

	// RTR Compromise Detection: if token is already revoked, all tokens for the user are invalidated
	if revoked {
		revokeAllQuery := "UPDATE user_refresh_tokens SET revoked = TRUE WHERE user_tracking_id = $1"
		_, _ = tx.Exec(ctx, revokeAllQuery, userTrackingID)
		_ = tx.Commit(ctx)
		return AuthResponse{}, errors.New("FORBIDDEN_TOKEN_COMPROMISED: reuse detected, all user tokens revoked")
	}

	if time.Now().After(expiresAt) {
		return AuthResponse{}, errors.New("UNAUTHORIZED_TOKEN_EXPIRED: refresh token has expired")
	}

	// Revoke old token by token_hash
	revokeQuery := "UPDATE user_refresh_tokens SET revoked = TRUE WHERE token_hash = $1"
	_, err = tx.Exec(ctx, revokeQuery, tokenHash)
	if err != nil {
		return AuthResponse{}, fmt.Errorf("failed to revoke old token: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return AuthResponse{}, fmt.Errorf("failed to commit refresh token transaction: %w", err)
	}

	// Fetch user details for access token generation
	var role string
	var fullName string
	var email string
	var phone *string
	var address *string
	var isVerified bool
	var entityType string

	userQuery := "SELECT role, full_name, email, phone, COALESCE(address, ''), is_verified, COALESCE(entity_type, '') FROM users WHERE tracking_id = $1"
	err = s.db.QueryRow(ctx, userQuery, userTrackingID).Scan(&role, &fullName, &email, &phone, &address, &isVerified, &entityType)
	if err != nil {
		return AuthResponse{}, fmt.Errorf("failed to resolve user for token refresh: %w", err)
	}

	phoneStr := ""
	if phone != nil {
		phoneStr = *phone
	}
	addressStr := ""
	if address != nil {
		addressStr = *address
	}

	// Generate access token (1 hour expiry)
	// MEDIUM-05: a panic inside a request handler kills the whole process.
	// Fail this request (and every auth request) loudly instead.
	jwtSecret := os.Getenv("JWT_SECRET_KEY")
	if jwtSecret == "" {
		return AuthResponse{}, fmt.Errorf("server misconfiguration: signing key unavailable")
	}

	claims := jwt.MapClaims{
		"tracking_id": userTrackingID,
		"role":        role,
		"email":       email,
		"full_name":   fullName,
		"is_verified": isVerified,
		"entity_type": entityType,
		"exp":         time.Now().Add(1 * time.Hour).Unix(),
		"iat":         time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signedToken, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		return AuthResponse{}, fmt.Errorf("failed to sign new JWT: %w", err)
	}

	// Generate rotated refresh token
	newRefreshToken, err := s.generateAndSaveRefreshToken(ctx, userTrackingID)
	if err != nil {
		return AuthResponse{}, fmt.Errorf("failed to generate new refresh token: %w", err)
	}

	return AuthResponse{
		Token:        signedToken,
		RefreshToken: newRefreshToken,
		TrackingID:   userTrackingID,
		Role:         role,
		FullName:     fullName,
		Email:        email,
		Phone:        phoneStr,
		Address:      addressStr,
		IsVerified:   isVerified,
		EntityType:   entityType,
	}, nil
}
