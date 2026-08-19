package security

import (
	"fmt"
	"time"
)

// AuditEvent represents a security-relevant event in the payment system.
type AuditEvent struct {
	Timestamp   time.Time `json:"timestamp"`
	EventType   string    `json:"event_type"`   // "payment_split", "cod_settlement", "webhook_received", etc.
	Actor       string    `json:"actor"`        // "stripe", "payfast", "rider", "system"
	ReferenceID string    `json:"reference_id"` // order_id, delivery_id, etc.
	Amount      float64   `json:"amount,omitempty"`
	IPAddress   string    `json:"ip_address,omitempty"`
	UserAgent   string    `json:"user_agent,omitempty"`
	Success     bool      `json:"success"`
	Details     string    `json:"details,omitempty"`
}

// AuditLogger logs security-relevant events for forensic analysis.
// In production, this should write to a dedicated audit log table
// or an external SIEM system.
type AuditLogger struct {
	events []AuditEvent
}

func NewAuditLogger() *AuditLogger {
	return &AuditLogger{}
}

// LogPaymentSplit records a payment split event.
func (l *AuditLogger) LogPaymentSplit(orderID, gateway string, amount float64, success bool, details string) {
	l.events = append(l.events, AuditEvent{
		Timestamp:   time.Now().UTC(),
		EventType:   "payment_split",
		Actor:       gateway,
		ReferenceID: orderID,
		Amount:      amount,
		Success:     success,
		Details:     details,
	})
	if !success {
		fmt.Printf("[AUDIT] PAYMENT_SPLIT FAILED: order=%s gateway=%s amount=%.2f details=%s\n",
			orderID, gateway, amount, details)
	}
}

// LogCODSettlement records a COD settlement event.
func (l *AuditLogger) LogCODSettlement(orderID, riderID, gateway string, amount float64, success bool) {
	l.events = append(l.events, AuditEvent{
		Timestamp:   time.Now().UTC(),
		EventType:   "cod_settlement",
		Actor:       "rider:" + riderID,
		ReferenceID: orderID,
		Amount:      amount,
		Success:     success,
		Details:     fmt.Sprintf("gateway=%s", gateway),
	})
}

// LogWebhookReceived records an incoming webhook.
func (l *AuditLogger) LogWebhookReceived(source, eventID, orderID string, ipAddress string, success bool) {
	l.events = append(l.events, AuditEvent{
		Timestamp:   time.Now().UTC(),
		EventType:   "webhook_received",
		Actor:       source,
		ReferenceID: orderID,
		IPAddress:   ipAddress,
		Success:     success,
		Details:     fmt.Sprintf("event_id=%s", eventID),
	})
}

// LogDisputeFiled records a dispute filing.
func (l *AuditLogger) LogDisputeFiled(disputeID, orderID, filedBy string) {
	l.events = append(l.events, AuditEvent{
		Timestamp:   time.Now().UTC(),
		EventType:   "dispute_filed",
		Actor:       "user:" + filedBy,
		ReferenceID: disputeID,
		Success:     true,
		Details:     fmt.Sprintf("order=%s", orderID),
	})
}

// LogEscrowRelease records an escrow release.
func (l *AuditLogger) LogEscrowRelease(orderID, vendorID string, amount float64) {
	l.events = append(l.events, AuditEvent{
		Timestamp:   time.Now().UTC(),
		EventType:   "escrow_released",
		Actor:       "system",
		ReferenceID: orderID,
		Amount:      amount,
		Success:     true,
		Details:     fmt.Sprintf("vendor=%s", vendorID),
	})
}

// LogPayout records a vendor payout.
func (l *AuditLogger) LogPayout(vendorID string, amount float64, success bool, details string) {
	l.events = append(l.events, AuditEvent{
		Timestamp:   time.Now().UTC(),
		EventType:   "vendor_payout",
		Actor:       "system",
		ReferenceID: vendorID,
		Amount:      amount,
		Success:     success,
		Details:     details,
	})
}

// LogSuspiciousActivity records a potential security issue.
func (l *AuditLogger) LogSuspiciousActivity(eventType, details, ipAddress, userAgent string) {
	l.events = append(l.events, AuditEvent{
		Timestamp: time.Now().UTC(),
		EventType: "suspicious:" + eventType,
		Actor:     "unknown",
		IPAddress: ipAddress,
		UserAgent: userAgent,
		Success:   false,
		Details:   details,
	})
	fmt.Printf("[AUDIT] SUSPICIOUS: type=%s ip=%s details=%s\n", eventType, ipAddress, details)
}

// GetRecentEvents returns the last N audit events.
func (l *AuditLogger) GetRecentEvents(n int) []AuditEvent {
	if n > len(l.events) {
		n = len(l.events)
	}
	return l.events[len(l.events)-n:]
}
