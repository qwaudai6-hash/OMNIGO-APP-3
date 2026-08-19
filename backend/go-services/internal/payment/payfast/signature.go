package payfast

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// generateHMACSHA256 generates a hex-encoded HMAC-SHA256 signature for the concatenated payload.
func generateHMACSHA256(payload string, securedKey string) string {
	mac := hmac.New(sha256.New, []byte(securedKey))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// CalculateValidationHash computes the required secured_hash for POST /customer/validate
// Official rules:
// Card: basket_id + txnamt + card_number + expiry_month + expiry_year + cvv
// Account/Wallet: basket_id + txnamt + account_number + cnic_number
func CalculateValidationHash(req CustomerValidationRequest, securedKey string) string {
	var payload string

	switch req.AccountTypeID {
	case "1": // Card
		payload = req.BasketID + req.TxnAmt + req.CardNumber + req.ExpiryMonth + req.ExpiryYear + req.CVV
	case "2", "3": // Account or Wallet
		payload = req.BasketID + req.TxnAmt + req.AccountNumber + req.CNICNumber
	default:
		// Fallback for unknown account types
		payload = req.BasketID + req.TxnAmt + req.AccountNumber + req.CNICNumber
	}

	return generateHMACSHA256(payload, securedKey)
}

// CalculateTransactionHash computes the required secured_hash for POST /transaction
// Official rules:
// Card: basket_id + txnamt + card_number + expiry_month + expiry_year + cvv + otp
// Account/Wallet: basket_id + txnamt + account_number + cnic_number + otp
// Note: If no OTP is present/required, the "+ otp" part simply adds an empty string.
func CalculateTransactionHash(req InitiateTransactionRequest, otp string, securedKey string) string {
	var payload string

	switch req.AccountTypeID {
	case "1": // Card
		payload = req.BasketID + req.TxnAmt + req.CardNumber + req.ExpiryMonth + req.ExpiryYear + req.CVV + otp
	case "2", "3": // Account or Wallet
		payload = req.BasketID + req.TxnAmt + req.AccountNumber + req.CNICNumber + otp
	default:
		payload = req.BasketID + req.TxnAmt + req.AccountNumber + req.CNICNumber + otp
	}

	return generateHMACSHA256(payload, securedKey)
}

// VerifySignature compares expected and received signatures using constant-time comparison.
func VerifySignature(expected, received string) bool {
	if expected == "" || received == "" {
		return false
	}
	expBytes := []byte(strings.ToLower(strings.TrimSpace(expected)))
	recBytes := []byte(strings.ToLower(strings.TrimSpace(received)))
	if len(expBytes) != len(recBytes) {
		return false
	}
	return subtle.ConstantTimeCompare(expBytes, recBytes) == 1
}

// CalculateTemporaryTokenHash computes the required secured_hash for POST /transaction/token
// Official rules:
// Card: merchant_user_id + user_mobile_number + card_number + expiry_month + expiry_year + cvv
func CalculateTemporaryTokenHash(req TemporaryTokenRequest, securedKey string) string {
	var payload string
	switch req.AccountTypeID {
	case "1": // Card
		payload = req.MerchantUserId + req.CustomerMobileNo + req.CardNumber + req.ExpiryMonth + req.ExpiryYear + req.CVV
	case "2", "3": // Account or Wallet
		payload = req.MerchantUserId + req.CustomerMobileNo + req.AccountNumber + req.CNICNumber
	default:
		payload = req.MerchantUserId + req.CustomerMobileNo + req.CardNumber + req.ExpiryMonth + req.ExpiryYear + req.CVV
	}
	return generateHMACSHA256(payload, securedKey)
}

// CalculateTokenizedTransactionHash computes the required secured_hash for POST /transaction/tokenized
// Official rules:
// instrument_token + merchant_user_id + user_mobile_number + txnamt + otp
func CalculateTokenizedTransactionHash(req TokenizedTransactionRequest, securedKey string) string {
	payload := req.InstrumentToken + req.MerchantUserId + req.CustomerMobileNo + req.TxnAmt + req.Otp
	return generateHMACSHA256(payload, securedKey)
}
