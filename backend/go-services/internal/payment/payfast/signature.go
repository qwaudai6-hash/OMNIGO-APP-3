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
	if req.CardNumber != "" {
		payload = req.BasketID + req.TxnAmt + req.CardNumber + req.ExpiryMonth + req.ExpiryYear + req.CVV
	} else {
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
	if req.CardNumber != "" {
		payload = req.BasketID + req.TxnAmt + req.CardNumber + req.ExpiryMonth + req.ExpiryYear + req.CVV + otp
	} else {
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

// CalculateResponseValidationHash computes PayFast's RESPONSE-integrity hash — distinct from the
// "secured_hash" this client sends on REQUESTS (see CalculateValidationHash/CalculateTransactionHash/
// CalculateTemporaryTokenHash/CalculateTokenizedTransactionHash above, which are HMAC-SHA256).
//
// Per PayFast's official spec, this one is:
//   - Plain SHA256 (NOT HMAC) of a pipe-delimited string
//   - Format: basketID + "|" + securedKey + "|" + merchantID + "|" + payfastErrCode
//   - The secured key is embedded directly IN the hashed string, not used as an HMAC key
//
// Verified against PayFast's worked example:
//
//	CalculateResponseValidationHash("BAS-01", "jdnkaabcks", "102", "000")
//	  == "e8192a7554dd699975adf39619c703a492392edf5e416a61e183866ecdf6a2a2"
//
// NOTE: which securedKey PayFast means here (the OAuth SECURED_KEY vs. a separate hash-only key)
// is still unconfirmed — see the open question to PayFast's integration team. Do not assume;
// pass whichever key value has been confirmed once support responds.
//
// NOTE 2: which response field(s)/endpoint(s) actually return this hash for verification, and
// under what JSON key name, is also not yet confirmed. This function is provided so the wiring
// can be completed the moment that's confirmed — see VerifyResponseHash below for the comparison
// step once the field mapping is known.
func CalculateResponseValidationHash(basketID, securedKey, merchantID, payfastErrCode string) string {
	payload := basketID + "|" + securedKey + "|" + merchantID + "|" + payfastErrCode
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// VerifyResponseHash compares a locally-recomputed CalculateResponseValidationHash result against
// the hash PayFast returned, using constant-time comparison (SHA256 hex digests, so case-insensitive
// compare is safe and matches VerifySignature's convention).
func VerifyResponseHash(expected, receivedFromPayFast string) bool {
	return VerifySignature(expected, receivedFromPayFast)
}

// CalculateTemporaryTokenHash computes the required secured_hash for POST /transaction/token
// Official rules:
// Card: merchant_user_id + user_mobile_number + card_number + expiry_month + expiry_year + cvv
// Account/Wallet: merchant_user_id + user_mobile_number + account_number + cnic_number
func CalculateTemporaryTokenHash(req TemporaryTokenRequest, securedKey string) string {
	var payload string
	if req.CardNumber != "" {
		payload = req.MerchantUserId + req.CustomerMobileNo + req.CardNumber + req.ExpiryMonth + req.ExpiryYear + req.CVV
	} else {
		payload = req.MerchantUserId + req.CustomerMobileNo + req.AccountNumber + req.CNICNumber
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
