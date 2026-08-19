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
	"github.com/omnigo/backend/internal/shared/cache"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/shared/health"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/vendorstore/handlers"
	"github.com/omnigo/backend/internal/vendorstore/repository"
	"github.com/omnigo/backend/internal/vendorstore/service"
	"github.com/redis/go-redis/v9"
)

func main() {
	// 1. Load Configuration
	cfg := config.LoadConfig(".")
	defer telemetry.InitSentry(cfg.SentryDSN, cfg.Env)()
	cfg.Port = config.EnvPort(9002)

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

	kafkaClient, err := messaging.NewKafkaClient(cfg.KafkaBrokers, "vendor-store-service")
	if err != nil {
		log.Printf("Warning: Failed to initialize kafka: %v", err)
	} else {
		defer kafkaClient.Close()
	}

	// 3. Initialize Domain Layers
	repo := repository.NewVendorRepository(db.Writer, db.Reader)

	var rdb redis.UniversalClient
	if redisClient != nil {
		rdb = redisClient.Client
	}
	svc := service.NewVendorService(repo, rdb, kafkaClient)
	h := handlers.NewVendorHandler(svc)

	geocodingHandler := handlers.NewGeocodingHandler()
	if rdb != nil {
		geocodingHandler = handlers.NewGeocodingHandlerWithCache(rdb)
	}

	// 4. Setup Router
	router := gin.Default()
	router.Use(sentrygin.New(sentrygin.Options{Repanic: true}))

	// Security middleware: CORS + rate limiting
	router.Use(middleware.CORS())
	var rdbForMiddleware redis.UniversalClient
	if redisClient != nil {
		rdbForMiddleware = redisClient.Client
	}
	router.Use(middleware.RateLimit(rdbForMiddleware, 100, time.Minute))

	// Healthcheck
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "vendor-store"})
	})
	router.GET("/readyz", health.DBPool(db.Writer))

	h.RegisterRoutes(router)
	geocodingHandler.RegisterRoutes(router)

	// 5. Start Server with Graceful Shutdown
	srv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", cfg.Port), // Service runs on 9002
		Handler: router,
	}

	go func() {
		log.Printf("Starting Vendor Store Service on port %d", cfg.Port)
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
