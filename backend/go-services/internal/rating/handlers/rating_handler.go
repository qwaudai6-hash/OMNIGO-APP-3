package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/shared/middleware"
)

// RatingHandler is a thin wrapper around the existing `reviews` table
// exposed at `/api/v1/ratings/*`. The Flutter app references
// `ApiEndpoints.ratingCreate()` and `ratingForUser()` so the gateway
// (`/api/v1/ratings` → ORDER_SERVICE_URL) routes here.
//
// Authenticity: every endpoint writes/reads the same `reviews` rows as
// the product-service's review handler, so a customer can both review
// a product (via /api/v1/reviews) and rate the vendor they bought it
// from (via /api/v1/ratings). The single source of truth is the
// `reviews` table; we just expose a different URL surface.
type RatingHandler struct {
	db *pgxpool.Pool
}

func NewRatingHandler(db *pgxpool.Pool) *RatingHandler {
	return &RatingHandler{db: db}
}

// CreateRatingRequest is the body for POST /api/v1/ratings/.
//
// It mirrors the reviews-table shape (product_tracking_id, rating,
// comment) and additionally carries an optional `target_user_tracking_id`
// so the customer can rate the vendor they bought from. If the target
// is omitted, the rating is still inserted into the reviews table
// (existing behaviour).
type CreateRatingRequest struct {
	ProductTrackingID    string `json:"product_tracking_id" binding:"required"`
	TargetUserTrackingID string `json:"target_user_tracking_id,omitempty"`
	Rating               int    `json:"rating" binding:"required,min=1,max=5"`
	Comment              string `json:"comment,omitempty"`
}

// CreateRating handles POST /api/v1/ratings/.
//
// Auth: any logged-in user. The caller's tracking_id is taken from the
// JWT (not the body) so a customer can't rate on behalf of someone
// else.
func (h *RatingHandler) CreateRating(c *gin.Context) {
	callerID := middleware.GetTrackingID(c)
	if callerID == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error":   "AUTH_TOKEN_INVALID",
			"message": "caller identity missing from context",
		})
		return
	}

	var req CreateRatingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// If the user passed a target (typically the vendor), sanity-check
	// that the product was actually bought from them. Otherwise we leak
	// a soft-DOS vector where anyone can rate anyone.
	if req.TargetUserTrackingID != "" {
		var okCount int
		row := h.db.QueryRow(c.Request.Context(),
			`SELECT COUNT(*) FROM orders
			 WHERE customer_tracking_id = $1
			   AND vendor_tracking_id = $2
			   AND order_tracking_id IS NOT NULL
			 LIMIT 1`,
			callerID, req.TargetUserTrackingID)
		if err := row.Scan(&okCount); err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if okCount == 0 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN_NOT_CUSTOMER",
				"message": "you can only rate vendors you have placed an order with",
			})
			return
		}
	}

	// Insert into the review table. The unique constraint
	// (product_tracking_id, user_tracking_id) enforces one rating per
	// product per customer; ON CONFLICT lets us update an existing
	// rating (rate-it-again behaviour).
	_, err := h.db.Exec(c.Request.Context(),
		`INSERT INTO reviews (product_tracking_id, user_tracking_id, rating, comment)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (product_tracking_id, user_tracking_id)
		 DO UPDATE SET rating = $3, comment = $4, updated_at = NOW()`,
		req.ProductTrackingID, callerID, req.Rating, req.Comment,
	)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":                  "rating recorded",
		"product_tracking_id":     req.ProductTrackingID,
		"target_user_tracking_id": req.TargetUserTrackingID,
		"rating":                  req.Rating,
	})
}

// ListRatingsForUser handles GET /api/v1/ratings/:tracking_id. Returns
// the average rating and total count the user has received across all
// products they sold (i.e. the vendor's overall rating).
func (h *RatingHandler) ListRatingsForUser(c *gin.Context) {
	targetUserTrackingID := c.Param("tracking_id")
	if targetUserTrackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}

	// Aggregate over the reviews table joined to products on the same
	// vendor. Only ratings attached to a product the vendor sells count
	// toward their overall score.
	var avg float64
	var count int64
	row := h.db.QueryRow(c.Request.Context(),
		`SELECT COALESCE(AVG(r.rating), 0.0), COUNT(r.id)
		 FROM reviews r
		 INNER JOIN products p ON p.product_tracking_id = r.product_tracking_id
		 WHERE p.vendor_tracking_id = $1`,
		targetUserTrackingID)
	if err := row.Scan(&avg, &count); err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user_tracking_id": targetUserTrackingID,
		"average_rating":   avg,
		"total_ratings":    count,
	})
}

// RegisterRoutes attaches the ratings endpoints to the gin router.
func (h *RatingHandler) RegisterRoutes(router *gin.Engine) {
	ratings := router.Group("/api/v1/ratings")
	{
		ratings.POST("/", h.CreateRating)
		ratings.GET("/:tracking_id", h.ListRatingsForUser)
	}
}
