package main

import (
	"context"
	"github.com/getsentry/sentry-go/gin"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/redis/go-redis/v9"

	"github.com/omnigo/backend/internal/admin"
	"github.com/omnigo/backend/internal/analytics"
	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/shared/health"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/shared/middleware"
)

func parsePagination(c *gin.Context) (limit, offset int) {
	limit, _ = strconv.Atoi(c.Query("limit"))
	offset, _ = strconv.Atoi(c.Query("offset"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func main() {
	ctx := context.Background()

	// Initialize PostgreSQL
	pgURL := os.Getenv("DATABASE_URL")
	if pgURL == "" {
		log.Fatal("DATABASE_URL env var is required")
	}
	database.MigrateUpOrFail(ctx, pgURL)
	dbPool, err := pgxpool.New(ctx, pgURL)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer dbPool.Close()

	// Initialize Neo4j Driver (graceful — admin works without Neo4j)
	neo4jURI := os.Getenv("NEO4J_URI")
	neo4jUser := os.Getenv("NEO4J_USER")
	neo4jPass := os.Getenv("NEO4J_PASSWORD")

	var driver neo4j.DriverWithContext
	driver, err = neo4j.NewDriverWithContext(neo4jURI, neo4j.BasicAuth(neo4jUser, neo4jPass, ""))
	if err != nil {
		log.Printf("Warning: Neo4j driver init failed (graph audit disabled): %v", err)
	} else {
		defer driver.Close(context.Background())
	}

	// Initialize TigerBeetle
	tbAddrs := []string{}
	if envAddr := os.Getenv("TIGERBEETLE_ADDRESSES"); envAddr != "" {
		tbAddrs = strings.Split(envAddr, ",")
	} else if envAddr := os.Getenv("TIGERBEETLE_ADDR"); envAddr != "" {
		tbAddrs = []string{envAddr}
	}
	var tbService *ledger.TBService
	if len(tbAddrs) > 0 {
		tbService, err = ledger.NewTBService(tbAddrs)
		if err != nil {
			log.Printf("Warning: TigerBeetle init failed, ledger KPIs will be unavailable: %v", err)
			tbService = nil
		} else {
			defer tbService.Close()
		}
	} else {
		log.Println("Warning: TIGERBEETLE_ADDRESSES not set, ledger KPIs will be unavailable")
	}

	// Init Services
	ledgerSvc := ledger.NewService(dbPool, tbService)
	// Single read-write pool today: the same pool is wired as both writer and
	// reader. When a master-replica split lands, pass the replica pool as the
	// second argument — all reads go through dbReader, writes through dbWriter.
	adminService := admin.NewAdminSurveillanceService(dbPool, dbPool, driver, "neo4j", ledgerSvc)
	verificationService := admin.NewVerificationService(dbPool)

	// Initialize Kafka client for emitting payment.keys.updated events so
	// downstream services can hot-reload. Best-effort: if Kafka is offline
	// the api-keys endpoints still work, they just won't notify consumers.
	kafkaBrokers := []string{}
	if kb := os.Getenv("KAFKA_BROKERS"); kb != "" {
		kafkaBrokers = strings.Split(kb, ",")
	}
	var kafkaClient *messaging.KafkaClient
	if len(kafkaBrokers) > 0 {
		if kc, kerr := messaging.NewKafkaClient(kafkaBrokers, "admin-service"); kerr != nil {
			log.Printf("Warning: Kafka unavailable for api-key notifications: %v", kerr)
		} else {
			kafkaClient = kc
			defer kafkaClient.Close()
		}
	} else {
		log.Println("Warning: KAFKA_BROKERS not set, api-key notifications disabled")
	}

	// Admin API key service requires an encryption passphrase. We refuse to
	// start without it — no insecure fallback. See internal/admin/api_keys.go.
	encPassphrase := os.Getenv("ADMIN_API_KEY_ENCRYPTION_KEY")
	if encPassphrase == "" {
		log.Fatal("FATAL: ADMIN_API_KEY_ENCRYPTION_KEY is not set. Refusing to start. Generate with: openssl rand -base64 32")
	}
	apiKeyService := admin.NewAPIKeyService(dbPool, encPassphrase, kafkaClient)
	trustProxy := os.Getenv("OMNIGO_TRUST_PROXY") == "true"

	clickhouseAddr := os.Getenv("CLICKHOUSE_ADDR")
	if clickhouseAddr == "" {
		log.Println("Warning: CLICKHOUSE_ADDR not set, analytics disabled")
	}
	var analyticsService *analytics.AnalyticsService
	if clickhouseAddr != "" {
		analyticsService, err = analytics.NewAnalyticsService(strings.Split(clickhouseAddr, ","))
		if err != nil {
			log.Printf("Warning: Analytics service init failed (ClickHouse offline): %v", err)
			analyticsService = nil
		}
	}

	// Initialize Redis for rate limiting (graceful degradation if unavailable)
	redisAddr := os.Getenv("REDIS_ADDRS")
	if redisAddr == "" {
		redisAddr = os.Getenv("REDIS_ADDR")
	}
	if redisAddr == "" {
		redisAddr = os.Getenv("REDIS_URL")
	}

	var rdb redis.UniversalClient
	if redisAddr != "" && !strings.HasPrefix(redisAddr, "redis://") && !strings.HasPrefix(redisAddr, "rediss://") {
		rdb = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs: []string{redisAddr},
		})
		if err := rdb.Ping(context.Background()).Err(); err != nil {
			log.Printf("Warning: Redis unavailable for admin rate limiting: %v", err)
			rdb = nil
		}
	} else {
		log.Println("Warning: Standalone/URL Redis detected for admin-service, bypassing cluster rate limiter")
		rdb = nil
	}

	r := gin.Default()
	r.Use(sentrygin.New(sentrygin.Options{Repanic: true}))

	// Security audit middleware: inject Correlation Trace ID
	r.Use(func(c *gin.Context) {
		traceID := c.GetHeader("X-Trace-Id")
		if traceID == "" {
			traceID = "SEC-TRC-" + uuid.NewString()[:8]
		}
		cctx := context.WithValue(c.Request.Context(), "trace_id", traceID)
		c.Request = c.Request.WithContext(cctx)
		log.Printf("[SECURITY-AUDIT] [TraceID: %s] Admin Access: %s %s", traceID, c.Request.Method, c.Request.URL.Path)
		c.Next()
	})

	// ── Admin API (JWT + role=admin required + rate limit) ──────
	adminRoutes := r.Group("/api/v1/admin")
	adminRoutes.Use(middleware.RateLimit(rdb, 100, time.Minute))
	adminRoutes.Use(middleware.AdminRequired())
	{
		// Lineage endpoints (supporting both compact and full sweeps for any UTID)
		adminRoutes.GET("/lineage/:order_id", func(c *gin.Context) {
			orderID := c.Param("order_id")
			report, err := adminService.GetCompleteOrderLineage(c.Request.Context(), orderID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, report)
		})

		adminRoutes.GET("/lineage/:order_id/full", func(c *gin.Context) {
			orderID := c.Param("order_id")
			report, err := adminService.GetFullOrderLineage(c.Request.Context(), orderID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, report)
		})

		adminRoutes.GET("/lineage/full/:order_id", func(c *gin.Context) {
			orderID := c.Param("order_id")
			report, err := adminService.GetFullOrderLineage(c.Request.Context(), orderID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, report)
		})

		// Ride-hailing lineage: RIDE- sessions are a separate domain from the
		// e-commerce order chain and get their own trace (participants, bid
		// marketplace trail, fare-split ledger entries).
		adminRoutes.GET("/lineage/ride/:id", func(c *gin.Context) {
			report, err := adminService.GetRideLineage(c.Request.Context(), c.Param("id"))
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, report)
		})

		// Finance Endpoints
		adminRoutes.GET("/finance/ledger-kpis", func(c *gin.Context) {
			kpis, err := adminService.GetLedgerKPIs(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, kpis)
		})

		adminRoutes.GET("/finance/payments", func(c *gin.Context) {
			limit, _ := strconv.Atoi(c.Query("limit"))
			if limit <= 0 || limit > 100 {
				limit = 50 // default to recent 50
			}
			payments, err := adminService.GetRecentPayments(c.Request.Context(), limit)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"payments": payments})
		})

		adminRoutes.GET("/finance/daily-revenue", func(c *gin.Context) {
			days, _ := strconv.Atoi(c.Query("days"))
			if days <= 0 {
				days = 7
			}
			method := c.Query("payment_method")
			data, err := adminService.GetDailyRevenue(c.Request.Context(), days, method)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"daily_revenue": data})
		})

		// PayFast Transaction Collection & Analytics (Admin Panel)
		adminRoutes.GET("/finance/payfast/summary", func(c *gin.Context) {
			stats, err := adminService.GetPayFastTransactionSummary(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, stats)
		})

		adminRoutes.GET("/finance/payfast/transactions", func(c *gin.Context) {
			status := c.DefaultQuery("status", "all")
			limit, offset := parsePagination(c)
			txns, total, err := adminService.GetPayFastTransactions(c.Request.Context(), status, limit, offset)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"transactions": txns,
				"total":        total,
				"limit":        limit,
				"offset":       offset,
			})
		})

		// Payment API key management (admin-only; encrypted at rest).
		// Flutter modal in admin_finance_screen.dart calls these.
		adminRoutes.GET("/finance/api-keys", func(c *gin.Context) {
			records, err := apiKeyService.List(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if records == nil {
				records = []admin.APIKeyRecord{}
			}
			c.JSON(http.StatusOK, gin.H{"api_keys": records})
		})

		adminRoutes.POST("/finance/api-keys", func(c *gin.Context) {
			var body struct {
				Provider string `json:"provider" binding:"required"`
				KeyName  string `json:"key_name" binding:"required"`
				Value    string `json:"value" binding:"required"`
			}
			if err := c.ShouldBindJSON(&body); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			actorID := admin.ExtractActorID(c.GetString("tracking_id"))
			actorIP := admin.ExtractClientIP(c.GetHeader("X-Forwarded-For"), c.Request.RemoteAddr, trustProxy)
			result, err := apiKeyService.Set(c.Request.Context(), body.Provider, body.KeyName, body.Value, actorID, actorIP)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, result)
		})

		adminRoutes.DELETE("/finance/api-keys/:provider/:key_name", func(c *gin.Context) {
			provider := c.Param("provider")
			keyName := c.Param("key_name")
			actorID := admin.ExtractActorID(c.GetString("tracking_id"))
			actorIP := admin.ExtractClientIP(c.GetHeader("X-Forwarded-For"), c.Request.RemoteAddr, trustProxy)
			fp, err := apiKeyService.Delete(c.Request.Context(), provider, keyName, actorID, actorIP)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"deleted": true, "fingerprint": fp})
		})

		// User verification endpoints
		adminRoutes.GET("/users/pending", func(c *gin.Context) {
			limit, offset := parsePagination(c)
			users, total, err := adminService.ListPendingVerifications(c.Request.Context(), limit, offset)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"pending_users": users, "total": total, "limit": limit, "offset": offset})
		})

		adminRoutes.PATCH("/users/:tracking_id/approve", func(c *gin.Context) {
			trackingID := c.Param("tracking_id")
			if err := adminService.ApproveUser(c.Request.Context(), trackingID); err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "success", "message": "user verified successfully", "tracking_id": trackingID})
		})

		adminRoutes.GET("/users", func(c *gin.Context) {
			role := c.Query("role")
			limit, offset := parsePagination(c)
			users, total, err := adminService.ListAllUsers(c.Request.Context(), role, limit, offset)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"users": users, "total": total, "limit": limit, "offset": offset})
		})

		// Dispute Resolution endpoints
		adminRoutes.GET("/disputes", func(c *gin.Context) {
			disputes, err := adminService.GetDisputedOrders(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"disputes": disputes})
		})

		adminRoutes.POST("/disputes/:order_id/resolve", func(c *gin.Context) {
			orderID := c.Param("order_id")
			var req struct {
				Decision string `json:"decision" binding:"required"` // vendor_guilty, rider_guilty, customer_guilty
			}
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			if err := adminService.ResolveDispute(c.Request.Context(), orderID, req.Decision); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "resolved", "order_id": orderID})
		})

		// NOTE: GET /lineage/:order_id/full is registered once above (line ~190).
		// A duplicate registration here previously caused a gin panic at startup
		// ("handlers are already registered for path").

		// ── KYC/KYB automated verification endpoints ───────────────
		adminRoutes.GET("/verifications/pending", func(c *gin.Context) {
			limit, offset := parsePagination(c)
			users, total, err := verificationService.ListPendingVerifications(c.Request.Context(), limit, offset)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"pending_users": users, "total": total, "limit": limit, "offset": offset})
		})

		adminRoutes.GET("/verifications/:tracking_id", func(c *gin.Context) {
			trackingID := c.Param("tracking_id")
			detail, err := verificationService.GetVerification(c.Request.Context(), trackingID)
			if err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, detail)
		})

		adminRoutes.POST("/verifications/:tracking_id/submit", func(c *gin.Context) {
			trackingID := c.Param("tracking_id")
			detail, err := verificationService.SubmitVerification(c.Request.Context(), trackingID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, detail)
		})

		adminRoutes.POST("/verifications/:tracking_id/approve", func(c *gin.Context) {
			trackingID := c.Param("tracking_id")
			var req struct {
				Reason string `json:"reason"`
			}
			_ = c.ShouldBindJSON(&req)
			if err := verificationService.ApproveVerification(c.Request.Context(), trackingID, req.Reason); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "approved", "tracking_id": trackingID})
		})

		adminRoutes.POST("/verifications/:tracking_id/reject", func(c *gin.Context) {
			trackingID := c.Param("tracking_id")
			var req struct {
				Reason string `json:"reason" binding:"required"`
			}
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			if err := verificationService.RejectVerification(c.Request.Context(), trackingID, req.Reason); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "rejected", "tracking_id": trackingID})
		})

		// ── AI Security & Self-Healing Control Center Proxy Endpoints ─────
		aiHost := os.Getenv("AI_ENGINE_URL")
		if aiHost == "" {
			log.Println("Warning: AI_ENGINE_URL not set, AI audit endpoints will return 503")
		}
		aiClient := &http.Client{Timeout: 10 * time.Second}
		adminRoutes.GET("/ai/audit-overview", func(c *gin.Context) {
			if aiHost == "" {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI_ENGINE_URL not configured"})
				return
			}
			req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, aiHost+"/api/v1/ai/audit/overview", nil)
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": "failed to build AI engine request: " + err.Error()})
				return
			}
			resp, err := aiClient.Do(req)
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI Engine auditor offline: " + err.Error()})
				return
			}
			defer resp.Body.Close()
			contentType := resp.Header.Get("Content-Type")
			if contentType == "" {
				contentType = "application/json"
			}
			c.DataFromReader(resp.StatusCode, resp.ContentLength, contentType, resp.Body, nil)
		})

		adminRoutes.POST("/ai/auto-heal", func(c *gin.Context) {
			if aiHost == "" {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI_ENGINE_URL not configured"})
				return
			}
			req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, aiHost+"/api/v1/ai/audit/auto-heal", c.Request.Body)
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": "failed to build AI engine request: " + err.Error()})
				return
			}
			if ct := c.ContentType(); ct != "" {
				req.Header.Set("Content-Type", ct)
			}
			resp, err := aiClient.Do(req)
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI Engine auto-heal offline: " + err.Error()})
				return
			}
			defer resp.Body.Close()
			contentType := resp.Header.Get("Content-Type")
			if contentType == "" {
				contentType = "application/json"
			}
			c.DataFromReader(resp.StatusCode, resp.ContentLength, contentType, resp.Body, nil)
		})

		// ── Analytics / Geospatial Heatmap ───────────────
		adminRoutes.GET("/analytics/demand-heatmap", func(c *gin.Context) {
			if analyticsService == nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Analytics database unavailable"})
				return
			}

			// Parse bounding box or radius params
			minLat, _ := strconv.ParseFloat(c.DefaultQuery("minLat", "-90.0"), 64)
			maxLat, _ := strconv.ParseFloat(c.DefaultQuery("maxLat", "90.0"), 64)
			minLng, _ := strconv.ParseFloat(c.DefaultQuery("minLng", "-180.0"), 64)
			maxLng, _ := strconv.ParseFloat(c.DefaultQuery("maxLng", "180.0"), 64)

			heatmap, err := analyticsService.GetRiderDemandHeatmap(c.Request.Context(), minLat, maxLat, minLng, maxLng)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"heatmap": heatmap})
		})
	}

	// ── Health check (public) ────────────────────────────────────
	// Public geocode proxy (Nominatim): frontend reverse-geocodes
	// through this so User-Agent / rate limits stay server-side.
	geoService := admin.NewGeoService()
	r.GET("/api/v1/geo/reverse", middleware.RateLimit(rdb, 30, time.Minute), geoService.ReverseGeocode)

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "admin-service"})
	})
	r.GET("/readyz", health.DBPool(dbPool))

	port := os.Getenv("PORT")
	if port == "" {
		port = "9007" // aligned with monolith child-port registry
	}

	srv := &http.Server{Addr: config.BindHost() + ":" + port, Handler: r}

	go func() {
		log.Printf("Starting Admin Surveillance Engine on port %s...", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Listen error: %v", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down admin service...")

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctxTimeout); err != nil {
		log.Fatal("Server forced to shutdown: ", err)
	}
	log.Println("Admin service exiting")
}
