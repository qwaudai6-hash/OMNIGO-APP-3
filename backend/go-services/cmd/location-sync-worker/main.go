package main

import (
	"context"
	"github.com/omnigo/backend/internal/shared/telemetry"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/omnigo/backend/internal/shared/cache"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/syncworker"
	"github.com/redis/go-redis/v9"
)

func main() {
	cfg := config.LoadConfig(".")
	defer telemetry.InitSentry(cfg.SentryDSN, cfg.Env)()

	ctx := context.Background()

	database.MigrateUpOrFail(ctx, cfg.DBWriterDSN)
	db, err := database.NewDB(ctx, cfg.DBWriterDSN, cfg.DBReaderDSN)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// Ensure telemetry columns exist in PostgreSQL/TimescaleDB
	_, err = db.Writer.Exec(ctx, `
		ALTER TABLE rider_location_history ADD COLUMN IF NOT EXISTS speed REAL;
		ALTER TABLE rider_location_history ADD COLUMN IF NOT EXISTS bearing REAL;
		ALTER TABLE rider_location_history ADD COLUMN IF NOT EXISTS battery_pct SMALLINT;
	`)
	if err != nil {
		log.Printf("Warning: Failed to verify/add telemetry database columns: %v", err)
	} else {
		log.Println("Telemetry database columns verified and updated successfully.")
	}

	redisClient, err := cache.NewRedisClient(ctx, cfg.RedisAddrs)
	if err != nil {
		log.Printf("Warning: Failed to initialize redis: %v", err)
	} else {
		defer redisClient.Close()
	}

	kafkaClient, err := messaging.NewKafkaClient(cfg.KafkaBrokers, "location-sync-worker")
	if err != nil {
		log.Fatalf("Failed to initialize kafka: %v", err)
	}
	defer kafkaClient.Close()

	var rdb redis.UniversalClient
	if redisClient != nil {
		rdb = redisClient.Client
	}

	worker := syncworker.NewWorker(db.Writer, kafkaClient, rdb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Shutting down Location Sync Worker...")
		cancel()
	}()

	worker.Start(ctx)
}
