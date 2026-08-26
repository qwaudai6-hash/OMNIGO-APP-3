package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	middleware "github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/review/models"
	"github.com/omnigo/backend/internal/review/service"
)

type ReviewHandler struct {
	svc *service.ReviewService
}

func NewReviewHandler(svc *service.ReviewService) *ReviewHandler {
	return &ReviewHandler{svc: svc}
}

// CreateReview handles POST /api/v1/reviews
func (h *ReviewHandler) CreateReview(c *gin.Context) {
	var req models.CreateReviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload: " + err.Error()})
		return
	}

	// SECURITY: the review author is the authenticated JWT subject — a
	// body-supplied customer_tracking_id would allow forging reviews for
	// arbitrary accounts.
	req.CustomerTrackingID = middleware.GetTrackingID(c)
	if req.CustomerTrackingID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing authenticated customer"})
		return
	}

	rev, err := h.svc.CreateReview(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, rev)
}

// ListReviews handles GET /api/v1/reviews/:product_id
func (h *ReviewHandler) ListReviews(c *gin.Context) {
	productID := c.Param("product_id")
	if productID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "product_id is required"})
		return
	}

	limitStr := c.DefaultQuery("limit", "10")
	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		limit = 10
	}

	reviews, err := h.svc.ListReviewsByProduct(c.Request.Context(), productID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if reviews == nil {
		reviews = []*models.Review{}
	}
	c.JSON(http.StatusOK, reviews)
}

// GetReviewSummary handles GET /api/v1/reviews/:product_id/summary
func (h *ReviewHandler) GetReviewSummary(c *gin.Context) {
	productID := c.Param("product_id")
	if productID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "product_id is required"})
		return
	}

	summary, err := h.svc.GetReviewSummary(c.Request.Context(), productID)
	if err != nil {
		// Return zeroed summary instead of 404 — a product with no reviews
		// should still show "0.0 (0 reviews)" on the frontend.
		c.JSON(http.StatusOK, gin.H{
			"product_tracking_id": productID,
			"average_rating":      0.0,
			"total_reviews":       0,
		})
		return
	}

	c.JSON(http.StatusOK, summary)
}

// RegisterRoutes registers review endpoints on the Gin engine.
// Writes require auth (author = JWT subject); reads stay public.
func (h *ReviewHandler) RegisterRoutes(router *gin.Engine) {
	reviews := router.Group("/api/v1/reviews")
	{
		reviews.POST("/", middleware.JWTAuth(), h.CreateReview)
		reviews.GET("/:product_id", h.ListReviews)
		reviews.GET("/:product_id/summary", h.GetReviewSummary)
	}
}
