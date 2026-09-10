package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/omnigo/backend/internal/return/models"
	"github.com/omnigo/backend/internal/return/repository"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	returnWindowHours         = 36
	pickupDeadlineHours       = 12
	vendorVerifyDeadlineHours = 24
	returnHoldHours           = 72 // Hold escrow for 3 days during return verification
)

// EscrowManager handles escrow operations for returns.
type EscrowManager interface {
	CancelForOrder(ctx context.Context, orderTrackingID string) error
	CreateReturnHold(ctx context.Context, orderID, vendorID string, amount int64, holdUntil time.Time) error
	RefundForReturn(ctx context.Context, orderTrackingID string, amount int64) error
}

type ReturnService struct {
	repo    *repository.ReturnRepository
	escrow  EscrowManager
	kafka   *messaging.KafkaClient
}

func NewReturnService(
	repo *repository.ReturnRepository,
	escrow EscrowManager,
	kafka *messaging.KafkaClient,
) *ReturnService {
	return &ReturnService{
		repo:   repo,
		escrow: escrow,
		kafka:  kafka,
	}
}

// emitEvent produces a Kafka event. Nil-safe (no-op if kafka is nil).
func (s *ReturnService) emitEvent(ctx context.Context, topic, key string, payload interface{}) {
	if s.kafka == nil || s.kafka.Client == nil {
		return
	}
	eventBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[RETURN] Failed to marshal event for topic %s: %v", topic, err)
		return
	}
	record := &kgo.Record{
		Topic: topic,
		Key:   []byte(key),
		Value: eventBytes,
	}
	s.kafka.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
		if err != nil {
			log.Printf("[RETURN] Failed to produce event to topic %s: %v", topic, err)
		}
	})
}

// RequestReturn handles a customer's return request.
// It validates the order, checks the return window, creates the return request,
// and re-holds the escrow for verification.
func (s *ReturnService) RequestReturn(
	ctx context.Context,
	orderTrackingID string,
	customerID string,
	reason string,
	returnItems []models.ReturnItem,
) (*models.ReturnRequest, error) {
	// 1. Check if order already has an active return
	hasActive, err := s.repo.HasActiveReturn(ctx, orderTrackingID)
	if err != nil {
		return nil, fmt.Errorf("failed to check active return: %w", err)
	}
	if hasActive {
		return nil, fmt.Errorf("ORDER_ALREADY_HAS_RETURN: order %s already has an active return request", orderTrackingID)
	}

	// 2. Check return window
	isOpen, err := s.repo.IsReturnWindowOpen(ctx, orderTrackingID)
	if err != nil {
		return nil, fmt.Errorf("failed to check return window: %w", err)
	}
	if !isOpen {
		return nil, fmt.Errorf("RETURN_WINDOW_EXPIRED: return window has expired for order %s (36 hours from delivery)", orderTrackingID)
	}

	// 3. Get order details
	orderData, err := s.repo.GetOrderForReturn(ctx, orderTrackingID)
	if err != nil {
		return nil, fmt.Errorf("failed to get order details: %w", err)
	}

	// 4. Validate customer owns this order
	if orderData["customer_tracking_id"] != customerID {
		return nil, fmt.Errorf("FORBIDDEN: customer %s does not own order %s", customerID, orderTrackingID)
	}

	// 5. Validate order is in returnable state
	status := orderData["status"].(string)
	if status != "delivered" && status != "completed" {
		return nil, fmt.Errorf("ORDER_NOT_RETURNABLE: order status is '%s', must be 'delivered' or 'completed'", status)
	}

	// 6. Calculate return delivery fee
	var returnFeePaisa int64 = 15000 // PKR 150 in paisa

	// 7. Create return request
	now := time.Now()
	pickupDeadline := now.Add(time.Duration(pickupDeadlineHours) * time.Hour)

	returnItemsJSON, _ := json.Marshal(returnItems)

	req := &models.ReturnRequest{
		OrderTrackingID:       orderTrackingID,
		CustomerTrackingID:    customerID,
		VendorTrackingID:      orderData["vendor_tracking_id"].(string),
		StoreTrackingID:       orderData["store_tracking_id"].(string),
		Reason:                reason,
		ReturnItems:           returnItemsJSON,
		Status:                models.ReturnStatusRequested,
		RequestedAt:           now,
		PickupDeadline:        &pickupDeadline,
		ReturnDeliveryFeePaisa: returnFeePaisa,
		PaymentStatus:         "pending",
	}

	if err := s.repo.CreateReturnRequest(ctx, req); err != nil {
		return nil, fmt.Errorf("failed to create return request: %w", err)
	}

	// 8. Re-hold escrow for return verification
	if s.escrow != nil {
		vendorID := orderData["vendor_tracking_id"].(string)
		totalAmount := int64(0)
		if v, ok := orderData["total_amount_paisa"]; ok {
			if amt, ok := v.(int64); ok {
				totalAmount = amt
			}
		}

		// Create return-specific hold with extended hold_until
		returnHoldUntil := time.Now().Add(time.Duration(returnHoldHours) * time.Hour)
		if err := s.escrow.CreateReturnHold(ctx, orderTrackingID, vendorID, totalAmount, returnHoldUntil); err != nil {
			log.Printf("[RETURN-%s] Warning: failed to create return escrow hold: %v", orderTrackingID, err)
		}
	}

	// 9. Emit Kafka event for delivery-service to create return gig
	event := map[string]interface{}{
		"return_request_id":    req.ID,
		"order_tracking_id":    orderTrackingID,
		"customer_tracking_id": customerID,
		"vendor_tracking_id":   orderData["vendor_tracking_id"].(string),
		"store_tracking_id":    orderData["store_tracking_id"].(string),
		"reason":               reason,
		"customer_name":        orderData["customer_name"],
		"customer_address":     orderData["customer_address"],
		"customer_phone":       orderData["customer_phone"],
		"customer_lat":         orderData["customer_lat"],
		"customer_lng":         orderData["customer_lng"],
		"return_fee_paisa":     returnFeePaisa,
		"items_summary":        "",
		"timestamp":            time.Now().UnixMilli(),
	}
	s.emitEvent(ctx, "orders.return_requested", orderTrackingID, event)

	return req, nil
}

// GetReturnRequest retrieves a return request by ID.
func (s *ReturnService) GetReturnRequest(ctx context.Context, id string) (*models.ReturnRequest, error) {
	return s.repo.GetReturnRequestByID(ctx, id)
}

// GetReturnByOrderID retrieves the active return request for an order.
func (s *ReturnService) GetReturnByOrderID(ctx context.Context, orderTrackingID string) (*models.ReturnRequest, error) {
	return s.repo.GetReturnRequestByOrderID(ctx, orderTrackingID)
}

// UpdateStatus updates the return request status with transition validation.
func (s *ReturnService) UpdateStatus(ctx context.Context, id, newStatus string) error {
	current, err := s.repo.GetReturnRequestByID(ctx, id)
	if err != nil {
		return err
	}

	if !models.IsValidReturnTransition(current.Status, newStatus) {
		return fmt.Errorf("invalid transition from '%s' to '%s'", current.Status, newStatus)
	}

	return s.repo.UpdateReturnStatus(ctx, id, newStatus)
}

// AssignRider assigns a rider to a return pickup.
func (s *ReturnService) AssignRider(ctx context.Context, id, riderTrackingID, gigTrackingID string) error {
	current, err := s.repo.GetReturnRequestByID(ctx, id)
	if err != nil {
		return err
	}

	if !models.IsValidReturnTransition(current.Status, models.ReturnStatusRiderAssigned) {
		return fmt.Errorf("cannot assign rider in '%s' status", current.Status)
	}

	return s.repo.AssignRider(ctx, id, riderTrackingID, gigTrackingID)
}

// RecordPickup records the rider's pickup with photo proof.
func (s *ReturnService) RecordPickup(ctx context.Context, id, photoURL string) error {
	current, err := s.repo.GetReturnRequestByID(ctx, id)
	if err != nil {
		return err
	}

	if !models.IsValidReturnTransition(current.Status, models.ReturnStatusPickupCompleted) {
		return fmt.Errorf("cannot record pickup in '%s' status", current.Status)
	}

	return s.repo.RecordPickupPhoto(ctx, id, photoURL)
}

// RecordDeliveryToVendor records the rider's delivery to vendor store.
func (s *ReturnService) RecordDeliveryToVendor(ctx context.Context, id, photoURL string) error {
	current, err := s.repo.GetReturnRequestByID(ctx, id)
	if err != nil {
		return err
	}

	if !models.IsValidReturnTransition(current.Status, models.ReturnStatusDelivered) {
		return fmt.Errorf("cannot record delivery in '%s' status", current.Status)
	}

	return s.repo.RecordDeliveryPhoto(ctx, id, photoURL)
}

// VerifyByVendor processes the vendor's verification of the returned product.
// When verified, triggers refund to customer and cancels COD debts.
func (s *ReturnService) VerifyByVendor(
	ctx context.Context,
	id string,
	verified bool,
	photoURL string,
	notes string,
) error {
	current, err := s.repo.GetReturnRequestByID(ctx, id)
	if err != nil {
		return err
	}

	if !models.IsValidReturnTransition(current.Status, models.ReturnStatusVerified) &&
		!models.IsValidReturnTransition(current.Status, models.ReturnStatusDisputed) {
		return fmt.Errorf("cannot verify in '%s' status", current.Status)
	}

	if err := s.repo.VerifyByVendor(ctx, id, verified, photoURL, notes); err != nil {
		return err
	}

	if verified {
		// Trigger refund to customer
		if s.escrow != nil {
			orderID := current.OrderTrackingID
			// Get order total for refund amount
			orderData, err := s.repo.GetOrderForReturn(ctx, orderID)
			if err != nil {
				log.Printf("[RETURN-%s] Warning: failed to get order for refund: %v", orderID, err)
			} else {
				totalAmount := int64(0)
				if v, ok := orderData["total_amount_paisa"]; ok {
					if amt, ok := v.(int64); ok {
						totalAmount = amt
					}
				}
				// Refund from escrow to customer wallet + cancel COD debts
				if err := s.escrow.RefundForReturn(ctx, orderID, totalAmount); err != nil {
					log.Printf("[RETURN-%s] Warning: failed to process return refund: %v", orderID, err)
				}
			}
		}

		s.emitEvent(ctx, "return.verified", current.OrderTrackingID, map[string]interface{}{
			"return_request_id": id,
			"order_tracking_id": current.OrderTrackingID,
			"status":            "verified",
			"timestamp":         time.Now().UnixMilli(),
		})
	} else {
		s.emitEvent(ctx, "return.disputed", current.OrderTrackingID, map[string]interface{}{
			"return_request_id": id,
			"order_tracking_id": current.OrderTrackingID,
			"status":            "disputed",
			"reason":            notes,
			"timestamp":         time.Now().UnixMilli(),
		})
	}

	return nil
}

// CompleteReturn processes the final settlement after vendor verification.
func (s *ReturnService) CompleteReturn(ctx context.Context, id string) error {
	current, err := s.repo.GetReturnRequestByID(ctx, id)
	if err != nil {
		return err
	}

	if !models.IsValidReturnTransition(current.Status, models.ReturnStatusCompleted) {
		return fmt.Errorf("cannot complete in '%s' status", current.Status)
	}

	// Emit Kafka event
	s.emitEvent(ctx, "return.completed", current.OrderTrackingID, map[string]interface{}{
		"return_request_id": id,
		"order_tracking_id": current.OrderTrackingID,
		"status":            "completed",
		"timestamp":         time.Now().UnixMilli(),
	})

	return s.repo.CompleteReturn(ctx, id)
}

// CancelReturn cancels a return request (by customer before rider pickup).
func (s *ReturnService) CancelReturn(ctx context.Context, id string) error {
	current, err := s.repo.GetReturnRequestByID(ctx, id)
	if err != nil {
		return err
	}

	if !models.IsValidReturnTransition(current.Status, models.ReturnStatusCancelled) {
		return fmt.Errorf("cannot cancel in '%s' status", current.Status)
	}

	return s.repo.CancelReturn(ctx, id)
}

// SetReturnDeadline sets the 36-hour return deadline on an order.
func (s *ReturnService) SetReturnDeadline(ctx context.Context, orderTrackingID string, deliveredAt time.Time) error {
	deadline := deliveredAt.Add(time.Duration(returnWindowHours) * time.Hour)
	return s.repo.SetReturnDeadline(ctx, orderTrackingID, deadline)
}
