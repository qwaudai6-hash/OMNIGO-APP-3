package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/getsentry/sentry-go/gin"
	"github.com/omnigo/backend/internal/shared/telemetry"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	chatHandler "github.com/omnigo/backend/internal/chat/handlers"
	chatRepo "github.com/omnigo/backend/internal/chat/repository"
	chatSvc "github.com/omnigo/backend/internal/chat/service"
	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/order/handlers"
	"github.com/omnigo/backend/internal/order/repository"
	"github.com/omnigo/backend/internal/order/service"
	paymentHandlers "github.com/omnigo/backend/internal/payment/handlers"
	paymentRepo "github.com/omnigo/backend/internal/payment/repository"
	paymentSvc "github.com/omnigo/backend/internal/payment/service"
	payment_orchestrator "github.com/omnigo/backend/internal/payment_orchestrator"
	"github.com/omnigo/backend/internal/product/pb"
	ratingHandlers "github.com/omnigo/backend/internal/rating/handlers"
	"github.com/omnigo/backend/internal/shared/cache"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/shared/health"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/shared/security"
	walletHandler "github.com/omnigo/backend/internal/wallet/handler"
	walletSvc "github.com/omnigo/backend/internal/wallet/service"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	// 1. Load Configuration
	cfg := config.LoadConfig(".")
	defer telemetry.InitSentry(cfg.SentryDSN, cfg.Env)()
	cfg.Port = config.EnvPort(9005)

	ctx := context.Background()

	// Initialize OpenTelemetry
	shutdown, err := telemetry.InitProvider("order-service", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if err != nil {
		log.Printf("Warning: Failed to initialize OpenTelemetry: %v", err)
	} else {
		defer func() {
			if err := shutdown(context.Background()); err != nil {
				log.Printf("Failed to shutdown OpenTelemetry: %v", err)
			}
		}()
	}

	// 2. Apply pending migrations, then initialize Infrastructure
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

	kafkaClient, err := messaging.NewKafkaClient(cfg.KafkaBrokers, "order-service")
	if err != nil {
		log.Printf("Warning: Failed to initialize kafka: %v", err)
	} else {
		defer kafkaClient.Close()
	}

	// 3. Initialize Domain Layers
	productServiceURL := os.Getenv("PRODUCT_SERVICE_URL")
	if productServiceURL == "" {
		log.Fatal("PRODUCT_SERVICE_URL env var is required (e.g. http://product-service:9001)")
	}
	repo := repository.NewOrderRepository(db.Writer, db.Reader)

	// Build internal HMAC signer for product-service calls. nil in dev
	// (no INTERNAL_API_SECRET set); populated in production.
	var internalSigner *security.InternalSigner
	if secret := os.Getenv("INTERNAL_API_SECRET"); secret != "" {
		internalSigner = security.NewInternalSigner(secret, "order-service")
	}

	// Initialize gRPC connection to product-service
	productGRPCAddr := os.Getenv("PRODUCT_SERVICE_GRPC_ADDR")
	if productGRPCAddr == "" {
		productGRPCAddr = "localhost:50052"
	}
	grpcConn, err := grpc.Dial(productGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect to product-service gRPC at %s: %v", productGRPCAddr, err)
	}
	defer grpcConn.Close()
	productGRPCClient := pb.NewProductInventoryServiceClient(grpcConn)

	// Initialize Ledger Service
	ledgerSvc := ledger.NewService(db.Writer, nil)

	svc := service.NewOrderService(repo, kafkaClient, redisClient, productGRPCClient, productServiceURL, internalSigner)

	// COD accounting depends on the ledger and the new payment transaction table.
	paymentTxnRepo := paymentRepo.NewRepository(db.Writer)
	codSvc := paymentSvc.NewCODService(ledgerSvc, paymentTxnRepo)
	svc.WithCODService(codSvc)

	h := handlers.NewOrderHandler(svc, repo)

	cartRepo := repository.NewCartRepository(db.Writer)
	cartSvc := service.NewCartService(cartRepo, productServiceURL, internalSigner)
	cartHandler := handlers.NewCartHandler(cartSvc)

	// Initialize Payment Orchestrator (loads keys from env)
	paymentOrchestrator := paymentSvc.NewOrchestrator()
	commissionCalculator := payment_orchestrator.NewCommissionCalculator(db.Writer)

	customerWalletSvc := walletSvc.NewCustomerWalletService(db.Writer, ledgerSvc)
	newCheckoutHandler := paymentHandlers.NewCheckoutHandler(paymentOrchestrator, customerWalletSvc, repo)

	var rdbForWebhook redis.UniversalClient
	if redisClient != nil {
		rdbForWebhook = redisClient.Client
	}
	webhookHandler := paymentHandlers.NewWebhookHandler(paymentOrchestrator, ledgerSvc, paymentTxnRepo, repo, commissionCalculator, db.Writer, rdbForWebhook)
	refundHandler := paymentHandlers.NewRefundHandler(paymentOrchestrator, ledgerSvc, paymentTxnRepo, repo, svc)

	// 4. Setup Router
	router := gin.Default()
	router.Use(sentrygin.New(sentrygin.Options{Repanic: true}))

	// OpenTelemetry Gin Middleware
	router.Use(otelgin.Middleware("order-service"))

	// Security middleware: CORS + rate limiting
	router.Use(middleware.CORS())
	var rdbForMiddleware redis.UniversalClient
	if redisClient != nil {
		rdbForMiddleware = redisClient.Client
	}
	router.Use(middleware.RateLimit(rdbForMiddleware, 100, time.Minute))

	// Healthcheck
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "order-service"})
	})
	router.GET("/readyz", health.DBPool(db.Writer))

	h.RegisterRoutes(router)

	// Cart endpoints (JWT-protected; handlers read tracking_id from context)
	cartGroup := router.Group("/api/v1/cart", middleware.JWTAuth())
	{
		cartGroup.GET("", cartHandler.GetCart)
		cartGroup.POST("/items", cartHandler.AddItem)
		cartGroup.PUT("/items/:product_id", cartHandler.UpdateItem)
		cartGroup.DELETE("/items/:product_id", cartHandler.RemoveItem)
		cartGroup.DELETE("", cartHandler.ClearCart)
	}

	// Register Checkout, Webhook, Refund and Cancellation endpoints.
	// Checkout is JWT-protected so wallet deductions are tied to the
	// authenticated customer, never a body-supplied ID. Webhooks stay public
	// (gateway callbacks verify signatures in-handler).
	router.POST("/api/v1/payment/checkout", middleware.JWTAuth(), newCheckoutHandler.CreateCheckout)
	router.POST("/api/v1/payment/webhook/:gateway", webhookHandler.HandleWebhook)
	refundHandler.RegisterRefundRoutes(router)

	// Register mobile wallet endpoints (JazzCash/EasyPaisa scaffolding)
	var rdbForWallet redis.UniversalClient
	if redisClient != nil {
		rdbForWallet = redisClient.Client
	}
	walletH := walletHandler.NewWalletHandler().
		WithRiderWallet(walletSvc.NewRiderWalletService(db.Writer)).
		WithCustomerWallet(customerWalletSvc).
		WithOrderService(svc).
		WithKafka(kafkaClient).
		WithRedis(rdbForWallet)
	if rdbForWallet == nil {
		log.Println("Warning: wallet top-up callbacks are DISABLED without redis (pending-load verification unavailable)")
	}
	walletH.RegisterRoutes(router)

	// Register Ratings endpoints (POST /api/v1/ratings/, GET /api/v1/ratings/:tracking_id)
	ratingH := ratingHandlers.NewRatingHandler(db.Writer)
	ratingH.RegisterRoutes(router)

	// Register Chat endpoints
	cRepo := chatRepo.NewChatRepository(db.Writer)
	var rdbForChat redis.UniversalClient
	if redisClient != nil {
		rdbForChat = redisClient.Client
	}
	cSvc := chatSvc.NewChatService(cRepo, rdbForChat)
	cH := chatHandler.NewChatHandler(cSvc)

	// Secure Chat routes
	chatGroup := router.Group("/api/v1/chat")
	chatGroup.Use(middleware.JWTAuth())
	chatGroup.POST("/messages", cH.SendMessage)
	chatGroup.GET("/messages", cH.GetHistory)
	chatGroup.PUT("/messages/:orderId/read", cH.MarkRead)
	chatGroup.GET("/conversations", cH.ListConversations)
	chatGroup.GET("/unread", cH.UnreadCount)

	// 5. Start Server with Graceful Shutdown
	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", config.BindHost(), cfg.Port), // Service runs on 9005
		Handler: router,
	}

	go func() {
		log.Printf("Starting Order Service on port %d", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Listen and serve error: %v", err)
		}
	}()

	// Outbox Poller
	pollerCtx, pollerCancel := context.WithCancel(context.Background())
	go svc.StartOutboxPoller(pollerCtx)

	// Note: Escrow releases are centrally handled by payment_orchestrator's
	// EscrowReleaserWorker (with dispute checks and single commission deduction).
	// Duplicate escrowCronSvc has been decommissioned to prevent double deductions.

	// Start Delivery Status Consumer — syncs delivery events to order status
	if kafkaClient != nil {
		go startDeliveryStatusConsumer(ctx, cfg.KafkaBrokers, repo)
		go startGigAcceptedConsumer(ctx, cfg.KafkaBrokers, repo)
	}

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	pollerCancel() // stop the outbox poller

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctxTimeout); err != nil {
		log.Fatal("Server forced to shutdown: ", err)
	}

	log.Println("Server exiting")
}

// startDeliveryStatusConsumer listens to deliveries.status_updated and updates order status.
// Maps delivery statuses to order statuses. All order statuses are lowercase
// to match the canonical DB enum (`pending`, `accepted`, `shipped`,
// `in_transit`, `delivered`, `completed`, `cancelled`, `failed`) and the
// Flutter timeline in `order_detail_screen.dart`.
//
//	picked_up  → shipped
//	in_transit → in_transit
//	completed  → delivered (also sets delivered_at, releases escrow timer)
//	failed     → failed
func startDeliveryStatusConsumer(ctx context.Context, brokers []string, repo *repository.OrderRepository) {
	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup("order-service-delivery-status"),
		kgo.ConsumeTopics("deliveries.status_updated"),
	)
	if err != nil {
		log.Printf("Warning: Failed to create delivery status consumer: %v", err)
		return
	}
	defer consumer.Close()

	log.Println("Delivery status consumer started — listening to deliveries.status_updated")

	statusMap := map[string]string{
		"picked_up":  "shipped",
		"in_transit": "in_transit",
		"completed":  "delivered",
		"failed":     "failed",
	}

	for {
		fetches := consumer.PollFetches(ctx)
		if fetches.IsClientClosed() {
			return
		}
		iter := fetches.RecordIter()
		for !iter.Done() {
			record := iter.Next()
			var event struct {
				OrderTrackingID string `json:"order_tracking_id"`
				Status          string `json:"status"`
			}
			if err := json.Unmarshal(record.Value, &event); err != nil {
				log.Printf("Failed to unmarshal delivery status event: %v", err)
				continue
			}
			orderStatus, ok := statusMap[event.Status]
			if !ok {
				continue
			}
			// When the delivery completes, also stamp delivered_at so the
			// escrow release cron (`escrow_cron.go`) can settle the funds
			// after the 48-hour dispute window.
			if orderStatus == "delivered" {
				if err := repo.MarkOrderDelivered(ctx, event.OrderTrackingID); err != nil {
					log.Printf("Failed to mark order %s delivered: %v", event.OrderTrackingID, err)
					continue
				}
			} else if err := repo.UpdateOrderStatus(ctx, event.OrderTrackingID, orderStatus); err != nil {
				log.Printf("Failed to update order %s status to %s: %v", event.OrderTrackingID, orderStatus, err)
				continue
			}
			log.Printf("Order %s status updated to %s (from delivery event)", event.OrderTrackingID, orderStatus)
		}
	}
}

// startGigAcceptedConsumer listens to deliveries.accepted and moves the order
// from pending/paid to accepted as soon as a rider claims the gig. Without
// this consumer the order sat in pending until pickup, skipping the accepted
// step in the customer-facing timeline and breaking RecordVendorHandover
// (which requires status IN ('accepted','shipped','in_transit')).
func startGigAcceptedConsumer(ctx context.Context, brokers []string, repo *repository.OrderRepository) {
	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup("order-service-gig-accepted"),
		kgo.ConsumeTopics("deliveries.accepted"),
	)
	if err != nil {
		log.Printf("Warning: Failed to create gig accepted consumer: %v", err)
		return
	}
	defer consumer.Close()

	log.Println("Gig accepted consumer started — listening to deliveries.accepted")

	for {
		fetches := consumer.PollFetches(ctx)
		if fetches.IsClientClosed() {
			return
		}
		iter := fetches.RecordIter()
		for !iter.Done() {
			record := iter.Next()
			var event struct {
				OrderTrackingID string `json:"order_tracking_id"`
				Status          string `json:"status"`
			}
			if err := json.Unmarshal(record.Value, &event); err != nil {
				log.Printf("Failed to unmarshal gig accepted event: %v", err)
				continue
			}
			if event.OrderTrackingID == "" {
				continue
			}
			if err := repo.UpdateOrderStatus(ctx, event.OrderTrackingID, "accepted"); err != nil {
				log.Printf("Failed to update order %s status to accepted: %v", event.OrderTrackingID, err)
				continue
			}
			log.Printf("Order %s status updated to accepted (from gig accept)", event.OrderTrackingID)
		}
	}
}
