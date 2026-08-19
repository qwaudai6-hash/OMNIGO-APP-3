package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	sharedAuth "github.com/omnigo/backend/internal/shared/auth"
	"github.com/omnigo/backend/internal/vendorstore/service"
)

type VendorMetricsHandler struct {
	svc *service.VendorService
}

func NewVendorMetricsHandler(svc *service.VendorService) *VendorMetricsHandler {
	return &VendorMetricsHandler{svc: svc}
}

// extractVendorTrackingID parses the Authorization header using shared JWT parser.
func (h *VendorMetricsHandler) extractVendorTrackingID(c *gin.Context) (string, error) {
	return sharedAuth.ExtractTrackingIDFromHeader(c.GetHeader("Authorization"))
}

// GetMetrics handles GET /api/v1/vendor/metrics
func (h *VendorMetricsHandler) GetMetrics(c *gin.Context) {
	vendorID, err := h.extractVendorTrackingID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	metrics, err := h.svc.GetVendorMetrics(c.Request.Context(), vendorID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, metrics)
}
