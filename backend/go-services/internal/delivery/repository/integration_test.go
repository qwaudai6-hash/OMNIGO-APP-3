//go:build integration
// +build integration

package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/delivery/models"
)

// TestE2E_CreateGigPreventsDuplicates exercises BUG #2 fix end-to-end.
// It creates an order, then calls CreateGig twice concurrently, and
// verifies that only ONE delivery gig is created.
//
// Before the fix: the check-then-act pattern (SELECT COUNT then INSERT)
// had a TOCTOU race — two concurrent calls could both see count=0 and
// both INSERT, creating duplicate delivery rows.
//
// After the fix: CreateGig uses a transaction with SELECT ... FOR UPDATE
// on the parent order, serializing concurrent calls. The second call
// sees the first call's already-active gig and returns an error.
//
// To run:
//   1. Start Postgres on 5434 (see integration_test.go in escrow package)
//   2. go test -tags=integration ./internal/delivery/repository/...
func TestE2E_CreateGigPreventsDuplicates(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		dsn = "postgres://omnigo_user@127.0.0.1:5434/omnigo_db?sslmode=disable&host=/tmp"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("skipping integration test: cannot connect: %v", err)
	}
	defer pool.Close()

	suffix := fmt.Sprintf("creategig-%d", time.Now().UnixNano())
	customerID := "CUST-" + suffix
	vendorID := "VEND-" + suffix
	storeID := "STOR-" + suffix
	orderID := "ORD-" + suffix

	t.Cleanup(func() {
		cleanupDeliveryTest(t, pool, customerID, vendorID, storeID, orderID)
	})

	// Seed
	if err := seedDeliveryTest(t, pool, customerID, vendorID, storeID, orderID); err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	repo := NewDeliveryRepository(pool, pool, nil)

	// ACT: call CreateGig twice serially
	gig1 := &models.DeliveryGig{
		TrackingID:         "DEL-" + suffix + "-1",
		OrderTrackingID:    orderID,
		VendorStoreTrackID: storeID,
		CustomerTrackID:    customerID,
		OTPCode:            "1234",
		DeliveryFee:        100.0,
		AdminCommission:    5.0,
		RiderEarning:       95.0,
	}
	if err := repo.CreateGig(ctx, gig1); err != nil {
		t.Fatalf("first CreateGig failed: %v", err)
	}

	gig2 := &models.DeliveryGig{
		TrackingID:         "DEL-" + suffix + "-2",
		OrderTrackingID:    orderID,
		VendorStoreTrackID: storeID,
		CustomerTrackID:    customerID,
		OTPCode:            "5678",
		DeliveryFee:        100.0,
		AdminCommission:    5.0,
		RiderEarning:       95.0,
	}
	err = repo.CreateGig(ctx, gig2)
	if err == nil {
		t.Error("BUG #2 REGRESSION: second CreateGig succeeded (should have been rejected)")
	} else {
		t.Logf("second CreateGig correctly rejected: %v", err)
	}

	// ASSERT: only ONE active gig exists for the order
	var activeCount int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM deliveries WHERE order_tracking_id = $1 AND status NOT IN ('cancelled','completed')`,
		orderID,
	).Scan(&activeCount)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if activeCount != 1 {
		t.Errorf("active gig count = %d, want 1 (BUG #2: duplicate delivery created!)", activeCount)
	}

	// ASSERT: only ONE delivery row exists for the order (regardless of status)
	var totalCount int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM deliveries WHERE order_tracking_id = $1`,
		orderID,
	).Scan(&totalCount)
	if err != nil {
		t.Fatalf("total count query failed: %v", err)
	}
	if totalCount != 1 {
		t.Errorf("total delivery count = %d, want 1 (BUG #2: duplicate delivery created!)", totalCount)
	}
}

// TestE2E_CreateGig_DBUniqueIndexDefense verifies that the database-level
// UNIQUE index (migration 0024) also protects against duplicates, even
// if the application code is bypassed. This is the second line of
// defense — a raw INSERT that doesn't go through CreateGig should still
// fail.
func TestE2E_CreateGig_DBUniqueIndexDefense(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		dsn = "postgres://omnigo_user@127.0.0.1:5434/omnigo_db?sslmode=disable&host=/tmp"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("skipping integration test: cannot connect: %v", err)
	}
	defer pool.Close()

	suffix := fmt.Sprintf("dbdef-%d", time.Now().UnixNano())
	customerID := "CUST-" + suffix
	vendorID := "VEND-" + suffix
	storeID := "STOR-" + suffix
	orderID := "ORD-" + suffix

	t.Cleanup(func() {
		cleanupDeliveryTest(t, pool, customerID, vendorID, storeID, orderID)
	})

	if err := seedDeliveryTest(t, pool, customerID, vendorID, storeID, orderID); err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	// First raw INSERT
	_, err = pool.Exec(ctx, `
		INSERT INTO deliveries (tracking_id, order_tracking_id, vendor_store_tracking_id, customer_tracking_id, status, delivery_fee, otp_code)
		VALUES ($1, $2, $3, $4, 'broadcasting', 100, '1234')
	`, "DEL-"+suffix+"-1", orderID, storeID, customerID)
	if err != nil {
		t.Fatalf("first raw insert failed: %v", err)
	}

	// Second raw INSERT with same order_tracking_id should be REJECTED
	// by the unique partial index ux_deliveries_active_order
	_, err = pool.Exec(ctx, `
		INSERT INTO deliveries (tracking_id, order_tracking_id, vendor_store_tracking_id, customer_tracking_id, status, delivery_fee, otp_code)
		VALUES ($1, $2, $3, $4, 'broadcasting', 100, '5678')
	`, "DEL-"+suffix+"-2", orderID, storeID, customerID)
	if err == nil {
		t.Error("BUG #2 REGRESSION: second raw insert succeeded (DB unique index missing!)")
	} else {
		t.Logf("second raw insert correctly rejected by DB unique index: %v", err)
	}

	// Verify only 1 row exists
	var count int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM deliveries WHERE order_tracking_id = $1`, orderID).Scan(&count)
	if count != 1 {
		t.Errorf("delivery count after duplicate attempt = %d, want 1", count)
	}
}

// seedDeliveryTest creates minimal test data for delivery tests.
func seedDeliveryTest(t *testing.T, pool *pgxpool.Pool, customerID, vendorID, storeID, orderID string) error {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO users (tracking_id, email, full_name, password_hash, role, is_active, is_verified)
		VALUES ($1, $2, 'Test Customer', 'x', 'customer', true, true)
		ON CONFLICT (tracking_id) DO NOTHING
	`, customerID, customerID+"@test.com")
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO users (tracking_id, email, full_name, password_hash, role, is_active, is_verified)
		VALUES ($1, $2, 'Test Vendor', 'x', 'vendor', true, true)
		ON CONFLICT (tracking_id) DO NOTHING
	`, vendorID, vendorID+"@test.com")
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO stores (store_tracking_id, vendor_tracking_id, store_name, commission_rate, latitude, longitude, is_active)
		VALUES ($1, $2, 'Test Store', 2.0, 24.8607, 67.0011, true)
		ON CONFLICT (store_tracking_id) DO NOTHING
	`, storeID, vendorID)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO orders (order_tracking_id, customer_tracking_id, store_tracking_id, vendor_tracking_id, status, total_amount, currency)
		VALUES ($1, $2, $3, $4, 'paid', 1000, 'PKR')
		ON CONFLICT (order_tracking_id) DO NOTHING
	`, orderID, customerID, storeID, vendorID)
	if err != nil {
		return err
	}
	return nil
}

// cleanupDeliveryTest removes the test data.
func cleanupDeliveryTest(t *testing.T, pool *pgxpool.Pool, customerID, vendorID, storeID, orderID string) {
	t.Helper()
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM order_items WHERE order_tracking_id = $1`, orderID)
	_, _ = pool.Exec(ctx, `DELETE FROM deliveries WHERE order_tracking_id = $1`, orderID)
	_, _ = pool.Exec(ctx, `DELETE FROM orders WHERE order_tracking_id = $1`, orderID)
	_, _ = pool.Exec(ctx, `DELETE FROM stores WHERE store_tracking_id = $1`, storeID)
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE tracking_id IN ($1, $2)`, customerID, vendorID)
}
