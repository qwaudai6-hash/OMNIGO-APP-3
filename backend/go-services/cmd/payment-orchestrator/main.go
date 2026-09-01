package main

import (
	"context"
	"fmt"
	"github.com/getsentry/sentry-go/gin"
	"github.com/omnigo/backend/internal/shared/telemetry"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/omnigo/backend/internal/admin"
	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/ledger"
	orderRepo "github.com/omnigo/backend/internal/order/repository"
	stripeClientPkg "github.com/omnigo/backend/internal/payment/stripe"
	"github.com/omnigo/backend/internal/payment/payfast"
	paymentRepo "github.com/omnigo/backend/internal/payment/repository"
	paymentservice "github.com/omnigo/backend/internal/payment/service"
	"github.com/omnigo/backend/internal/payment_orchestrator"
	"github.com/omnigo/backend/internal/payment_orchestrator/fraud"
	"github.com/omnigo/backend/internal/payment_orchestrator/handlers"
	payfastSvc "github.com/omnigo/backend/internal/payment_orchestrator/service"
	"github.com/omnigo/backend/internal/payment_orchestrator/workers"
	"github.com/omnigo/backend/internal/shared/cache"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/shared/health"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/shared/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

func main() {
	// 1. Load Configuration
	cfg := config.LoadConfig(".")
	defer telemetry.InitSentry(cfg.SentryDSN, cfg.Env)()
	cfg.Port = config.EnvPort(9006)

	ctx := context.Background()

	// 2. Initialize Infrastructure
	database.MigrateUpOrFail(ctx, cfg.DBWriterDSN)
	db, err := database.NewDB(ctx, cfg.DBWriterDSN, cfg.DBReaderDSN)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	redisClient, err := cache.NewRedisClient(ctx, cfg.RedisAddrs)
	if err != nil {
		log.Printf("Warning: Failed to initialize redis: %v", err)
	} else {
		defer redisClient.Close()
	}

	kafkaClient, err := messaging.NewKafkaClient(cfg.KafkaBrokers, "payment-orchestrator")
	if err != nil {
		log.Printf("Warning: Failed to initialize kafka: %v", err)
	} else {
		defer kafkaClient.Close()
	}

	// Initialize TigerBeetle client if enabled
	var tbService *ledger.TBService
	if os.Getenv("TB_ENABLED") == "true" {
		envAddrs := os.Getenv("TIGERBEETLE_ADDRESSES")
		if envAddrs == "" {
			log.Fatal("FATAL: TB_ENABLED=true but TIGERBEETLE_ADDRESSES is not set")
		}
		tbAddrs := strings.Split(envAddrs, ",")
		var err error
		tbService, err = ledger.NewTBService(tbAddrs)
		if err != nil {
			log.Printf("Warning: Failed to initialize TigerBeetle client: %v", err)
		} else {
			defer tbService.Close()
			log.Println("TigerBeetle client initialized successfully")
		}
	}

	// 3. Initialize Domain Services
	ledgerSvc := ledger.NewService(db.Writer, tbService)
	escrowSvc := escrow.NewService(db.Writer, ledgerSvc)
	calculator := payment_orchestrator.NewCommissionCalculator(db.Writer)

	// 4. Initialize Handlers
	codHandler := handlers.NewCODHandler(db.Writer, ledgerSvc, escrowSvc, calculator)
	disputeHandler := handlers.NewDisputeHandler(db.Writer, escrowSvc)
	vendorHandler := handlers.NewVendorHandler(db.Writer)

	cardVaultService := payfastSvc.NewCardVaultService(db.Writer)
	cardVaultHandler := handlers.NewCardVaultHandler(cardVaultService)

	var rdb redis.UniversalClient
	if redisClient != nil {
		rdb = redisClient.Client
	}
	fraudDetector := fraud.NewDetector(rdb, db.Writer)

	// Stripe split handler — full lifecycle (checkout + webhook + refund + ledger split).
	stripeClient := stripeClientPkg.NewClientFromEnv()
	var stripeSplitHandler *handlers.StripeSplitHandler
	if stripeClient.IsConfigured() {
		stripeService := payfastSvc.NewStripeService(
			db.Writer,
			ledgerSvc,
			escrowSvc,
			calculator,
			stripeClient,
		)
		stripeService.SetFraudDetector(fraudDetector)
		stripeSplitHandler = handlers.NewStripeSplitHandler(stripeService)
		log.Println("Stripe split handler initialized (checkout + webhook + refund)")
	}

	// PayFast split handler
	payfastClient := payfast.NewClientFromEnv()
	var payfastSplitHandler *handlers.PayFastSplitHandler
	if payfastClient.IsConfigured() {
		payfastService := payfastSvc.NewPayFastService(
			db.Writer,
			ledgerSvc,
			escrowSvc,
			calculator,
			payfastClient,
		)
		payfastService.SetVault(cardVaultService)
		payfastService.SetFraudDetector(fraudDetector)
		payfastSplitHandler = handlers.NewPayFastSplitHandler(payfastService)
		log.Println("PayFast split handler initialized")
	}

	// 5. Setup Router
	router := gin.Default()
	router.RedirectTrailingSlash = false
	router.Use(sentrygin.New(sentrygin.Options{Repanic: true}))

	// Security middleware
	router.Use(middleware.CORS())
	if redisClient != nil {
		router.Use(middleware.RateLimit(redisClient.Client, 100, time.Minute))
	}

	// Health check
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status":  "ok",
			"service": "payment-orchestrator",
			"version": "1.0.0",
		})
	})
	router.GET("/readyz", health.DBPool(db.Writer))

	// Ledger balance endpoint (admin only)
	router.GET("/api/v1/ledger/balance/:account", middleware.AdminRequired(), func(c *gin.Context) {
		account := c.Param("account")
		balance, err := ledgerSvc.GetBalance(c.Request.Context(), ledger.Account(account))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"account": account,
			"balance": balance,
		})
	})

	// Ledger entries by reference
	router.GET("/api/v1/ledger/entries/:type/:id", middleware.AdminRequired(), func(c *gin.Context) {
		refType := c.Param("type")
		refID := c.Param("id")
		entries, err := ledgerSvc.GetEntriesByReference(c.Request.Context(), refType, refID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"entries": entries})
	})

	// Escrow holds by vendor. NOTE: served under /api/v1/payments/escrow so
	// the public gateway (/api/v1/payments → payment-orchestrator) can reach
	// it — there is no /api/v1/escrow route at the gateway.
	router.GET("/api/v1/payments/escrow/holds/:vendor_id", middleware.AdminRequired(), func(c *gin.Context) {
		vendorID := c.Param("vendor_id")
		holds, err := escrowSvc.GetHoldsByVendor(c.Request.Context(), vendorID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"holds": holds})
	})

	// Register domain handlers
	codHandler.RegisterRoutes(router)
	disputeHandler.RegisterRoutes(router)
	vendorHandler.RegisterRoutes(router)
	cardVaultHandler.RegisterRoutes(router)

	// JazzCash / EasyPaisa hosted-checkout flow: initiate + callback.
	// NOTE: there is intentionally NO /status endpoint here — payment state
	// arrives asynchronously at the callback; clients poll order status.
	mobileWalletHandler := handlers.NewMobileWalletHandler(
		paymentservice.NewOrchestrator(),
		paymentRepo.NewRepository(db.Writer),
		orderRepo.NewOrderRepository(db.Writer, nil),
		calculator,
		db.Writer,
		rdb,
	)
	mobileWalletHandler.RegisterRoutes(router)

	// Prometheus Metrics
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Register Stripe endpoints (checkout + webhook + refund)
	if stripeSplitHandler != nil {
		stripeSplitHandler.RegisterRoutes(router)
	}

	// Register PayFast split endpoints (payment + 3ds_callback + ipn).
	// NOTE: the hosted-checkout postback lives in order-service wallet at
	// /api/v1/wallet/payfast/callback — NOT here.
	if payfastSplitHandler != nil {
		payfastSplitHandler.RegisterRoutes(router)
		log.Println("PayFast split endpoints registered: /api/v1/payments/payfast/{payment,3ds_callback,ipn}")
	}

	// Financial Reconciliation Endpoint (admin only)
	reconWorker := workers.NewReconciliationWorker(db.Writer, ledgerSvc, rdb)
	router.POST("/api/v1/finance/reconcile", middleware.AdminRequired(), func(c *gin.Context) {
		res, err := reconWorker.RunReconciliation(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	})

	// 1LINK / 1IBFT Corporate Payout CSV Export Endpoint (admin only)
	payoutExportSvc := admin.NewPayoutExportService(db.Writer)
	router.GET("/api/v1/finance/payouts/export-1ibft", middleware.AdminRequired(), func(c *gin.Context) {
		batchID := c.Query("batch_id")
		csvBytes, filename, err := payoutExportSvc.Export1IBFTCSVPending(c.Request.Context(), batchID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
		c.Data(http.StatusOK, "text/csv", csvBytes)
	})

	// 6. Start Background Workers
	workerCtx, workerCancel := context.WithCancel(context.Background())
	go workers.NewEscrowReleaserWorker(escrowSvc, rdb).Start(workerCtx)
	go workers.NewPayoutWorker(db.Writer, ledgerSvc, rdb).Start(workerCtx)
	go workers.NewSettlementWorker(db.Writer, ledgerSvc, escrowSvc, calculator, payfastClient, rdb).Start(workerCtx)
	go workers.NewStripeReplayWorker(db.Writer, stripeClient).Start(workerCtx)
	go reconWorker.Start(workerCtx)

	// 7. Start Server
	srv := &http.Server{
		Addr:    config.BindHost() + ":" + strconv.Itoa(cfg.Port),
		Handler: router,
	}

	go func() {
		log.Printf("Starting Payment Orchestrator on port %d", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Listen and serve error: %v", err)
		}
	}()

	// 8. Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down Payment Orchestrator...")

	workerCancel() // Stop background workers

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctxTimeout); err != nil {
		log.Fatal("Server forced to shutdown: ", err)
	}

	log.Println("Payment Orchestrator exited")
}
