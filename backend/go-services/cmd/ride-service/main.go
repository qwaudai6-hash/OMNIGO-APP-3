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
	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/ride/handlers"
	"github.com/omnigo/backend/internal/ride/repository"
	"github.com/omnigo/backend/internal/ride/service"
	"github.com/omnigo/backend/internal/shared/cache"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/shared/health"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/redis/go-redis/v9"
)

func main() {
	// 1. Load Configuration
	cfg := config.LoadConfig(".")
	defer telemetry.InitSentry(cfg.SentryDSN, cfg.Env)()
	cfg.Port = config.EnvPort(9004)

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

	kafkaClient, err := messaging.NewKafkaClient(cfg.KafkaBrokers, "ride-service")
	if err != nil {
		log.Printf("Warning: Failed to initialize kafka: %v", err)
	} else {
		defer kafkaClient.Close()
	}

	// 3. Initialize Domain Layers
	repo := repository.NewRideRepository(db.Writer, db.Reader)

	var clusterClient redis.UniversalClient
	if redisClient != nil {
		clusterClient = redisClient.Client
	}

	svc := service.NewRideService(repo, kafkaClient, clusterClient, ledger.NewService(db.Writer, nil))
	h := handlers.NewRideHandler(svc)

	// 4. Background Services
	go svc.StartConsumer(ctx)

	// 5. Setup Router
	router := gin.Default()
	router.Use(sentrygin.New(sentrygin.Options{Repanic: true}))

	// Healthcheck
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": "ride-service"})
	})
	router.GET("/readyz", health.DBPool(db.Writer))

	h.RegisterRoutes(router)

	// 5. Start Server with Graceful Shutdown
	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", config.BindHost(), cfg.Port), // Service runs on 9004
		Handler: router,
	}

	go func() {
		log.Printf("Starting Ride Service on port %d", cfg.Port)
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
