package payfast

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ValidateCustomerPayment calls POST /customer/validate
func (c *Client) ValidateCustomerPayment(ctx context.Context, req CustomerValidationRequest) (*CustomerValidationResponse, error) {
	token, err := c.GetAuthToken(ctx, req.CustomerIP)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := c.baseURL + "/customer/validate"
	formData := url.Values{}

	// Common Parameters
	formData.Set("basket_id", req.BasketID)
	formData.Set("txnamt", req.TxnAmt)
	formData.Set("order_date", req.OrderDate)
	formData.Set("customer_mobile_no", req.CustomerMobileNo)
	formData.Set("customer_email_address", req.CustomerEmailAddress)
	formData.Set("account_type_id", req.AccountTypeID)
	formData.Set("merCatCode", req.MerCatCode)
	formData.Set("customer_ip", req.CustomerIP)

	// Calculate and set secured_hash
	securedHash := CalculateValidationHash(req, c.securedKey)
	formData.Set("secured_hash", securedHash)

	// Instrument-specific parameters
	if req.CardNumber != "" {
		formData.Set("card_number", req.CardNumber)
		formData.Set("expiry_month", req.ExpiryMonth)
		formData.Set("expiry_year", req.ExpiryYear)
		formData.Set("cvv", req.CVV)
		if req.Data3DSPagemode != "" {
			formData.Set("data_3ds_pagemode", req.Data3DSPagemode)
		}
		if req.Data3DSCallbackURL != "" {
			formData.Set("data_3ds_callback_url", req.Data3DSCallbackURL)
		}
	} else if req.AccountNumber != "" {
		formData.Set("bank_code", req.BankCode)
		formData.Set("account_number", req.AccountNumber)
		formData.Set("cnic_number", req.CNICNumber)
		if req.AccountTitle != "" {
			formData.Set("account_title", req.AccountTitle)
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Customer validation failed",
			Internal:   fmt.Errorf("status %d", resp.StatusCode),
		}
	}

	var validationRes CustomerValidationResponse
	if err := json.Unmarshal(bodyBytes, &validationRes); err != nil {
		return nil, fmt.Errorf("failed to parse validation response: %w", err)
	}

	return &validationRes, nil
}

// InitiateTransaction calls POST /transaction
func (c *Client) InitiateTransaction(ctx context.Context, req InitiateTransactionRequest, otp string) (*InitiateTransactionResponse, error) {
	token, err := c.GetAuthToken(ctx, req.CustomerIP)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := c.baseURL + "/transaction"
	formData := url.Values{}

	formData.Set("basket_id", req.BasketID)
	formData.Set("txnamt", req.TxnAmt)
	formData.Set("order_date", req.OrderDate)
	formData.Set("customer_mobile_no", req.CustomerMobileNo)
	formData.Set("customer_email_address", req.CustomerEmailAddress)
	formData.Set("account_type_id", req.AccountTypeID)
	formData.Set("merCatCode", req.MerCatCode)
	formData.Set("customer_ip", req.CustomerIP)

	if req.TransactionID != "" {
		formData.Set("transaction_id", req.TransactionID)
	}
	if req.ECI != "" {
		formData.Set("eci", req.ECI)
	}

	if otp != "" {
		formData.Set("otp", otp)
	}

	securedHash := CalculateTransactionHash(req, otp, c.securedKey)
	formData.Set("secured_hash", securedHash)

	if req.CardNumber != "" {
		formData.Set("card_number", req.CardNumber)
		formData.Set("expiry_month", req.ExpiryMonth)
		formData.Set("expiry_year", req.ExpiryYear)
		formData.Set("cvv", req.CVV)
		if req.Data3DSSecureID != "" {
			formData.Set("data_3ds_secureid", req.Data3DSSecureID)
		}
		if req.Data3DSPaRes != "" {
			formData.Set("data_3ds_pares", req.Data3DSPaRes)
		}
	} else if req.AccountNumber != "" {
		formData.Set("bank_code", req.BankCode)
		formData.Set("account_number", req.AccountNumber)
		formData.Set("cnic_number", req.CNICNumber)
		if req.AccountTitle != "" {
			formData.Set("account_title", req.AccountTitle)
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Transaction initiation failed",
			Internal:   fmt.Errorf("status %d", resp.StatusCode),
		}
	}

	var txnRes InitiateTransactionResponse
	if err := json.Unmarshal(bodyBytes, &txnRes); err != nil {
		return nil, fmt.Errorf("failed to parse transaction response: %w", err)
	}

	return &txnRes, nil
}

// GetTransactionStatus calls GET /transaction/<transaction_id>
func (c *Client) GetTransactionStatus(ctx context.Context, transactionID string) (*TransactionStatusResponse, error) {
	token, err := c.GetAuthToken(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := fmt.Sprintf("%s/transaction/%s", c.baseURL, url.PathEscape(transactionID))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Transaction status check failed",
			Internal:   fmt.Errorf("status %d", resp.StatusCode),
		}
	}

	var statusRes TransactionStatusResponse
	if err := json.Unmarshal(bodyBytes, &statusRes); err != nil {
		return nil, fmt.Errorf("failed to parse transaction status response: %w", err)
	}

	return &statusRes, nil
}

// GetTransactionStatusByBasketID calls GET /transaction/basket/<basket_id>
func (c *Client) GetTransactionStatusByBasketID(ctx context.Context, basketID string) (*TransactionStatusResponse, error) {
	token, err := c.GetAuthToken(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := fmt.Sprintf("%s/transaction/basket/%s", c.baseURL, url.PathEscape(basketID))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Transaction status check by basket ID failed",
			Internal:   fmt.Errorf("status %d", resp.StatusCode),
		}
	}

	var statusRes TransactionStatusResponse
	if err := json.Unmarshal(bodyBytes, &statusRes); err != nil {
		return nil, fmt.Errorf("failed to parse transaction status response: %w", err)
	}

	return &statusRes, nil
}

// GetTemporaryTransactionToken calls POST /transaction/token
func (c *Client) GetTemporaryTransactionToken(ctx context.Context, req TemporaryTokenRequest) (*TemporaryTokenResponse, error) {
	token, err := c.GetAuthToken(ctx, req.CustomerIP)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := c.baseURL + "/transaction/token"
	formData := url.Values{}

	// Common Parameters
	formData.Set("basket_id", req.BasketID)
	formData.Set("txnamt", req.TxnAmt)
	formData.Set("order_date", req.OrderDate)
	formData.Set("user_mobile_number", req.CustomerMobileNo)
	formData.Set("merchant_user_id", req.MerchantUserId)
	formData.Set("account_type", req.AccountTypeID)
	formData.Set("merCatCode", req.MerCatCode)
	formData.Set("customer_ip", req.CustomerIP)

	// Card specific
	if req.CardNumber != "" {
		formData.Set("card_number", req.CardNumber)
		formData.Set("expiry_month", req.ExpiryMonth)
		formData.Set("expiry_year", req.ExpiryYear)
		formData.Set("cvv", req.CVV)
		if req.Data3DSPagemode != "" {
			formData.Set("data_3ds_pagemode", req.Data3DSPagemode)
		}
		if req.Data3DSCallbackURL != "" {
			formData.Set("data_3ds_callback_url", req.Data3DSCallbackURL)
		}
	} else if req.AccountNumber != "" {
		formData.Set("account_number", req.AccountNumber)
		formData.Set("cnic_number", req.CNICNumber)
	}

	securedHash := CalculateTemporaryTokenHash(req, c.securedKey)
	formData.Set("secured_hash", securedHash)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Temporary token request failed",
			Internal:   fmt.Errorf("status %d", resp.StatusCode),
		}
	}

	var tokenRes TemporaryTokenResponse
	if err := json.Unmarshal(bodyBytes, &tokenRes); err != nil {
		return nil, fmt.Errorf("failed to parse temporary token response: %w", err)
	}

	return &tokenRes, nil
}

// InitiateTokenizedTransaction calls POST /transaction/tokenized
func (c *Client) InitiateTokenizedTransaction(ctx context.Context, req TokenizedTransactionRequest) (*TokenizedTransactionResponse, error) {
	token, err := c.GetAuthToken(ctx, req.CustomerIP)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := c.baseURL + "/transaction/tokenized"
	formData := url.Values{}

	formData.Set("instrument_token", req.InstrumentToken)
	formData.Set("transaction_id", req.TransactionID)
	formData.Set("merchant_user_id", req.MerchantUserId)
	formData.Set("user_mobile_number", req.CustomerMobileNo)
	formData.Set("basket_id", req.BasketID)
	formData.Set("order_date", req.OrderDate)
	formData.Set("txndesc", req.TxnDesc)
	formData.Set("txnamt", req.TxnAmt)
	formData.Set("customer_ip", req.CustomerIP)
	formData.Set("merCatCode", req.MerCatCode)

	if req.Otp != "" {
		formData.Set("otp", req.Otp)
	}
	if req.ECI != "" {
		formData.Set("eci", req.ECI)
	}
	if req.Data3DSSecureID != "" {
		formData.Set("data_3ds_secureid", req.Data3DSSecureID)
	}
	if req.Data3DSPaRes != "" {
		formData.Set("data_3ds_pares", req.Data3DSPaRes)
	}

	securedHash := CalculateTokenizedTransactionHash(req, c.securedKey)
	formData.Set("secured_hash", securedHash)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Tokenized transaction failed",
			Internal:   fmt.Errorf("status %d", resp.StatusCode),
		}
	}

	var txnRes TokenizedTransactionResponse
	if err := json.Unmarshal(bodyBytes, &txnRes); err != nil {
		return nil, fmt.Errorf("failed to parse tokenized transaction response: %w", err)
	}

	return &txnRes, nil
}
