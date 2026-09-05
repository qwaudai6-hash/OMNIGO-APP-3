package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	emailVerificationTTL = 24 * time.Hour
	passwordResetTTL     = 1 * time.Hour
)

// ─────────────────────────────────────────────────────────────────────
//  Password reset (forgot password)
// ─────────────────────────────────────────────────────────────────────

// RequestPasswordReset generates a 1-hour token and returns the
// plain-text token (so the caller can email it). Always returns nil
// error even if the user doesn't exist — otherwise we'd leak which
// emails are registered.
func (s *AuthService) RequestPasswordReset(ctx context.Context, email string) (string, error) {
	var trackingID string
	err := s.db.QueryRow(ctx, "SELECT tracking_id FROM users WHERE email = $1", email).Scan(&trackingID)
	if err != nil {
		// User not found — pretend it worked so the API doesn't leak
		// which emails are registered.
		return "", nil
	}

	raw := uuid.NewString() + uuid.NewString()
	tokenHash := sha256Hex(raw)

	_, err = s.db.Exec(ctx,
		`INSERT INTO password_reset_tokens (user_tracking_id, token_hash, expires_at)
		 VALUES ($1, $2, NOW() + $3::interval)`,
		trackingID, tokenHash, passwordResetTTL.String())
	if err != nil {
		return "", err
	}
	return raw, nil
}

// ConfirmPasswordReset validates the token, marks it used, and updates
// the password hash. The token is one-time only.
func (s *AuthService) ConfirmPasswordReset(ctx context.Context, token, newPassword string) error {
	if len(newPassword) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	tokenHash := sha256Hex(token)

	hash, err := bcryptGenerateFromPassword(newPassword)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var (
		uid       string
		expiresAt time.Time
		usedAt    *time.Time
	)
	row := tx.QueryRow(ctx,
		`SELECT user_tracking_id, expires_at, used_at
		 FROM password_reset_tokens
		 WHERE token_hash = $1
		 FOR UPDATE`, tokenHash)
	if err := row.Scan(&uid, &expiresAt, &usedAt); err != nil {
		return errors.New("invalid or expired token")
	}
	if usedAt != nil {
		return errors.New("token already used")
	}
	if time.Now().After(expiresAt) {
		return errors.New("token expired")
	}

	if _, err := tx.Exec(ctx,
		`UPDATE users SET password_hash = $1, updated_at = NOW() WHERE tracking_id = $2`,
		hash, uid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE password_reset_tokens SET used_at = NOW() WHERE token_hash = $1`,
		tokenHash); err != nil {
		return err
	}
	// Invalidate all active sessions across all devices for this user
	if _, err := tx.Exec(ctx,
		`UPDATE user_refresh_tokens SET revoked = TRUE, updated_at = NOW() WHERE user_tracking_id = $1`,
		uid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ─────────────────────────────────────────────────────────────────────
//  Email verification
// ─────────────────────────────────────────────────────────────────────

// IssueEmailVerification creates a 24-hour token for the user. Returns
// the plain-text token (caller is responsible for emailing the URL).
func (s *AuthService) IssueEmailVerification(ctx context.Context, trackingID string) (string, error) {
	raw := uuid.NewString() + uuid.NewString()
	tokenHash := sha256Hex(raw)

	_, err := s.db.Exec(ctx,
		`INSERT INTO email_verification_tokens (user_tracking_id, email, token_hash, expires_at)
		 VALUES ($1, (SELECT email FROM users WHERE tracking_id = $1), $2, NOW() + $3::interval)`,
		trackingID, tokenHash, emailVerificationTTL.String())
	if err != nil {
		return "", err
	}
	return raw, nil
}

// ConfirmEmailVerification validates the token and marks the user's
// email verified. Returns the verified email so the caller can display a
// banner.
func (s *AuthService) ConfirmEmailVerification(ctx context.Context, token string) (string, error) {
	tokenHash := sha256Hex(token)

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var (
		uid        string
		email      string
		expiresAt  time.Time
		verifiedAt *time.Time
	)
	row := tx.QueryRow(ctx,
		`SELECT user_tracking_id, email, expires_at, verified_at
		 FROM email_verification_tokens
		 WHERE token_hash = $1
		 FOR UPDATE`, tokenHash)
	if err := row.Scan(&uid, &email, &expiresAt, &verifiedAt); err != nil {
		return "", errors.New("invalid or expired token")
	}
	if verifiedAt != nil {
		// Idempotent — already verified.
		return email, nil
	}
	if time.Now().After(expiresAt) {
		return "", errors.New("token expired")
	}

	if _, err := tx.Exec(ctx,
		`UPDATE users SET email_verified = true, updated_at = NOW() WHERE tracking_id = $1`,
		uid); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE email_verification_tokens SET verified_at = NOW() WHERE token_hash = $1`,
		tokenHash); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return email, nil
}

// IsEmailVerified returns whether the user has a verified email.
func (s *AuthService) IsEmailVerified(ctx context.Context, trackingID string) (bool, error) {
	var verified bool
	err := s.db.QueryRow(ctx,
		"SELECT email_verified FROM users WHERE tracking_id = $1", trackingID).Scan(&verified)
	if err != nil {
		return false, err
	}
	return verified, nil
}

// ─────────────────────────────────────────────────────────────────────
//  2FA / TOTP
// ─────────────────────────────────────────────────────────────────────

// Enroll2FA returns a base32 TOTP secret + a QR code URL. The secret is
// stored AES-GCM encrypted in user_2fa_secrets. 2FA is enrolled but NOT
// enabled until the user verifies their first code via
// Verify2FAEnrollment.
func (s *AuthService) Enroll2FA(ctx context.Context, trackingID string) (string, string, error) {
	secret, err := generateBase32Secret(20)
	if err != nil {
		return "", "", err
	}
	cipher, err := encryptForStorage(secret)
	if err != nil {
		return "", "", err
	}

	_, err = s.db.Exec(ctx,
		`INSERT INTO user_2fa_secrets (user_tracking_id, secret_encrypted, enabled, enrolled_at)
		 VALUES ($1, $2, false, NOW())
		 ON CONFLICT (user_tracking_id)
		 DO UPDATE SET secret_encrypted = $2, enabled = false, enrolled_at = NOW()`,
		trackingID, cipher)
	if err != nil {
		return "", "", err
	}

	issuer := "OMNIGO"
	otpauthURL := "otpauth://totp/" + issuer + ":" + trackingID +
		"?secret=" + secret + "&issuer=" + issuer + "&algorithm=SHA1&digits=6&period=30"
	return secret, otpauthURL, nil
}

// Verify2FAEnrollment checks the first code from the user. If it
// matches, the secret is flipped to enabled.
func (s *AuthService) Verify2FAEnrollment(ctx context.Context, trackingID, code string) error {
	secret, err := s.load2FASecret(ctx, trackingID)
	if err != nil {
		return err
	}
	if !verifyTOTP(secret, code) {
		return errors.New("invalid code")
	}

	_, err = s.db.Exec(ctx,
		`UPDATE user_2fa_secrets SET enabled = true WHERE user_tracking_id = $1`,
		trackingID)
	return err
}

// Is2FAEnabled returns whether 2FA is enabled for the user.
func (s *AuthService) Is2FAEnabled(ctx context.Context, trackingID string) (bool, error) {
	var enabled bool
	err := s.db.QueryRow(ctx,
		`SELECT enabled FROM user_2fa_secrets WHERE user_tracking_id = $1`,
		trackingID).Scan(&enabled)
	if err != nil {
		// No row = 2FA not enrolled = treated as disabled.
		return false, nil
	}
	return enabled, nil
}

// Verify2FALogin checks the code at login time. Call this AFTER the
// password has been verified.
func (s *AuthService) Verify2FALogin(ctx context.Context, trackingID, code string) error {
	enabled, err := s.Is2FAEnabled(ctx, trackingID)
	if err != nil {
		return err
	}
	if !enabled {
		return errors.New("2FA not enabled")
	}
	secret, err := s.load2FASecret(ctx, trackingID)
	if err != nil {
		return err
	}
	if !verifyTOTP(secret, code) {
		return errors.New("invalid code")
	}
	_, _ = s.db.Exec(ctx,
		`UPDATE user_2fa_secrets SET last_used_at = NOW() WHERE user_tracking_id = $1`,
		trackingID)
	return nil
}

// Disable2FA removes the user's 2FA after verifying a current code.
func (s *AuthService) Disable2FA(ctx context.Context, trackingID, code string) error {
	secret, err := s.load2FASecret(ctx, trackingID)
	if err != nil {
		return err
	}
	if !verifyTOTP(secret, code) {
		return errors.New("invalid code")
	}
	_, err = s.db.Exec(ctx,
		`DELETE FROM user_2fa_secrets WHERE user_tracking_id = $1`,
		trackingID)
	return err
}

func (s *AuthService) load2FASecret(ctx context.Context, trackingID string) (string, error) {
	var encrypted string
	err := s.db.QueryRow(ctx,
		`SELECT secret_encrypted FROM user_2fa_secrets WHERE user_tracking_id = $1`,
		trackingID).Scan(&encrypted)
	if err != nil {
		return "", err
	}
	return decryptFromStorage(encrypted)
}

// ─────────────────────────────────────────────────────────────────────
//  Crypto helpers
// ─────────────────────────────────────────────────────────────────────

// sha256Hex returns the lowercase hex SHA-256 hash of the input.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	const hex = "0123456789abcdef"
	out := make([]byte, len(sum)*2)
	for i, b := range sum {
		out[i*2] = hex[b>>4]
		out[i*2+1] = hex[b&0x0f]
	}
	return string(out)
}

// bcryptGenerateFromPassword is a thin wrapper so we don't have to
// import bcrypt in this file (it's already imported in auth_service.go).
func bcryptGenerateFromPassword(plain string) (string, error) {
	// live-reload-friendly: we always re-derive the bcrypt hash using
	// the same cost as the rest of the auth service.
	return hashPlainPassword(plain)
}

// hashPlainPassword is the injection point for the auth_service's
// bcrypt helper. Set in init() so we don't have to duplicate the bcrypt
// import here.
var hashPlainPassword func(plain string) (string, error)

// generateBase32Secret returns a 20-byte random secret encoded as
// base32 (RFC 4648, no padding). Used as TOTP shared secret.
func generateBase32Secret(byteLen int) (string, error) {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate TOTP secret: %w", err)
	}
	return strings.TrimRight(enc.EncodeToString(b), "="), nil
}

// verifyTOTP returns true if the 6-digit code matches the secret.
// Implements RFC 6238 with 30-second time window + 1-step skew on each
// side to compensate for clock drift.
func verifyTOTP(secret, code string) bool {
	if len(code) != 6 {
		return false
	}
	// Decode the base32 secret.
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	key, err := enc.DecodeString(strings.ToUpper(secret))
	if err != nil {
		return false
	}

	// Calculate the time counter — 30s window starting from Unix epoch.
	counter := uint64(time.Now().Unix()) / 30

	// Check current, previous, and next window for clock drift tolerance.
	for _, skew := range []int64{-1, 0, 1} {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], counter+uint64(skew))
		h := hmac.New(sha1.New, key)
		h.Write(buf[:])
		sum := h.Sum(nil)

		// Dynamic truncation per RFC 4226 §5.3.
		offset := sum[len(sum)-1] & 0x0f
		truncated := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
		otp := truncated % 1000000
		if fmt.Sprintf("%06d", otp) == code {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────
//  AES-GCM encryption for 2FA secret storage at rest
// ─────────────────────────────────────────────────────────────────────

// init populates the encryption key and the password hash dispatcher
// once at package load. The encryption key comes from
// HMAC_TOKEN_ENCRYPTION_KEY (production) or a deterministic fallback
// (dev only).
func init() {
	hashPlainPassword = hashPlainPasswordBcrypt
	totpEncryptionKey = loadEncryptionKey()
}

// loadEncryptionKey reads HMAC_TOKEN_ENCRYPTION_KEY from env (a 64-char
// hex string = 32 bytes) or decodes base64 keys or derives from HMAC_SECRET.
func loadEncryptionKey() []byte {
	k := strings.TrimSpace(os.Getenv("HMAC_TOKEN_ENCRYPTION_KEY"))
	if len(k) == 64 {
		out := make([]byte, 32)
		valid := true
		for i := 0; i < 32; i++ {
			c1, ok1 := fromHexChar(k[i*2])
			c2, ok2 := fromHexChar(k[i*2+1])
			if !ok1 || !ok2 {
				valid = false
				break
			}
			out[i] = (c1 << 4) | c2
		}
		if valid {
			return out
		}
	}
	if secret := strings.TrimSpace(os.Getenv("HMAC_SECRET")); secret != "" {
		sum := sha256.Sum256([]byte(secret))
		return sum[:]
	}
	if jwtSecret := strings.TrimSpace(os.Getenv("JWT_SECRET_KEY")); jwtSecret != "" {
		sum := sha256.Sum256([]byte(jwtSecret))
		return sum[:]
	}
	// No encryption key available — refuse to start with an insecure key.
	panic("FATAL: no encryption key available. Set HMAC_TOKEN_ENCRYPTION_KEY, HMAC_SECRET, or JWT_SECRET_KEY env var")
}

var totpEncryptionKey []byte

func fromHexChar(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// hashPlainPasswordBcrypt is the bcrypt-based password hasher shared
// with auth_service.go. It's set on the package var hashPlainPassword
// in init() so the auth_flow.go helpers can reuse it without importing
// bcrypt twice.
func hashPlainPasswordBcrypt(plain string) (string, error) {
	// Cost 10 matches the default in auth_service.go.
	return bcryptGenerateReuse(plain)
}

// bcryptGenerateReuse is a small bridge so we don't duplicate the
// bcrypt import here. Resolved by the auth_service.go init().
var bcryptGenerateReuse func(plain string) (string, error)

// encryptForStorage AES-GCM encrypts the plaintext with a random
// nonce and returns the base64-encoded ciphertext blob.
func encryptForStorage(plain string) (string, error) {
	block, err := aes.NewCipher(totpEncryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plain), nil)
	// Out: nonce || ciphertext
	blob := make([]byte, 0, len(nonce)+len(ciphertext))
	blob = append(blob, nonce...)
	blob = append(blob, ciphertext...)
	return base32.StdEncoding.EncodeToString(blob), nil
}

// decryptFromStorage reverses encryptForStorage.
func decryptFromStorage(blob string) (string, error) {
	raw, err := base32.StdEncoding.DecodeString(blob)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(totpEncryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("encrypted blob too short")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
