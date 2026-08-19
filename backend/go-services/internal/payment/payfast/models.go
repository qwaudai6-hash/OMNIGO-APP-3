package payfast

import (
	"fmt"
	"time"
)

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
type AuthTokenResponse struct {
	Code         string `json:"code,omitempty"`
	Token        string `json:"token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    string `json:"expiry,omitempty"` // Wait, docs say "expiry": "<no.ofseconds>"
	Message      string `json:"message,omitempty"`
}

// TokenCache holds in-memory cached OAuth/Auth tokens with thread-safe expiration checks.
type TokenCache struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// AccountType represents the type of payment instrument.
type AccountType int

const (
	AccountTypeCard   AccountType = 1
	AccountTypeBank   AccountType = 2
	AccountTypeWallet AccountType = 3
)

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
	Code                         string `json:"code"`
	Message                      string `json:"message"`
	TransactionID                string `json:"transaction_id"`
	Data3DSAcsURL                string `json:"data_3ds_acsurl"`
	Data3DSPaReq                 string `json:"data_3ds_pareq"`
	Data3DSHTML                  string `json:"data_3ds_html"`
	Data3DSSecureID              string `json:"data_3ds_secureid"`
	Data3DSGatewayRecommendation string `json:"data_3ds_gatewayrecommendation"`
	ECI                          string `json:"eci"`
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

	// Card specific (never persist)
	CardNumber         string `json:"card_number,omitempty"`
	ExpiryMonth        string `json:"expiry_month,omitempty"`
	ExpiryYear         string `json:"expiry_year,omitempty"`
	CVV                string `json:"cvv,omitempty"`
	Data3DSPagemode    string `json:"data_3ds_pagemode,omitempty"`
	Data3DSCallbackURL string `json:"data_3ds_callback_url,omitempty"`

	// Bank/Wallet specific (never persist)
	AccountNumber      string `json:"account_number,omitempty"`
	CNICNumber         string `json:"cnic_number,omitempty"`
}

func (t TemporaryTokenRequest) String() string {
	return fmt.Sprintf("TemporaryTokenRequest{BasketID:%s, TxnAmt:%s, CardNumber:[REDACTED], CVV:[REDACTED]}", t.BasketID, t.TxnAmt)
}

// TemporaryTokenResponse is returned by POST /transaction/token
type TemporaryTokenResponse struct {
	StatusCode                   string `json:"status_code"`
	StatusMsg                    string `json:"status_msg"`
	InstrumentAlias              string `json:"instrument_alias"`
	InstrumentToken              string `json:"instrument_token"`
	TransactionID                string `json:"transaction_id"`
	OtpRequired                  string `json:"otp_required"`
	ECI                          string `json:"eci"`
	Data3DSAcsURL                string `json:"data_3ds_acsurl"`
	Data3DSPaReq                 string `json:"data_3ds_pareq"`
	Data3DSHTML                  string `json:"data_3ds_html"`
	Data3DSSecureID              string `json:"data_3ds_secureid"`
	Data3DSGatewayRecommendation string `json:"data_3ds_gatewayrecommendation"`
}

// TokenizedTransactionRequest is for POST /transaction/tokenized
type TokenizedTransactionRequest struct {
	InstrumentToken  string `json:"instrument_token"`
	TransactionID    string `json:"transaction_id"`
	MerchantUserId   string `json:"merchant_user_id"`
	CustomerMobileNo string `json:"user_mobile_number"`
	BasketID         string `json:"basket_id"`
	OrderDate        string `json:"order_date"`
	TxnDesc          string `json:"txndesc"`
	TxnAmt           string `json:"txnamt"`
	Otp              string `json:"otp,omitempty"`
	CustomerIP       string `json:"customer_ip"`
	MerCatCode       string `json:"merCatCode"`
	ECI              string `json:"eci,omitempty"`
	Data3DSSecureID  string `json:"data_3ds_secureid,omitempty"`
	Data3DSPaRes     string `json:"data_3ds_pares,omitempty"`
	SecuredHash      string `json:"secured_hash"`
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
}
