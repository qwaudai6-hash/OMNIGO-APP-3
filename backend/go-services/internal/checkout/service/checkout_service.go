package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/paymentintent"
)

type CheckoutItem struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

type CheckoutService struct {
	db    *pgxpool.Pool
	redis redis.UniversalClient
}

func NewCheckoutService(dbPool *pgxpool.Pool, redisClient redis.UniversalClient, stripeAPIKey string) *CheckoutService {
	stripe.Key = stripeAPIKey
	return &CheckoutService{
		db:    dbPool,
		redis: redisClient,
	}
}

// CreateCheckoutTransaction uses Reservation Pattern & Saga compensation to avoid connection pool starvation.
func (s *CheckoutService) CreateCheckoutTransaction(
	ctx context.Context,
	customerID string,
	storeID string,
	nonce string,
	orderTrackingID string,
	items []CheckoutItem,
	amountCents int64,
) (string, error) {

	// 1. Idempotency Lock
	var idempotencyKey string
	if s.redis != nil && nonce != "" {
		idempotencyKey = fmt.Sprintf("idempotency:checkout:%s:%s", customerID, nonce)
		success, err := s.redis.SetNX(ctx, idempotencyKey, "PROCESSING", 120*time.Second).Result()
		if err == nil && !success {
			return "", errors.New("CONFLICT_DUPLICATE_CHECKOUT: request already in progress")
		}
	}

	// 2. FAST DB TRANSACTION: Deduct stock and commit IMMEDIATELY to avoid pool starvation.
	// DB locks are held for <2ms instead of waiting on external network latency.
	err := func() error {
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)

		for _, item := range items {
			// H-2 FIX: products use product_tracking_id (VARCHAR) not numeric id.
			// Frontend sends product_tracking_id consistently everywhere.
			productTrackingID := strings.TrimSpace(item.ProductID)
			if productTrackingID == "" {
				return fmt.Errorf("invalid product id %q: empty", item.ProductID)
			}
			query := `UPDATE products SET stock = stock - $1 WHERE product_tracking_id = $2 AND stock >= $1`
			cmdTag, err := tx.Exec(ctx, query, item.Quantity, productTrackingID)
			if err != nil {
				return fmt.Errorf("failed executing stock deduction: %w", err)
			}
			if cmdTag.RowsAffected() == 0 {
				return fmt.Errorf("insufficient stock or missing product: %s", item.ProductID)
			}
		}
		return tx.Commit(ctx)
	}()

	if err != nil {
		return "", fmt.Errorf("inventory reservation failed: %w", err)
	}

	// 3. EXTERNAL NETWORK CALL: Safe to call Stripe API without holding active PostgreSQL row locks
	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(amountCents),
		Currency: stripe.String(string(stripe.Currency("PKR"))),
		PaymentMethodTypes: []*string{
			stripe.String("card"),
		},
		Metadata: map[string]string{
			"customer_id": customerID,
			"store_id":    storeID,
			"nonce":       nonce,
			"order_id":    orderTrackingID,
		},
	}

	pi, err := paymentintent.New(params)
	if err != nil {
		// COMPENSATION BLOCK: Stripe network/API request failed.
		// Launch background worker to release reserved inventory.
		go s.compensateFailedInventory(context.Background(), items)
		return "", fmt.Errorf("stripe api failure, inventory rolled back: %w", err)
	}

	return pi.ClientSecret, nil
}

// Background Worker to release stock back to active inventory if Stripe
// connection drops. GO-23: the release runs in ONE transaction — a crash
// mid-loop previously leaked reserved stock permanently.
func (s *CheckoutService) compensateFailedInventory(ctx context.Context, items []CheckoutItem) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		fmt.Printf("[Saga Engine Error] compensation tx begin failed: %v\n", err)
		return
	}
	defer tx.Rollback(ctx)

	for _, item := range items {
		// H-2 FIX: products use product_tracking_id (VARCHAR) not numeric id.
		productTrackingID := strings.TrimSpace(item.ProductID)
		if productTrackingID == "" {
			fmt.Printf("[Saga Engine Error] empty product id skipped in compensation\n")
			continue
		}
		query := `UPDATE products SET stock = stock + $1 WHERE product_tracking_id = $2`
		if _, err := tx.Exec(ctx, query, item.Quantity, productTrackingID); err != nil {
			fmt.Printf("[Saga Engine Error] Failed to compensate inventory for product %s: %v\n", productTrackingID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		fmt.Printf("[Saga Engine Error] compensation commit failed: %v\n", err)
		return
	}
	fmt.Println("[Saga Engine] Successfully compensated inventory for aborted Stripe payment.")
}
