package auth

import (
	"fmt"
	"os"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// ParseJWT extracts the tracking ID from a signed JWT token.
// Requires JWT_SECRET_KEY env var — panics if missing (prevents silent fallback to known secret).
func ParseJWT(tokenString string) (trackingID string, role string, err error) {
	jwtSecret := os.Getenv("JWT_SECRET_KEY")
	if jwtSecret == "" {
		// PANIC: Never allow fallback to a known secret in any environment.
		// This forces operators to set JWT_SECRET_KEY before the service starts.
		panic("FATAL: JWT_SECRET_KEY environment variable is not set. Refusing to start with insecure fallback.")
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})

	if err != nil || !token.Valid {
		return "", "", fmt.Errorf("invalid token: %v", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", "", fmt.Errorf("invalid token claims")
	}

	tid, _ := claims["tracking_id"].(string)
	rl, _ := claims["role"].(string)
	if tid == "" {
		return "", "", fmt.Errorf("invalid token: no tracking_id claim found")
	}

	return tid, rl, nil
}

// ExtractTrackingIDFromHeader parses the Authorization header (Bearer token)
// and returns the tracking ID using ParseJWT.
func ExtractTrackingIDFromHeader(authHeader string) (string, error) {
	if authHeader == "" {
		return "", fmt.Errorf("Authorization header is required")
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	tid, _, err := ParseJWT(token)
	return tid, err
}

// ParseJWTFromHeader parses the Authorization header and returns both the
// tracking ID and role claim. It validates the Bearer scheme.
func ParseJWTFromHeader(authHeader string) (string, string, error) {
	if authHeader == "" {
		return "", "", fmt.Errorf("Authorization header is required")
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == authHeader {
		return "", "", fmt.Errorf("Authorization header must use Bearer scheme")
	}
	return ParseJWT(token)
}
