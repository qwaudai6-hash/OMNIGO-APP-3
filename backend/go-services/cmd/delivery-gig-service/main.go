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
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/delivery/handlers"
	"github.com/omnigo/backend/internal/delivery/repository"
	"github.com/omnigo/backend/internal/delivery/service"
	"github.com/omnigo/backend/internal/ledger"
	paymentRepo "github.com/omnigo/backend/internal/payment/repository"
	paymentSvc "github.com/omnigo/backend/internal/payment/service"
	"github.com/omnigo/backend/internal/shared/cache"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/shared/health"
	"github.com/omnigo/backend/internal/shared/messaging"
	walletSvc "github.com/omnigo/backend/internal/wallet/service"
	"github.com/redis/go-redis/v9"
)

func main() {
	// 1. Load Configuration
	cfg := config.LoadConfig(".")
	defer telemetry.InitSentry(cfg.SentryDSN, cfg.Env)()
	cfg.Port = config.EnvPort(9003)

	ctx := context.Background()

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

	kafkaClient, err := messaging.NewKafkaClient(cfg.KafkaBrokers, "delivery-service")
	if err != nil {
		log.Printf("Warning: Failed to initialize kafka: %v", err)
	} else {
		defer kafkaClient.Close()
	}

	// 3. Initialize Domain Layers
	repo := repository.NewDeliveryRepository(db.Writer, db.Reader, nil)
	var rdb redis.UniversalClient
	if redisClient != nil {
		rdb = redisClient.Client
		repo = repository.NewDeliveryRepository(db.Writer, db.Reader, rdb)
	}
	var osrmURL string
	if cfg != nil {
		osrmURL = cfg.OSRMURL
	}
	// Initialize ledger service for double-entry wallet sync
	ledgerSvc := ledger.NewService(db.Writer, nil)

	paymentTxnRepo := paymentRepo.NewRepository(db.Writer)
	codSvc := paymentSvc.NewCODService(ledgerSvc, paymentTxnRepo)

	svc := service.NewDeliveryService(repo, kafkaClient, rdb, osrmURL).
		WithRiderWallet(walletSvc.NewRiderWalletServiceWithLedger(db.Writer, ledgerSvc)).
		WithCODService(codSvc)
	h := handlers.NewDeliveryHandler(svc)

	// 4. Start Background Kafka Consumer
	go svc.StartKafkaConsumer(context.Background())

	// 5. Setup Router
	router := gin.Default()
	router.Use(sentrygin.New(sentrygin.Options{Repanic: true}))

	// Healthcheck
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "delivery-gig-service"})
	})
	router.GET("/readyz", health.DBPool(db.Writer))

	h.RegisterRoutes(router)

	// 6. Start Server with Graceful Shutdown
	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", config.BindHost(), cfg.Port), // Service runs on 9003
		Handler: router,
	}

	go func() {
		log.Printf("Starting Delivery Gig Service on port %d", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Listen and serve error: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctxTimeout); err != nil {
		log.Fatal("Server forced to shutdown: ", err)
	}

	log.Println("Server exiting")
}
