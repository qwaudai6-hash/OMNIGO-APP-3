package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	sentrygin "github.com/getsentry/sentry-go/gin"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/shared/health"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/websocket/handler"
	"github.com/redis/go-redis/v9"
)

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s env var is required", key)
	}
	return v
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	r := gin.Default()
	r.Use(sentrygin.New(sentrygin.Options{Repanic: true}))

	redisAddr := os.Getenv("REDIS_ADDRS")
	var rdb *redis.Client
	if redisAddr != "" {
		if strings.HasPrefix(redisAddr, "redis://") || strings.HasPrefix(redisAddr, "rediss://") {
			opts, err := redis.ParseURL(redisAddr)
			if err != nil {
				log.Printf("Warning: Failed to parse REDIS_ADDRS URL: %v", err)
			} else {
				rdb = redis.NewClient(opts)
			}
		} else {
			cleanAddr := strings.TrimPrefix(strings.TrimPrefix(redisAddr, "redis://"), "rediss://")
			rdb = redis.NewClient(&redis.Options{
				Addr: cleanAddr,
			})
		}
	}

	var kafkaClient *messaging.KafkaClient
	var kafkaBrokers []string
	if kb := os.Getenv("KAFKA_BROKERS"); kb != "" {
		kafkaBrokers = strings.Split(kb, ",")
		var err error
		kafkaClient, err = messaging.NewKafkaClient(kafkaBrokers, "websocket-gateway")
		if err != nil {
			log.Printf("Warning: Failed to initialize kafka: %v", err)
		} else {
			defer kafkaClient.Close()
		}
	}

	// Open Postgres pool so the gateway can resolve which customer/vendor
	// should receive a given rider's GPS tick. Falls back to a nil pool
	// (telemetry forwarding disabled) if DB env vars are missing.
	writerDSN := os.Getenv("DATABASE_URL")
	readerDSN := os.Getenv("DATABASE_READER_URL")
	var dbPool *pgxpool.Pool
	if writerDSN != "" {
		dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dbCancel()
		d, err := database.NewDB(dbCtx, writerDSN, readerDSN)
		if err != nil {
			log.Printf("Warning: telemetry lookup disabled (DB connect failed): %v", err)
		} else {
			// We only need the reader pool for lookup; writer is unused here.
			dbPool = d.Reader
			defer d.Close()
		}
	}

	gw := handler.NewWebSocketGateway(rdb, kafkaClient, kafkaBrokers, dbPool)

	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	defer cancelConsumer()
	go gw.StartConsuming(consumerCtx)
	go gw.StartOrderStatusConsuming(consumerCtx)

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})
	r.GET("/readyz", health.Redis(rdb))
	r.GET("/ws", gw.HandleConnection)

	port := envOrDefault("PORT", "9008")
	srv := &http.Server{
		Addr:    config.BindHost() + ":" + port,
		Handler: r,
	}

	go func() {
		log.Printf("Starting WebSocket Gateway on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to run gateway: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down websocket gateway...")

	cancelConsumer()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Gateway forced to shutdown: %v", err)
	}
	log.Println("WebSocket gateway exited")
}
