package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"sort"
	"strings"
)

// WalletCallback holds the normalized fields from a JazzCash/EasyPaisa callback.
type WalletCallback struct {
	Gateway       string
	OrderID       string
	TransactionID string
	AmountCents   int64
	Status        string
}

// WalletSalt returns the gateway-specific integrity salt from env vars.
// Falls back to a shared salt so development can keep working without
// per-gateway configuration.
func WalletSalt(gateway string) string {
	if gateway == "jazzcash" {
		if s := os.Getenv("JAZZCASH_SALT"); s != "" {
			return s
		}
	} else if gateway == "easypaisa" {
		if s := os.Getenv("EASYPAISA_SALT"); s != "" {
			return s
		}
	}
	if s := os.Getenv("WALLET_INTEGRITY_SALT"); s != "" {
		return s
	}
	return ""
}

// ComputeWalletSignature generates the HMAC-SHA256 signature for a payment request.
// 1. Sort keys alphabetically.
// 2. Build string: salt&key1=value1&key2=value2...
// 3. HMAC-SHA256 with the salt, uppercase hex.
// Returns "" when the salt is unconfigured — callers must refuse to sign.
func ComputeWalletSignature(params map[string]string, salt string) string {
	if salt == "" {
		return ""
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "pp_SecureHash" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString(salt)
	for _, k := range keys {
		if v := params[k]; v != "" {
			sb.WriteString("\u0026")
			sb.WriteString(k)
			sb.WriteString("=")
			sb.WriteString(v)
		}
	}

	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(sb.String()))
	return strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
}

// VerifyWalletCallback computes the canonical JazzCash/EasyPaisa HMAC:
//  1. Drop pp_SecureHash from the form values.
//  2. Sort remaining keys alphabetically.
//  3. Build string: salt&key1=value1&key2=value2...
//  4. HMAC-SHA256 with the salt, uppercase hex.
//
// An unconfigured (empty) salt makes the HMAC trivially forgeable by anyone,
// so verification always fails closed.
func VerifyWalletCallback(form url.Values, salt, providedHash string) bool {
	if salt == "" {
		return false
	}
	keys := make([]string, 0, len(form))
	for k := range form {
		if k == "pp_SecureHash" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString(salt)
	for _, k := range keys {
		if v := form.Get(k); v != "" {
			sb.WriteString("\u0026")
			sb.WriteString(k)
			sb.WriteString("=")
			sb.WriteString(v)
		}
	}

	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(sb.String()))
	expectedHash := strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
	return hmac.Equal([]byte(expectedHash), []byte(strings.ToUpper(providedHash)))
}
