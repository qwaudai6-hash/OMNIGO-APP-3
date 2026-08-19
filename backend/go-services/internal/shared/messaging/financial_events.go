package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// FinancialNotificationEvent is published when wallet balances, escrows, or payouts change.
type FinancialNotificationEvent struct {
	EventID        string    `json:"event_id"`
	RecipientID    string    `json:"recipient_id"`    // customer / vendor / rider tracking ID
	RecipientRole  string    `json:"recipient_role"`  // customer | vendor | rider
	EventType      string    `json:"event_type"`      // wallet_credited | escrow_released | payout_disbursed | dispute_refunded
	Title          string    `json:"title"`
	Message        string    `json:"message"`
	Amount         float64   `json:"amount"`
	Currency       string    `json:"currency"`
	ReferenceID    string    `json:"reference_id"`
	Timestamp      time.Time `json:"timestamp"`
}

// FinancialEventDispatcher dispatches asynchronous notification events to Kafka / Notification Service.
type FinancialEventDispatcher struct {
	kafkaClient *kgo.Client
	topic       string
}

// NewFinancialEventDispatcher constructs a new dispatcher.
func NewFinancialEventDispatcher(client *kgo.Client) *FinancialEventDispatcher {
	topic := "omnigo.financial.notifications"
	return &FinancialEventDispatcher{
		kafkaClient: client,
		topic:       topic,
	}
}

// Dispatch sends a notification event asynchronously. If Kafka is unavailable, it gracefully logs without failing.
func (d *FinancialEventDispatcher) Dispatch(ctx context.Context, evt FinancialNotificationEvent) {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}
	if evt.Currency == "" {
		evt.Currency = "PKR"
	}

	payloadBytes, err := json.Marshal(evt)
	if err != nil {
		log.Printf("[FinancialEventDispatcher] Failed to marshal event: %v", err)
		return
	}

	log.Printf("[FinancialEventDispatcher] [%s] %s: %s (Amount: %.2f %s)",
		evt.EventType, evt.RecipientID, evt.Message, evt.Amount, evt.Currency)

	if d.kafkaClient == nil {
		return
	}

	record := &kgo.Record{
		Topic: d.topic,
		Key:   []byte(evt.RecipientID),
		Value: payloadBytes,
	}

	d.kafkaClient.Produce(ctx, record, func(r *kgo.Record, err error) {
		if err != nil {
			log.Printf("[FinancialEventDispatcher] Warning: Kafka produce error for recipient %s: %v", evt.RecipientID, err)
		}
	})
}

// Global dispatcher helper
var globalDispatcher *FinancialEventDispatcher

// SetGlobalDispatcher initializes the global dispatcher.
func SetGlobalDispatcher(d *FinancialEventDispatcher) {
	globalDispatcher = d
}

// EmitFinancialNotification is a public helper used across services.
func EmitFinancialNotification(ctx context.Context, recipientID, role, eventType, title, message string, amount float64, refID string) {
	if globalDispatcher == nil {
		log.Printf("[FinancialNotification] [%s] %s: %s (%.2f PKR)", eventType, recipientID, message, amount)
		return
	}
	globalDispatcher.Dispatch(ctx, FinancialNotificationEvent{
		EventID:       fmt.Sprintf("evt_%d", time.Now().UnixNano()),
		RecipientID:   recipientID,
		RecipientRole: role,
		EventType:     eventType,
		Title:         title,
		Message:       message,
		Amount:        amount,
		Currency:      "PKR",
		ReferenceID:   refID,
		Timestamp:     time.Now().UTC(),
	})
}
