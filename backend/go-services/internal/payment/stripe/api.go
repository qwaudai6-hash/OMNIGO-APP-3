package stripe

import (
	"fmt"
	"time"
)

// GenerateIdempotencyKey creates a deterministic, reproducible key for Stripe API calls.
// Format: "stripe:<operation>:<orderID>:v<version>"
// Version counter allows retries of the same operation while creating new keys for new attempts.
func GenerateIdempotencyKey(operation, orderID string) string {
	return fmt.Sprintf("stripe:%s:%s:v1", operation, orderID)
}

// GenerateRefundKey creates an idempotency key for refund operations.
// Uses refund_counter to allow multiple partial refunds on the same order.
func GenerateRefundKey(orderID string, refundNumber int) string {
	return fmt.Sprintf("stripe:refund:%s:refund_%d", orderID, refundNumber)
}

// GenerateWebhookDedupKey creates a Redis key for webhook event deduplication.
func GenerateWebhookDedupKey(stripeEventID string) string {
	return fmt.Sprintf("stripe:webhook:dedup:%s", stripeEventID)
}

// WebhookDedupTTL is how long we remember a processed webhook event.
// Stripe retries for up to 3 days; we keep 48h to be safe without unbounded growth.
const WebhookDedupTTL = 48 * time.Hour
