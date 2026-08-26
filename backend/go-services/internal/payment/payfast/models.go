package payfast

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FlexibleBool unmarshals both JSON boolean (true/false) and string ("true"/"false"/"1"/"0").
type FlexibleBool bool

func (b *FlexibleBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), "\"")
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "true", "1", "t", "yes", "y":
		*b = true
		return nil
	case "false", "0", "f", "no", "n", "", "null":
		*b = false
		return nil
	default:
		var rawBool bool
		if err := json.Unmarshal(data, &rawBool); err == nil {
			*b = FlexibleBool(rawBool)
			return nil
		}
		*b = false
		return nil
	}
}

func (b FlexibleBool) Bool() bool {
	return bool(b)
}

// FlexibleString unmarshals JSON strings, numbers, and booleans into a normalized string representation.
type FlexibleString string

func (fs *FlexibleString) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*fs = FlexibleString(s)
		return nil
	}
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		*fs = FlexibleString(strconv.FormatBool(b))
		return nil
	}
	trimmed := strings.Trim(string(data), "\"")
	*fs = FlexibleString(trimmed)
	return nil
}

func (fs FlexibleString) String() string {
	return string(fs)
}

// Environment specifies Sandbox vs Production gateway endpoints.
type Environment string

const (
	EnvSandbox    Environment = "sandbox"
	EnvProduction Environment = "production"
)

// AuthTokenRequest represents the request to get a merchant access token.
type AuthTokenRequest struct {
	MerchantID string
	SecuredKey string
	GrantType  string
	CustomerIP string
}

// AuthTokenResponse represents the authentication response from PayFast API token endpoint.
// Supports standard IPG ("token", "refresh_token", "expiry") and APPS UAT ("ACCESS_TOKEN", "access_token").
type AuthTokenResponse struct {
	Code             string `json:"code,omitempty"`
	Token            string `json:"token,omitempty"`
	AccessToken      string `json:"access_token,omitempty"`
	AccessTokenUpper string `json:"ACCESS_TOKEN,omitempty"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	ExpiresIn        string `json:"expiry,omitempty"`
	Message          string `json:"message,omitempty"`
}

// GetToken returns the resolved access token regardless of parameter casing.
func (r *AuthTokenResponse) GetToken() string {
	if r.Token != "" {
		return r.Token
	}
	if r.AccessToken != "" {
		return r.AccessToken
	}
	return r.AccessTokenUpper
}

// IsSuccessCode returns true if the status or error code represents a successful transaction.
// PayFast APPS uses both "00" (APIs) and "000" (IPN callbacks & transaction inquiries).
func IsSuccessCode(code string) bool {
	c := strings.TrimSpace(code)
	return c == "00" || c == "000"
}

// TokenCache holds in-memory cached OAuth/Auth tokens with thread-safe expiration checks.
type TokenCache struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// NOTE: PayFast account_type / account_type_id values vary by integration endpoint
// and issuer. Do NOT hardcode assumptions about which numeric ID maps to card vs bank
// vs wallet. Use PayFast's /list/instruments API to retrieve the correct mapping for
// your merchant configuration. The values in PayFast's own documentation examples
// (e.g., account_type_id=2 for card, =3 for bank) differ from naive 1/2/3 assumptions.

// CustomerValidationRequest contains all fields needed for POST /customer/validate
type CustomerValidationRequest struct {
	BasketID             string
	TxnAmt               string
	OrderDate            string // YYYY-MM-DD HH:mm:ss
	CustomerMobileNo     string
	CustomerEmailAddress string
	AccountTypeID        string
	MerCatCode           string
	CustomerIP           string

	// Card specific
	CardNumber         string `json:"-"`
	ExpiryMonth        string `json:"-"`
	ExpiryYear         string `json:"-"`
	CVV                string `json:"-"`
	Data3DSPagemode    string
	Data3DSCallbackURL string

	// Bank/Wallet specific
	BankCode      string `json:"-"`
	AccountNumber string `json:"-"`
	AccountTitle  string `json:"-"`
	CNICNumber    string `json:"-"`
}

// String explicitly masks sensitive fields so they never leak in fmt.Print or log operations.
func (c CustomerValidationRequest) String() string {
	return fmt.Sprintf("CustomerValidationRequest{BasketID:%s, TxnAmt:%s, OrderDate:%s, AccountTypeID:%s, CardNumber:[REDACTED], CVV:[REDACTED], AccountNumber:[REDACTED]}",
		c.BasketID, c.TxnAmt, c.OrderDate, c.AccountTypeID)
}

// CustomerValidationResponse is returned by POST /customer/validate
type CustomerValidationResponse struct {
	Code                         string         `json:"code"`
	Message                      string         `json:"message"`
	TransactionID                string         `json:"transaction_id"`
	Data3DSAcsURL                string         `json:"data_3ds_acsurl"`
	Data3DSPaReq                 string         `json:"data_3ds_pareq"`
	Data3DSHTML                  string         `json:"data_3ds_html"`
	Data3DSSecureID              string         `json:"data_3ds_secureid"`
	Data3DSGatewayRecommendation string         `json:"data_3ds_gatewayrecommendation"`
	ECI                          FlexibleString `json:"eci"`
}

// InitiateTransactionRequest contains fields for POST /transaction
type InitiateTransactionRequest struct {
	BasketID             string
	TxnAmt               string
	OrderDate            string
	CustomerMobileNo     string
	CustomerEmailAddress string
	AccountTypeID        string
	MerCatCode           string
	CustomerIP           string

	// From validation response
	TransactionID string
	ECI           string

	// Card specific
	CardNumber      string `json:"-"`
	ExpiryMonth     string `json:"-"`
	ExpiryYear      string `json:"-"`
	CVV             string `json:"-"`
	Data3DSSecureID string
	Data3DSPaRes    string

	// Bank/Wallet specific
	BankCode      string `json:"-"`
	AccountNumber string `json:"-"`
	AccountTitle  string `json:"-"`
	CNICNumber    string `json:"-"`
}

// String explicitly masks sensitive fields.
func (i InitiateTransactionRequest) String() string {
	return fmt.Sprintf("InitiateTransactionRequest{BasketID:%s, TxnAmt:%s, TransactionID:%s, CardNumber:[REDACTED], CVV:[REDACTED], AccountNumber:[REDACTED]}",
		i.BasketID, i.TxnAmt, i.TransactionID)
}

// InitiateTransactionResponse is returned by POST /transaction
type InitiateTransactionResponse struct {
	StatusCode    string `json:"status_code"`
	StatusMsg     string `json:"status_msg"`
	RdvMessageKey string `json:"rdv_message_key"`
	BasketID      string `json:"basket_id"`
	TransactionID string `json:"transaction_id"`
	Code          string `json:"code"` // Sometimes "00"
}

// TransactionStatusResponse is returned by GET /transaction/<transaction_id>
type TransactionStatusResponse struct {
	StatusCode    string `json:"status_code"`
	StatusMsg     string `json:"status_msg"`
	RdvMessageKey string `json:"rdv_message_key"`
	BasketID      string `json:"basket_id"`
	TransactionID string `json:"transaction_id"`
	Code          string `json:"code"`
	TxnAmt        string `json:"txnamt,omitempty"`
}

// TemporaryTokenRequest is for POST /transaction/token
type TemporaryTokenRequest struct {
	BasketID         string `json:"basket_id"`
	TxnAmt           string `json:"txnamt"`
	OrderDate        string `json:"order_date"` // YYYY-MM-DD HH:mm:ss
	CustomerMobileNo string `json:"user_mobile_number"`
	MerchantUserId   string `json:"merchant_user_id"`
	AccountTypeID    string `json:"account_type"`
	MerCatCode       string `json:"merCatCode"`
	CustomerIP       string `json:"customer_ip"`
	SecuredHash      string `json:"secured_hash"`

	// Card specific (never persist, never serialize — json:"-" keeps PAN/CVV out of
	// any accidental json.Marshal in debug/logging paths)
	CardNumber         string `json:"-"`
	ExpiryMonth        string `json:"-"`
	ExpiryYear         string `json:"-"`
	CVV                string `json:"-"`
	Data3DSPagemode    string `json:"data_3ds_pagemode,omitempty"`
	Data3DSCallbackURL string `json:"data_3ds_callback_url,omitempty"`

	// Bank/Wallet specific (never persist, never serialize)
	AccountNumber string `json:"-"`
	CNICNumber    string `json:"-"`
}

func (t TemporaryTokenRequest) String() string {
	return fmt.Sprintf("TemporaryTokenRequest{BasketID:%s, TxnAmt:%s, CardNumber:[REDACTED], CVV:[REDACTED]}", t.BasketID, t.TxnAmt)
}

// TemporaryTokenResponse is returned by POST /transaction/token
type TemporaryTokenResponse struct {
	StatusCode                   string         `json:"status_code"`
	StatusMsg                    string         `json:"status_msg"`
	InstrumentAlias              string         `json:"instrument_alias"`
	InstrumentToken              string         `json:"instrument_token"`
	TransactionID                string         `json:"transaction_id"`
	OtpRequired                  FlexibleBool   `json:"otp_required"`
	ECI                          FlexibleString `json:"eci"`
	Data3DSAcsURL                string         `json:"data_3ds_acsurl"`
	Data3DSPaReq                 string         `json:"data_3ds_pareq"`
	Data3DSHTML                  string         `json:"data_3ds_html"`
	Data3DSSecureID              string         `json:"data_3ds_secureid"`
	Data3DSGatewayRecommendation string         `json:"data_3ds_gatewayrecommendation"`
}

// TokenizedTransactionRequest is for POST /transaction/tokenized
type TokenizedTransactionRequest struct {
	// InstrumentToken and Otp are credentials: json:"-" keeps them out of any
	// accidental json.Marshal (logs, error wrappers). The API client sends them
	// via form-encoding, not JSON, so this has no wire-format impact.
	InstrumentToken  string `json:"-"`
	TransactionID    string `json:"transaction_id"`
	MerchantUserId   string `json:"merchant_user_id"`
	CustomerMobileNo string `json:"user_mobile_number"`
	BasketID         string `json:"basket_id"`
	OrderDate        string `json:"order_date"`
	TxnDesc          string `json:"txndesc"`
	TxnAmt           string `json:"txnamt"`
	Otp              string `json:"-"`
	CustomerIP       string `json:"customer_ip"`
	MerCatCode       string `json:"merCatCode"`
	ECI              string `json:"eci,omitempty"`
	Data3DSSecureID  string `json:"data_3ds_secureid,omitempty"`
	Data3DSPaRes     string `json:"data_3ds_pares,omitempty"`
	SecuredHash      string `json:"-"`
}

func (t TokenizedTransactionRequest) String() string {
	return fmt.Sprintf("TokenizedTransactionRequest{BasketID:%s, TransactionID:%s, InstrumentToken:[REDACTED]}", t.BasketID, t.TransactionID)
}

// TokenizedTransactionResponse is returned by POST /transaction/tokenized
type TokenizedTransactionResponse struct {
	StatusCode    string `json:"status_code"`
	StatusMsg     string `json:"status_msg"`
	RdvMessageKey string `json:"rdv_message_key"`
	BasketID      string `json:"basket_id"`
	TransactionID string `json:"transaction_id"`
	Code          string `json:"code"`

	// 3DS step-up challenge fields. Some issuer/bank combinations demand a 3DS
	// verification even on tokenized (saved-card) transactions; when present, the
	// merchant must render the challenge and resume via the 3DS callback instead of
	// treating the transaction as settled or declined.
	OtpRequired                  FlexibleBool   `json:"otp_required"`
	ECI                          FlexibleString `json:"eci"`
	Data3DSAcsURL                string         `json:"data_3ds_acsurl"`
	Data3DSPaReq                 string         `json:"data_3ds_pareq"`
	Data3DSHTML                  string         `json:"data_3ds_html"`
	Data3DSSecureID              string         `json:"data_3ds_secureid"`
	Data3DSGatewayRecommendation string         `json:"data_3ds_gatewayrecommendation"`
}
