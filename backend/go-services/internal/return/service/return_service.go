package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnigo/backend/internal/return/fraud"
	"github.com/omnigo/backend/internal/return/models"
	"github.com/omnigo/backend/internal/return/repository"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	returnWindowHours         = 36
	pickupDeadlineHours       = 12
	vendorVerifyDeadlineHours = 24
	returnHoldHours           = 72
	maxDisputesPer90Days      = 3
)

// EscrowManager handles escrow operations for returns.
type EscrowManager interface {
	CancelForOrder(ctx context.Context, orderTrackingID string) error
	CreateReturnHold(ctx context.Context, orderID, vendorID string, amount int64, holdUntil time.Time) error
	RefundForReturn(ctx context.Context, orderTrackingID string, amount int64) error
	FreezeForDispute(ctx context.Context, orderTrackingID string, disputeID uuid.UUID) error
	UnfreezeOnRejection(ctx context.Context, disputeID uuid.UUID) error
}

type ReturnService struct {
	repo   *repository.ReturnRepository
	escrow EscrowManager
	kafka  *messaging.KafkaClient
	fraud  *fraud.ReturnFraudDetector
	rdb    redis.UniversalClient
}

func NewReturnService(
	repo *repository.ReturnRepository,
	escrow EscrowManager,
	kafka *messaging.KafkaClient,
	fraudDetector *fraud.ReturnFraudDetector,
	rdb redis.UniversalClient,
) *ReturnService {
	return &ReturnService{
		repo:   repo,
		escrow: escrow,
		kafka:  kafka,
		fraud:  fraudDetector,
		rdb:    rdb,
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

// checkVendorDisputeCount returns the number of disputes by a vendor in the last 90 days.
func (s *ReturnService) checkVendorDisputeCount(ctx context.Context, vendorID string) (int, error) {
	if s.rdb == nil {
		return 0, nil
	}
	key := fmt.Sprintf("vendor:disputes:count:%s", vendorID)
	min := float64(time.Now().Add(-90 * 24 * time.Hour).UnixMilli())
	max := float64(time.Now().UnixMilli())
	count, err := s.rdb.ZCount(ctx, key, fmt.Sprintf("%f", min), fmt.Sprintf("%f", max)).Result()
	return int(count), err
}

// recordVendorDispute records a vendor dispute in the Redis sorted set.
func (s *ReturnService) recordVendorDispute(ctx context.Context, vendorID string) {
	if s.rdb == nil {
		return
	}
	key := fmt.Sprintf("vendor:disputes:count:%s", vendorID)
	s.rdb.ZAdd(ctx, key, redis.Z{
		Score:  float64(time.Now().UnixMilli()),
		Member: time.Now().Format(time.RFC3339Nano),
	})
	s.rdb.Expire(ctx, key, 90*24*time.Hour)
}

// handleVendorPenalty checks if vendor should be suspended for repeated disputes.
func (s *ReturnService) handleVendorPenalty(ctx context.Context, vendorID string) {
	count, err := s.checkVendorDisputeCount(ctx, vendorID)
	if err != nil {
		log.Printf("[RETURN] Warning: failed to check vendor dispute count: %v", err)
		return
	}

	if count >= maxDisputesPer90Days {
		// Deactivate store for 24 hours
		if err := s.repo.DeactivateStore(ctx, vendorID, 24*time.Hour); err != nil {
			log.Printf("[RETURN] Warning: failed to deactivate store for vendor %s: %v", vendorID, err)
			return
		}

		// Set Redis TTL for auto-reactivation after 24 hours
		if s.rdb != nil {
			suspensionKey := fmt.Sprintf("vendor:suspended:%s", vendorID)
			s.rdb.Set(ctx, suspensionKey, "dispute_penalty", 24*time.Hour)
		}

		// Notify vendor
		s.emitEvent(ctx, "vendor.store_suspended", vendorID, map[string]interface{}{
			"vendor_id":       vendorID,
			"reason":          "3 disputes in 90 days",
			"duration_hours":  24,
			"message":         "Your store has been suspended for 24 hours due to repeated false disputes.",
			"timestamp":       time.Now().UnixMilli(),
		})

		// Notify admin
		s.emitEvent(ctx, "admin.vendor_suspended", vendorID, map[string]interface{}{
			"vendor_id":      vendorID,
			"reason":         "auto_suspension_3_disputes",
			"dispute_count":  count,
			"message":        fmt.Sprintf("Vendor %s auto-suspended: %d disputes in 90 days", vendorID, count),
			"timestamp":      time.Now().UnixMilli(),
		})

		log.Printf("[RETURN] Vendor %s suspended for 24h: %d disputes in 90 days", vendorID, count)
	}
}

// RequestReturn handles a customer's return request.
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

	// 6. Fraud detection
	if s.fraud != nil {
		totalAmount := int64(0)
		if v, ok := orderData["total_amount_paisa"]; ok {
			if amt, ok := v.(int64); ok {
				totalAmount = amt
			}
		}

		returnCount, err := s.repo.GetCustomerReturnCount(ctx, customerID)
		if err != nil {
			log.Printf("[RETURN-%s] Warning: failed to get customer return count: %v", orderTrackingID, err)
			returnCount = 0
		}
		orderCount, err := s.repo.GetCustomerOrderCount(ctx, customerID)
		if err != nil {
			log.Printf("[RETURN-%s] Warning: failed to get customer order count: %v", orderTrackingID, err)
			orderCount = 0
		}

		fraudResult, err := s.fraud.CheckReturn(ctx, customerID, totalAmount, returnCount, orderCount)
		if err != nil {
			log.Printf("[RETURN-%s] Warning: fraud check failed: %v", orderTrackingID, err)
		} else if fraudResult.Blocked {
			return nil, fmt.Errorf("RETURN_BLOCKED_BY_FRAUD_DETECTION: %v", fraudResult.Reasons)
		} else if fraudResult.FlagOnly {
			log.Printf("[RETURN-%s] RETURN FLAGGED FOR REVIEW: %v", orderTrackingID, fraudResult.Reasons)
		}
	}

	// 7. Calculate return delivery fee
	var returnFeePaisa int64 = 15000 // PKR 150 in paisa

	// 8. Create return request
	now := time.Now()
	pickupDeadline := now.Add(time.Duration(pickupDeadlineHours) * time.Hour)

	returnItemsJSON, _ := json.Marshal(returnItems)

	req := &models.ReturnRequest{
		OrderTrackingID:       orderTrackingID,
		CustomerTrackingID:    customerID,
		VendorTrackingID:      orderData["vendor_tracking_id"].(string),
		StoreTrackingID:       orderData["store_tracking_id"].(string),
		PaymentMethod:         fmt.Sprintf("%v", orderData["payment_gateway"]),
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

	// Record return in fraud tracking windows
	if s.fraud != nil {
		if err := s.fraud.RecordReturn(ctx, customerID); err != nil {
			log.Printf("[RETURN-%s] Warning: failed to record return in fraud tracker: %v", orderTrackingID, err)
		}
	}

	// 9. Re-hold escrow for return verification
	if s.escrow != nil {
		vendorID := orderData["vendor_tracking_id"].(string)
		totalAmount := int64(0)
		if v, ok := orderData["total_amount_paisa"]; ok {
			if amt, ok := v.(int64); ok {
				totalAmount = amt
			}
		}

		returnHoldUntil := time.Now().Add(time.Duration(returnHoldHours) * time.Hour)
		if err := s.escrow.CreateReturnHold(ctx, orderTrackingID, vendorID, totalAmount, returnHoldUntil); err != nil {
			log.Printf("[RETURN-%s] Warning: failed to create return escrow hold: %v", orderTrackingID, err)
		}
	}

	// Build items summary for rider
	itemsSummary := fmt.Sprintf("%d item(s) - Reason: %s", len(returnItems), reason)
	if len(returnItems) > 0 {
		var names []string
		for _, item := range returnItems {
			if item.ProductName != "" {
				names = append(names, item.ProductName)
			}
		}
		if len(names) > 0 {
			itemsSummary = fmt.Sprintf("%d item(s): %s", len(returnItems), strings.Join(names, ", "))
		}
	}

	// 10. Emit Kafka event for delivery-service to create return gig
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
		"items_summary":        itemsSummary,
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

// UpdateStatus updates the return request status with transition validation and optimistic locking.
func (s *ReturnService) UpdateStatus(ctx context.Context, id, newStatus string) error {
	current, err := s.repo.GetReturnRequestByID(ctx, id)
	if err != nil {
		return err
	}

	if !models.IsValidReturnTransition(current.Status, newStatus) {
		return fmt.Errorf("invalid transition from '%s' to '%s'", current.Status, newStatus)
	}

	return s.repo.UpdateReturnStatus(ctx, id, newStatus, current.Status)
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

// RecordPickup records the rider's pickup with photo proof and OTP validation.
func (s *ReturnService) RecordPickup(ctx context.Context, id, photoURL, otpCode string) error {
	current, err := s.repo.GetReturnRequestByID(ctx, id)
	if err != nil {
		return err
	}

	if !models.IsValidReturnTransition(current.Status, models.ReturnStatusPickupCompleted) {
		return fmt.Errorf("cannot record pickup in '%s' status", current.Status)
	}

	// OTP validation: fetch order's delivery OTP and verify
	orderOTP, otpErr := s.repo.GetOrderOTP(ctx, current.OrderTrackingID)
	if otpErr != nil {
		log.Printf("[RETURN-%s] Warning: could not fetch OTP for validation: %v", current.OrderTrackingID, otpErr)
		// Continue without OTP validation if lookup fails (best-effort)
	} else if orderOTP != "" && otpCode != orderOTP {
		return fmt.Errorf("OTP_INVALID: provided OTP does not match order OTP")
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
// When verified=true: triggers refund to customer.
// When verified=false: creates dispute, freezes escrow, notifies admin+customer.
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

	if verified {
		// APPROVE: Trigger refund to customer and auto-complete
		if err := s.repo.VerifyByVendor(ctx, id, verified, photoURL, notes); err != nil {
			return err
		}

		if s.escrow != nil {
			orderID := current.OrderTrackingID
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
				if err := s.escrow.RefundForReturn(ctx, orderID, totalAmount); err != nil {
					log.Printf("[RETURN-%s] Warning: failed to process return refund: %v", orderID, err)
				}
			}
		}

		// Auto-complete the return after successful refund
		if err := s.repo.UpdateReturnStatus(ctx, id, models.ReturnStatusCompleted, models.ReturnStatusVerified); err != nil {
			log.Printf("[RETURN-%s] Warning: failed to auto-complete return: %v", current.OrderTrackingID, err)
		}

		s.emitEvent(ctx, "return.verified", current.OrderTrackingID, map[string]interface{}{
			"return_request_id": id,
			"order_tracking_id": current.OrderTrackingID,
			"status":            "verified",
			"timestamp":         time.Now().UnixMilli(),
		})
		s.emitEvent(ctx, "return.completed", current.OrderTrackingID, map[string]interface{}{
			"return_request_id": id,
			"order_tracking_id": current.OrderTrackingID,
			"status":            "completed",
			"timestamp":         time.Now().UnixMilli(),
		})
	} else {
		// DISPUTE: Create dispute record, freeze escrow, notify admin+customer
		if err := s.repo.VerifyByVendor(ctx, id, verified, photoURL, notes); err != nil {
			return err
		}

		// 1. Create dispute record in disputes table
		disputeID := uuid.New()
		if err := s.repo.CreateReturnDispute(ctx, disputeID, current.OrderTrackingID, current.VendorTrackingID, notes); err != nil {
			log.Printf("[RETURN-%s] Warning: failed to create dispute record: %v", current.OrderTrackingID, err)
		}

		// 2. Freeze escrow
		if s.escrow != nil {
			if err := s.escrow.FreezeForDispute(ctx, current.OrderTrackingID, disputeID); err != nil {
				log.Printf("[RETURN-%s] Warning: failed to freeze escrow: %v", current.OrderTrackingID, err)
			}
		}

		// 3. Update return request with dispute details
		if err := s.repo.UpdateReturnDisputeDetails(ctx, id, notes, disputeID.String()); err != nil {
			log.Printf("[RETURN-%s] Warning: failed to update return dispute details: %v", current.OrderTrackingID, err)
		}

		// 4. Record vendor dispute in Redis (count tracking)
		s.recordVendorDispute(ctx, current.VendorTrackingID)

		// 5. Check vendor penalty (if >= 3 disputes → 24h store ban)
		s.handleVendorPenalty(ctx, current.VendorTrackingID)

		// 6. Notify customer via WebSocket
		s.emitEvent(ctx, "returns.customer_notification", current.CustomerTrackingID, map[string]interface{}{
			"action":           "RETURN_DISPUTED",
			"order_id":         current.OrderTrackingID,
			"customer_id":      current.CustomerTrackingID,
			"vendor_id":        current.VendorTrackingID,
			"message":          "Your return has been disputed by the vendor. Admin will review within 48 hours.",
			"dispute_id":       disputeID.String(),
			"timestamp":        time.Now().UnixMilli(),
		})

		// 7. Notify admin via Kafka
		s.emitEvent(ctx, "admin.return_disputes", current.OrderTrackingID, map[string]interface{}{
			"action":           "NEW_RETURN_DISPUTE",
			"return_id":        id,
			"order_id":         current.OrderTrackingID,
			"vendor_id":        current.VendorTrackingID,
			"customer_id":      current.CustomerTrackingID,
			"dispute_id":       disputeID.String(),
			"reason":           notes,
			"photo_url":        photoURL,
			"timestamp":        time.Now().UnixMilli(),
		})

		s.emitEvent(ctx, "return.disputed", current.OrderTrackingID, map[string]interface{}{
			"return_request_id": id,
			"order_tracking_id": current.OrderTrackingID,
			"dispute_id":        disputeID.String(),
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

	// Block completion if disputed - must go through admin resolution
	if current.Status == models.ReturnStatusDisputed {
		return fmt.Errorf("DISPUTE_PENDING: return is under dispute, admin must resolve first")
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

	// Release escrow hold if one was created for this return
	if s.escrow != nil {
		if err := s.escrow.CancelForOrder(ctx, current.OrderTrackingID); err != nil {
			log.Printf("[Return] Warning: failed to release escrow on cancel for order %s: %v", current.OrderTrackingID, err)
		}
	}

	return s.repo.CancelReturn(ctx, id)
}

// SetReturnDeadline sets the 36-hour return deadline on an order.
func (s *ReturnService) SetReturnDeadline(ctx context.Context, orderTrackingID string, deliveredAt time.Time) error {
	deadline := deliveredAt.Add(time.Duration(returnWindowHours) * time.Hour)
	return s.repo.SetReturnDeadline(ctx, orderTrackingID, deadline)
}
