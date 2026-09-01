package main

import (
	"context"
	"fmt"
	"github.com/getsentry/sentry-go/gin"
	"github.com/omnigo/backend/internal/shared/telemetry"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/meilisearch/meilisearch-go"
	"github.com/omnigo/backend/internal/product/handlers"
	"github.com/omnigo/backend/internal/product/pb"
	"github.com/omnigo/backend/internal/product/repository"
	"github.com/omnigo/backend/internal/product/service"
	reviewHandlers "github.com/omnigo/backend/internal/review/handlers"
	reviewRepo "github.com/omnigo/backend/internal/review/repository"
	reviewService "github.com/omnigo/backend/internal/review/service"
	"github.com/omnigo/backend/internal/shared/cache"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/shared/health"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/shared/security"
	wishlistHandlers "github.com/omnigo/backend/internal/wishlist/handlers"
	wishlistRepo "github.com/omnigo/backend/internal/wishlist/repository"
	wishlistService "github.com/omnigo/backend/internal/wishlist/service"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"google.golang.org/grpc"
)

func main() {
	// 1. Load Configuration
	cfg := config.LoadConfig(".")
	defer telemetry.InitSentry(cfg.SentryDSN, cfg.Env)()
	cfg.Port = config.EnvPort(9001)

	ctx := context.Background()

	// Initialize OpenTelemetry
	shutdown, err := telemetry.InitProvider("product-service", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
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

	kafkaClient, err := messaging.NewKafkaClient(cfg.KafkaBrokers, "product-service")
	if err != nil {
		log.Printf("Warning: Failed to initialize kafka: %v", err)
	} else {
		defer kafkaClient.Close()
	}

	// 2.5 Initialize Meilisearch (if active)
	var meiliClient *meilisearch.Client
	meiliHost := os.Getenv("MEILISEARCH_HOST")
	meiliKey := os.Getenv("MEILISEARCH_MASTER_KEY")
	if meiliHost != "" {
		meiliClient = meilisearch.NewClient(meilisearch.ClientConfig{
			Host:   meiliHost,
			APIKey: meiliKey,
		})
		log.Println("Meilisearch client initialized")
	}

	// 3. Initialize Domain Layers
	repo := repository.NewProductRepository(db.Writer, db.Reader)

	var rdb redis.UniversalClient
	if redisClient != nil {
		rdb = redisClient.Client
	}
	svc := service.NewProductService(repo, rdb, meiliClient, kafkaClient)

	// Start parallel gRPC server on product-service
	grpcServer := grpc.NewServer()
	pb.RegisterProductInventoryServiceServer(grpcServer, service.NewProductInventoryGRPCServer(svc))

	lis, err := net.Listen("tcp", ":50052")
	if err != nil {
		log.Fatalf("gRPC: Failed to listen on port 50052: %v", err)
	}

	go func() {
		log.Println("Starting Product Inventory gRPC server on port 50052")
		if err := grpcServer.Serve(lis); err != nil {
			log.Printf("gRPC Serve stopped: %v", err)
		}
	}()

	// Build the internal HMAC signer. We panic on missing INTERNAL_API_SECRET
	// in production (config.MustEnv); for local dev an empty secret is OK
	// and the internal routes are simply not registered (see handler).
	var internalSigner *security.InternalSigner
	if secret := os.Getenv("INTERNAL_API_SECRET"); secret != "" {
		internalSigner = security.NewInternalSigner(secret, "product-service")
	}

	h := handlers.NewProductHandler(svc, internalSigner)
	vendorH := handlers.NewVendorProductHandler(svc)

	// 4. Setup Router
	router := gin.Default()
	router.RedirectTrailingSlash = false
	router.Use(sentrygin.New(sentrygin.Options{Repanic: true}))

	// OpenTelemetry Gin Middleware
	router.Use(otelgin.Middleware("product-service"))

	// Security middleware: CORS + Redis-backed rate limiting (100 req/min)
	router.Use(middleware.CORS())
	var rdbForMiddleware redis.UniversalClient
	if redisClient != nil {
		rdbForMiddleware = redisClient.Client
	}
	router.Use(middleware.RateLimit(rdbForMiddleware, 100, time.Minute))

	// Healthcheck
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "product-service"})
	})
	router.GET("/readyz", health.DBPool(db.Writer))

	h.RegisterRoutes(router)
	vendorH.RegisterRoutes(router)

	// Wishlist endpoints (favorites toggle/list/remove)
	wRepo := wishlistRepo.NewWishlistRepository(db.Writer, db.Reader)
	wSvc := wishlistService.NewWishlistService(wRepo)
	wishlistH := wishlistHandlers.NewWishlistHandler(wSvc)
	wishlistH.RegisterRoutes(router)

	// Review endpoints (create/list/summary)
	rRepo := reviewRepo.NewReviewRepository(db.Writer, db.Reader)
	rSvc := reviewService.NewReviewService(rRepo)
	reviewH := reviewHandlers.NewReviewHandler(rSvc)
	reviewH.RegisterRoutes(router)

	// Static uploads directory for product images
	_ = os.MkdirAll("./uploads/products", 0755)
	router.Static("/uploads", "./uploads")

	// 5. Start Server with Graceful Shutdown
	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", config.BindHost(), cfg.Port), // Service runs on 9001
		Handler: router,
	}

	go func() {
		log.Printf("Starting Product Service on port %d", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Listen and serve error: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	grpcServer.GracefulStop()
	log.Println("gRPC server gracefully stopped")

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctxTimeout); err != nil {
		log.Fatal("Server forced to shutdown: ", err)
	}

	log.Println("Server exiting")
}
