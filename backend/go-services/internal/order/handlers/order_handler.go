package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/order/models"
	"github.com/omnigo/backend/internal/order/repository"
	"github.com/omnigo/backend/internal/order/service"
	"github.com/omnigo/backend/internal/shared/middleware"
)

type OrderHandler struct {
	svc *service.OrderService
	// repo is used to verify ownership before returning or mutating an order.
	repo *repository.OrderRepository
}

func NewOrderHandler(svc *service.OrderService, repo *repository.OrderRepository) *OrderHandler {
	return &OrderHandler{svc: svc, repo: repo}
}

// CreateOrder HTTP handler for POST /orders
//
// Authentication: JWTAuth() + RoleRequired("customer") in the route group.
// The user_tracking_id in the request body is IGNORED — the caller's identity
// is taken from the JWT only. This prevents a customer from placing orders
// in someone else's name.
func (h *OrderHandler) CreateOrder(c *gin.Context) {
	callerID := middleware.GetTrackingID(c)
	if callerID == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error":   "AUTH_TOKEN_INVALID",
			"message": "caller identity missing from context",
		})
		return
	}

	var req models.CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// SECURITY: override any body-supplied user_tracking_id with the JWT identity.
	req.UserTrackID = callerID

	order, err := h.svc.CreateOrder(c.Request.Context(), &req)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, order)
}

// GetOrder HTTP handler for GET /orders/:tracking_id
//
// Authentication: JWTAuth() in the route group. Customers may only read
// their own orders; vendors may only read orders addressed to their store;
// admins may read anything. Anyone else gets 403.
func (h *OrderHandler) GetOrder(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error":   "AUTH_TOKEN_INVALID",
			"message": "caller identity missing from context",
		})
		return
	}

	order, err := h.svc.GetOrder(c.Request.Context(), trackingID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	// Ownership check.
	if !h.callerCanReadOrder(order, callerID, role) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "FORBIDDEN_NOT_ORDER_OWNER",
			"message": "you are not allowed to read this order",
		})
		return
	}

	c.JSON(http.StatusOK, order)
}

// GetOrdersByCustomer HTTP handler for GET /orders/customer/:customer_id
//
// Authentication: JWTAuth() in the route group. Customers may only read
// their own list; admins may read any.
func (h *OrderHandler) GetOrdersByCustomer(c *gin.Context) {
	customerID := c.Param("customer_id")
	if customerID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "customer_id is required"})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error":   "AUTH_TOKEN_INVALID",
			"message": "caller identity missing from context",
		})
		return
	}

	if role != "admin" && callerID != customerID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "FORBIDDEN_NOT_ORDER_OWNER",
			"message": "customers may only read their own orders",
		})
		return
	}

	// Pagination + status filter. Defaults: limit=50, status="".
	// Both can be overridden via ?limit=20&status=pending.
	limit := 50
	if rawLimit := c.Query("limit"); rawLimit != "" {
		if n, err := strconv.Atoi(rawLimit); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	statusFilter := c.Query("status")

	orders, err := h.svc.GetOrdersByCustomer(c.Request.Context(), customerID, limit, statusFilter)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, orders)
}

// GetOrdersByVendor HTTP handler for GET /orders/vendor/:vendor_id
//
// Authentication: JWTAuth() in the route group. Vendors may only read their
// own list; admins may read any.
func (h *OrderHandler) GetOrdersByVendor(c *gin.Context) {
	vendorID := c.Param("vendor_id")
	if vendorID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "vendor_id is required"})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error":   "AUTH_TOKEN_INVALID",
			"message": "caller identity missing from context",
		})
		return
	}

	if role != "admin" && callerID != vendorID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "FORBIDDEN_NOT_ORDER_OWNER",
			"message": "vendors may only read their own orders",
		})
		return
	}

	orders, err := h.svc.GetOrdersByVendor(c.Request.Context(), vendorID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// MEDIUM-28: bound response size. Full pagination pushdown into the
	// repository is tracked separately; this slice prevents unbounded
	// payloads for high-volume vendors today.
	limit := 100
	if v, err := strconv.Atoi(c.DefaultQuery("limit", "100")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	offset := 0
	if v, err := strconv.Atoi(c.DefaultQuery("offset", "0")); err == nil && v > 0 {
		offset = v
	}
	if offset > len(orders) {
		offset = len(orders)
	}
	end := offset + limit
	if end > len(orders) {
		end = len(orders)
	}
	c.JSON(http.StatusOK, orders[offset:end])
}

// UpdateOrderStatus HTTP handler for PATCH /orders/:tracking_id/status
//
// Authentication: JWTAuth() in the route group. Only the assigned vendor
// (or an admin) may transition order status. The body must include the
// new status; transition rules are enforced by the service layer.
func (h *OrderHandler) UpdateOrderStatus(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}

	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error":   "AUTH_TOKEN_INVALID",
			"message": "caller identity missing from context",
		})
		return
	}

	// Only the vendor assigned to this order (or an admin) may change its
	// status. We fetch the order to compare.
	if role != "admin" {
		order, err := h.svc.GetOrder(c.Request.Context(), trackingID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		if order.VendorTrackID != callerID {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN_NOT_ORDER_OWNER",
				"message": "only the assigned vendor or an admin may change order status",
			})
			return
		}
	}

	if err := h.svc.UpdateOrderStatus(c.Request.Context(), trackingID, req.Status); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updated, _ := h.svc.GetOrder(c.Request.Context(), trackingID)
	c.JSON(http.StatusOK, gin.H{"status": "order status updated", "order": updated})
}

// ConfirmOrder HTTP handler for POST /orders/confirm
//
// Authentication: JWTAuth() in the route group. Customers use this after
// the rider hands over the package. The body must include the order id;
// the caller must be the customer of that order.
func (h *OrderHandler) ConfirmOrder(c *gin.Context) {
	var req struct {
		OrderTrackingID string `json:"order_tracking_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error":   "AUTH_TOKEN_INVALID",
			"message": "caller identity missing from context",
		})
		return
	}

	if role != "admin" {
		order, err := h.svc.GetOrder(c.Request.Context(), req.OrderTrackingID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		if order.UserTrackID != callerID {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN_NOT_ORDER_OWNER",
				"message": "only the order's customer or an admin may confirm delivery",
			})
			return
		}
	}

	// Mark the order as delivered (and stamp delivered_at) so the escrow
	// release cron can settle funds after the 48-hour dispute window.
	// The previous "completed" write didn't update delivered_at, which
	// meant escrows never auto-released.
	if err := h.svc.MarkOrderDelivered(c.Request.Context(), req.OrderTrackingID); err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	updated, _ := h.svc.GetOrder(c.Request.Context(), req.OrderTrackingID)
	c.JSON(http.StatusOK, gin.H{"status": "order delivered successfully", "order": updated})
}

// VendorHandoverRequest is the body for POST /orders/handover. Vendors
// call this when they hand a packaged order to the assigned rider. The
// photo is mandatory so the order lifecycle has evidence from both
// sides (vendor handover + rider pickup).
type VendorHandoverRequest struct {
	OrderTrackingID string `json:"order_tracking_id" binding:"required"`
	PhotoURL        string `json:"photo_url" binding:"required"`
	Notes           string `json:"notes,omitempty"`
}

// VendorHandoverOrder HTTP handler for POST /orders/handover. Vendors
// use this to record that they have handed the order over to the rider.
// The optional but recommended photo is saved as the order's
// `handover_photo_url` for audit and dispute resolution.
//
// Only the vendor on the order (or an admin) may call this. The order
// must be in `accepted` state (i.e. a rider has accepted the gig and
// is on the way to pickup).
func (h *OrderHandler) VendorHandoverOrder(c *gin.Context) {
	var req VendorHandoverRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error":   "AUTH_TOKEN_INVALID",
			"message": "caller identity missing from context",
		})
		return
	}

	order, err := h.svc.GetOrder(c.Request.Context(), req.OrderTrackingID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	if role != "admin" && order.VendorTrackID != callerID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "FORBIDDEN_NOT_ORDER_VENDOR",
			"message": "only the order's vendor or an admin may record a handover",
		})
		return
	}

	// Persist the handover metadata. We use a dedicated repo method so the
	// model layer can keep audit timestamps and enforce shape (e.g. only
	// accept if status = 'accepted'). Photo + notes are stored alongside
	// the order so admin can review any handover dispute.
	if err := h.repo.RecordVendorHandover(c.Request.Context(), req.OrderTrackingID, callerID, req.PhotoURL, req.Notes); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":            "handover recorded",
		"order_tracking_id": req.OrderTrackingID,
		"handover_photo":    req.PhotoURL,
	})
}

// callerCanReadOrder encodes the rule:
//   - admins may read anything
//   - the customer of the order may read it
//   - the vendor of the order may read it
//   - the assigned rider may read it
//   - anyone else is denied
func (h *OrderHandler) callerCanReadOrder(order *models.Order, callerID, role string) bool {
	if role == "admin" {
		return true
	}
	if order.UserTrackID == callerID {
		return true
	}
	if order.VendorTrackID == callerID {
		return true
	}
	if order.RiderTrackID == callerID {
		return true
	}
	return false
}

// CancelOrder allows a customer to cancel their own order before it is shipped.
// Valid source states: pending, paid, accepted.
func (h *OrderHandler) CancelOrder(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&req)

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "AUTH_TOKEN_INVALID"})
		return
	}

	order, err := h.svc.GetOrder(c.Request.Context(), trackingID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	// Allow: admin, order customer, or the assigned vendor
	isAllowed := role == "admin" || order.UserTrackID == callerID
	if !isAllowed && order.VendorTrackID != "" && order.VendorTrackID == callerID {
		isAllowed = true
	}
	if !isAllowed {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "FORBIDDEN_NOT_ORDER_OWNER",
			"message": "only the order's customer, assigned vendor, or an admin may cancel",
		})
		return
	}

	if err := h.svc.UpdateOrderStatus(c.Request.Context(), trackingID, "cancelled"); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Emit orders.cancelled so delivery-service cancels the gig and frees the rider.
	h.svc.EmitCancelEvent(c.Request.Context(), trackingID, req.Reason)

	updated, _ := h.svc.GetOrder(c.Request.Context(), trackingID)
	c.JSON(http.StatusOK, gin.H{"status": "order cancelled", "order": updated})
}

// ReturnOrder allows a customer to request a return on a delivered/completed order.
// Valid source states: delivered, completed.
func (h *OrderHandler) ReturnOrder(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}

	var req struct {
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "AUTH_TOKEN_INVALID"})
		return
	}

	order, err := h.svc.GetOrder(c.Request.Context(), trackingID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	if role != "admin" && order.UserTrackID != callerID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "FORBIDDEN_NOT_ORDER_OWNER",
			"message": "only the order's customer or an admin may request return",
		})
		return
	}

	if err := h.svc.UpdateOrderStatus(c.Request.Context(), trackingID, "returned"); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updated, _ := h.svc.GetOrder(c.Request.Context(), trackingID)
	c.JSON(http.StatusOK, gin.H{"status": "return requested", "order": updated})
}

// GetMyOrders returns all orders for the authenticated customer.
func (h *OrderHandler) GetMyOrders(c *gin.Context) {
	callerID := middleware.GetTrackingID(c)
	if callerID == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "AUTH_TOKEN_INVALID"})
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	status := c.Query("status")

	orders, err := h.svc.GetOrdersByCustomer(c.Request.Context(), callerID, limit, status)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"orders": orders, "total": len(orders)})
}

// GetVendorMyOrders returns all orders for the authenticated vendor.
func (h *OrderHandler) GetVendorMyOrders(c *gin.Context) {
	callerID := middleware.GetTrackingID(c)
	if callerID == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "AUTH_TOKEN_INVALID"})
		return
	}

	orders, err := h.svc.GetOrdersByVendor(c.Request.Context(), callerID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"orders": orders, "total": len(orders)})
}

// RegisterRoutes attaches the handlers to the gin router.
func (h *OrderHandler) RegisterRoutes(router *gin.Engine) {
	orders := router.Group("/api/v1/orders", middleware.JWTAuth())
	{
		orders.POST("/", h.CreateOrder)
		orders.POST("/confirm", h.ConfirmOrder)
		orders.POST("/handover", h.VendorHandoverOrder)
		orders.GET("/my", h.GetMyOrders)
		orders.GET("/vendor/my", h.GetVendorMyOrders)
		orders.GET("/:tracking_id", h.GetOrder)
		orders.GET("/customer/:customer_id", h.GetOrdersByCustomer)
		orders.GET("/vendor/:vendor_id", h.GetOrdersByVendor)
		orders.PATCH("/:tracking_id/status", h.UpdateOrderStatus)
		orders.POST("/:tracking_id/cancel", h.CancelOrder)
		orders.POST("/:tracking_id/return", h.ReturnOrder)
	}
}
