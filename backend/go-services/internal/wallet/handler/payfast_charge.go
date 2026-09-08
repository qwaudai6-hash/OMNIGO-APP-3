package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/payment/payfast"
	"github.com/omnigo/backend/internal/shared/middleware"
)

type payfastChargeRequest struct {
	Amount         string `json:"amount" binding:"required"`
	OrderID        string `json:"order_id" binding:"required"`
	CustomerMobile string `json:"customer_mobile"`
	CustomerEmail  string `json:"customer_email"`
}

// payfastDeprecationHeaders marks the legacy hosted-checkout endpoints as
// deprecated WITHOUT removing them: older app versions still in the wild keep
// working, while the Deprecation/Sunset headers plus the per-call log line let
// us track remaining legacy traffic and pick a data-driven removal date.
//
// DEPRECATED: current app versions use the Option C tokenized flow at
// POST /api/v1/payments/payfast/payment (payment orchestrator) which adds
// fraud checks, payment_transactions audit rows, 3DS step-up, gateway status
// verification and the admin/vendor/delivery ledger split. This hosted
// checkout path marks orders paid WITHOUT any ledger split.
// successorPayfastEndpoint is the Option C replacement endpoint that current
// app versions call instead of this legacy hosted-checkout flow.
const successorPayfastEndpoint = "/api/v1/payments/payfast/payment"

// legacySunsetDate resolves the advertised removal date for the deprecated
// hosted-checkout endpoints. Operators can pin an exact date via
// PAYFAST_LEGACY_SUNSET_DATE (RFC 7231 / HTTP-date format); otherwise a rolling
// one-year-out date is announced so the header never goes stale.
func legacySunsetDate() string {
	if v := strings.TrimSpace(os.Getenv("PAYFAST_LEGACY_SUNSET_DATE")); v != "" {
		return v
	}
	return time.Now().UTC().AddDate(1, 0, 0).Format("Mon, 02 Jan 2006 15:04:05 GMT")
}

func payfastDeprecationHeaders(c *gin.Context) {
	c.Header("Deprecation", "true")
	c.Header("Sunset", legacySunsetDate())
	c.Header("Link", "<"+successorPayfastEndpoint+">; rel=successor-version")
	log.Printf("[DEPRECATED] legacy PayFast hosted-checkout endpoint called from ip=%s ua=%s — migrate clients to %s",
		c.ClientIP(), c.Request.UserAgent(), successorPayfastEndpoint)
}

// PayFastCharge handles POST /api/v1/wallet/payfast/charge — DEPRECATED hosted
// checkout flow kept only for backward compatibility with older app releases.
// It validates order ownership and amount, records a pending payment intent,
// and returns the hosted checkout redirect URL. Confirmation arrives at
// PayFastCallback. New integrations MUST use the Option C flow instead (see
// payfastDeprecationHeaders).
func (h *WalletHandler) PayFastCharge(c *gin.Context) {
	payfastDeprecationHeaders(c)

	var req payfastChargeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	callerID := middleware.GetTrackingID(c)
	if callerID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing authenticated customer"})
		return
	}
	if h.orderSvc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "order service not configured"})
		return
	}
	order, err := h.orderSvc.GetOrder(c.Request.Context(), req.OrderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}
	if order.UserTrackID != callerID {
		c.JSON(http.StatusForbidden, gin.H{"error": "order does not belong to authenticated customer"})
		return
	}

	var amount float64
	if _, err := fmt.Sscanf(strings.TrimSpace(req.Amount), "%f", &amount); err != nil || amount <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid amount"})
		return
	}
	const amountEpsilon = 0.01
	if diff := amount - order.TotalAmount; diff > amountEpsilon || diff < -amountEpsilon {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount mismatch: provided amount does not match order total"})
		return
	}

	merchantID := os.Getenv("PAYFAST_MERCHANT_ID")
	if merchantID == "" {
		log.Printf("[payfast-charge] ERROR: PAYFAST_MERCHANT_ID not configured")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "PayFast merchant ID not configured on server"})
		return
	}

	securedKey := os.Getenv("PAYFAST_SECURED_KEY")
	if securedKey == "" {
		log.Printf("[payfast-charge] ERROR: PAYFAST_SECURED_KEY not configured")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "PayFast secured key not configured on server"})
		return
	}

	baseURL := os.Getenv("PAYFAST_BASE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("PAYFAST_API_URL")
	}
	if baseURL == "" {
		baseURL = "https://ipguat.apps.net.pk/Ecommerce/api/Transaction"
	}
	// Return URL: explicit WALLET_RETURN_URL wins, else build from PUBLIC_BASE_URL.
	// Never assume any deployment domain in code — an unreachable/foreign success_url
	// would strand customers on the gateway page after paying.
	returnURL := os.Getenv("WALLET_RETURN_URL")
	if returnURL == "" {
		publicBase := strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL"))
		if publicBase == "" {
			log.Printf("[payfast-charge] misconfiguration: WALLET_RETURN_URL or PUBLIC_BASE_URL must be set to build the hosted-checkout return_url")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "return URL is not configured on the server"})
			return
		}
		returnURL = publicBase + "/api/v1/wallet/payfast/callback"
	}

	basketID := fmt.Sprintf("PF%d", time.Now().UnixNano())
	if err := h.storePendingWalletLoad(c.Request.Context(), basketID, pendingWalletLoad{
		CustomerTrackingID: callerID,
		Gateway:            "payfast",
		AmountPKR:          order.TotalAmount,
		AmountCents:        int64(order.TotalAmount * 100),
		OrderID:            order.TrackingID,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record pending payment: " + err.Error()})
		return
	}

	// Fetch access token with basket details per PayFast PHP sample
	// GetAccessToken requires: MERCHANT_ID, SECURED_KEY, BASKET_ID, TXNAMT, CURRENCY_CODE
	accessToken := ""
	accessToken = fetchPayfastAccessToken(c.Request.Context(), baseURL, merchantID, securedKey, basketID, order.TotalAmount)

	merchantName := os.Getenv("PAYFAST_MERCHANT_NAME")
	if merchantName == "" {
		merchantName = "OMNIGO"
	}

	failureURL := returnURL
	orderDate := time.Now().Format("2006-01-02 15:04:05")

	// PayFast signature is a random string (per official PHP sample: "SOME-RANDOM-STRING")
	signature := fmt.Sprintf("SIG-%d", time.Now().UnixNano())

	if strings.Contains(baseURL, "apps.net.pk") {
		formEndpoint := strings.TrimRight(baseURL, "/")
		if !strings.HasSuffix(formEndpoint, "/PostTransaction") {
			if strings.HasSuffix(formEndpoint, "/Transaction") {
				formEndpoint += "/PostTransaction"
			} else {
				formEndpoint += "/Transaction/PostTransaction"
			}
		}

		// Build POST form HTML that auto-submits to PayFast
		// Per PayFast PHP sample, form uses method='post' with all fields as hidden inputs
		formHTML := fmt.Sprintf(`<!DOCTYPE html>
<html><head><title>Redirecting to PayFast...</title></head>
<body>
<form id="payfast_form" method="post" action="%s">
<input type="hidden" name="MERCHANT_ID" value="%s" />
<input type="hidden" name="MERCHANT_NAME" value="%s" />
<input type="hidden" name="TOKEN" value="%s" />
<input type="hidden" name="BASKET_ID" value="%s" />
<input type="hidden" name="TXNAMT" value="%.2f" />
<input type="hidden" name="CURRENCY_CODE" value="PKR" />
<input type="hidden" name="ORDER_DATE" value="%s" />
<input type="hidden" name="SUCCESS_URL" value="%s" />
<input type="hidden" name="FAILURE_URL" value="%s" />
<input type="hidden" name="CHECKOUT_URL" value="%s" />
<input type="hidden" name="CUSTOMER_EMAIL_ADDRESS" value="%s" />
<input type="hidden" name="CUSTOMER_MOBILE_NO" value="%s" />
<input type="hidden" name="SIGNATURE" value="%s" />
<input type="hidden" name="VERSION" value="MERCHANTCART-0.1" />
<input type="hidden" name="TXNDESC" value="OMNIGO Wallet Top-up" />
<input type="hidden" name="PROCCODE" value="00" />
<input type="hidden" name="TRAN_TYPE" value="ECOMM_PURCHASE" />
<input type="hidden" name="MERCHANT_USERAGENT" value="%s" />
</form>
<script>document.getElementById("payfast_form").submit();</script>
<p>Redirecting to PayFast payment page...</p>
</body></html>`,
			formEndpoint,
			merchantID,
			merchantName,
			accessToken,
			basketID,
			order.TotalAmount,
			orderDate,
			url.QueryEscape(returnURL),
			url.QueryEscape(failureURL),
			url.QueryEscape(returnURL),
			url.QueryEscape(req.CustomerEmail),
			url.QueryEscape(req.CustomerMobile),
			signature,
			c.Request.UserAgent(),
		)

		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(formHTML))
		return
	}

	// Fallback for non-APPS endpoints: return redirect URL
	redirectURL := fmt.Sprintf(
		"%s/hosted?merchant_id=%s&basket_id=%s&txnamt=%.2f&currency_code=PKR&customer_mobile_no=%s&customer_email_address=%s&success_url=%s&failure_url=%s&checkout_url=%s",
		strings.TrimRight(baseURL, "/"),
		url.QueryEscape(merchantID),
		url.QueryEscape(basketID),
		order.TotalAmount,
		url.QueryEscape(req.CustomerMobile),
		url.QueryEscape(req.CustomerEmail),
		url.QueryEscape(returnURL),
		url.QueryEscape(failureURL),
		url.QueryEscape(returnURL),
	)

	c.JSON(http.StatusOK, gin.H{
		"status":       "pending_redirect",
		"gateway":      "payfast",
		"redirect_url": redirectURL,
		"basket_id":    basketID,
		"amount":       order.TotalAmount,
		"order_id":     order.TrackingID,
	})
}

// PayFastCallback handles POST /api/v1/wallet/payfast/callback — DEPRECATED
// async postback for the legacy hosted checkout (see payfastDeprecationHeaders).
func (h *WalletHandler) PayFastCallback(c *gin.Context) {
	payfastDeprecationHeaders(c)

	if err := c.Request.ParseForm(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid form data"})
		return
	}

	basketID := c.Request.FormValue("basket_id")
	if basketID == "" {
		basketID = c.Request.FormValue("Basket_ID")
	}
	statusCode := c.Request.FormValue("status_code")
	if statusCode == "" {
		statusCode = c.Request.FormValue("err_code")
	}
	receivedHash := c.Request.FormValue("secured_hash")
	if receivedHash == "" {
		receivedHash = c.Request.FormValue("validation_hash")
	}
	if basketID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing basket_id in payfast callback"})
		return
	}

	securedKey := os.Getenv("PAYFAST_SECURED_KEY")
	merchantID := os.Getenv("PAYFAST_MERCHANT_ID")
	if securedKey == "" || merchantID == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "payment gateway configuration missing"})
		return
	}
	if receivedHash == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "missing signature hash in PayFast callback"})
		return
	}
	expectedHash := payfast.CalculateResponseValidationHash(basketID, securedKey, merchantID, statusCode)
	if !payfast.VerifyResponseHash(expectedHash, receivedHash) {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid PayFast signature"})
		return
	}

	pending, err := h.consumePendingWalletLoad(c.Request.Context(), basketID)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "no matching pending payment for this transaction (replay or tampered callback)"})
		return
	}
	if pending.OrderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pending record has no order reference"})
		return
	}
	if statusCode != "" && !payfast.IsSuccessCode(statusCode) {
		c.JSON(http.StatusOK, gin.H{"status": "failed", "message": "payment unsuccessful at gateway", "order_id": pending.OrderID})
		return
	}

	if err := h.orderSvc.UpdateOrderStatus(c.Request.Context(), pending.OrderID, "paid"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark order paid: " + err.Error()})
		return
	}
	h.emitPaymentCompleted(c.Request.Context(), pending.OrderID, basketID, "payfast", int64(pending.AmountPKR*100))

	c.JSON(http.StatusOK, gin.H{
		"status":   "verified",
		"order_id": pending.OrderID,
	})
}

// fetchPayfastAccessToken calls GetAccessToken API with basket details per PayFast PHP sample.
// Returns empty string on failure (non-fatal — form can still be submitted without TOKEN).
func fetchPayfastAccessToken(ctx context.Context, baseURL, merchantID, securedKey, basketID string, amount float64) string {
	// Determine token endpoint
	tokenURL := baseURL + "/Transaction/GetAccessToken"
	if strings.Contains(baseURL, "apps.net.pk") {
		if !strings.HasSuffix(baseURL, "/Transaction") && !strings.HasSuffix(baseURL, "/GetAccessToken") {
			tokenURL = strings.TrimRight(baseURL, "/") + "/Transaction/GetAccessToken"
		} else if strings.HasSuffix(baseURL, "/Transaction") {
			tokenURL = strings.TrimRight(baseURL, "/") + "/GetAccessToken"
		} else {
			tokenURL = strings.TrimRight(baseURL, "/")
		}
	}

	// Per PayFast PHP sample: MERCHANT_ID, SECURED_KEY, BASKET_ID, TXNAMT, CURRENCY_CODE, APPLY_DISCOUNT
	formData := url.Values{}
	formData.Set("MERCHANT_ID", merchantID)
	formData.Set("SECURED_KEY", securedKey)
	formData.Set("BASKET_ID", basketID)
	formData.Set("TXNAMT", fmt.Sprintf("%.2f", amount))
	formData.Set("CURRENCY_CODE", "PKR")
	formData.Set("APPLY_DISCOUNT", "true")

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(formData.Encode()))
	if err != nil {
		log.Printf("[payfast-charge] WARN: GetAccessToken request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		log.Printf("[payfast-charge] WARN: failed to read GetAccessToken response: %v", err)
		return ""
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("[payfast-charge] WARN: GetAccessToken returned status %d: %s", resp.StatusCode, string(body))
		return ""
	}

	var result struct {
		AccessToken string `json:"ACCESS_TOKEN"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("[payfast-charge] WARN: failed to parse GetAccessToken response: %v", err)
		return ""
	}

	return result.AccessToken
}
