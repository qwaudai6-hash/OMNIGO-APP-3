package main

import (
	"context"
	"encoding/json"
	"github.com/omnigo/backend/internal/shared/telemetry"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/omnigo/backend/internal/analytics"
	"github.com/omnigo/backend/internal/order/models"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/omnigo/backend/internal/shared/messaging"
)

func main() {
	log.Println("Starting Analytics Ingestion Worker...")

	cfg := config.LoadConfig(".")
	defer telemetry.InitSentry(cfg.SentryDSN, cfg.Env)()

	// Initialize ClickHouse
	clickhouseAddr := os.Getenv("CLICKHOUSE_ADDR")
	if clickhouseAddr == "" {
		log.Fatal("FATAL: CLICKHOUSE_ADDR environment variable is not set")
	}
	analyticsSvc, err := analytics.NewAnalyticsService(strings.Split(clickhouseAddr, ","))
	if err != nil {
		log.Fatalf("Failed to connect to ClickHouse: %v", err)
	}
	log.Println("Connected to ClickHouse")

	// Initialize Kafka
	kafkaBrokers := cfg.KafkaBrokers
	if len(kafkaBrokers) == 0 {
		if envStr := os.Getenv("KAFKA_BROKERS"); envStr != "" {
			kafkaBrokers = strings.Split(envStr, ",")
		} else {
			log.Fatal("FATAL: KAFKA_BROKERS environment variable is not set")
		}
	}

	kafkaClient, err := messaging.NewKafkaClient(kafkaBrokers, "analytics-ingest-worker")
	if err != nil {
		log.Fatalf("Failed to initialize Kafka client: %v", err)
	}
	defer kafkaClient.Close()
	log.Println("Connected to Kafka")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Shutting down Analytics Ingestion Worker...")
		cancel()
	}()

	// Start consuming
	log.Println("Subscribing to topics: orders.created")
	kafkaClient.Client.AddConsumeTopics("orders.created")

	go func() {
		for {
			fetches := kafkaClient.Client.PollFetches(ctx)
			if fetches.IsClientClosed() {
				return
			}
			iter := fetches.RecordIter()
			for !iter.Done() {
				record := iter.Next()
				if record.Topic == "orders.created" {
					var event models.OrderEvent
					if err := json.Unmarshal(record.Value, &event); err != nil {
						log.Printf("Failed to unmarshal OrderEvent: %v", err)
						continue
					}

					// Insert into ClickHouse
					if err := analyticsSvc.InsertOrderEvent(
						ctx,
						event.OrderID,
						event.UserTrackID,
						event.VendorStoreTrackID,
						event.TotalAmountPaisa,
						event.DropoffLat,
						event.DropoffLng,
						event.Timestamp,
					); err != nil {
						log.Printf("Failed to insert into ClickHouse: %v", err)
						continue
					}
					log.Printf("Ingested order %s into ClickHouse", event.OrderID)
				}
			}
		}
	}()

	<-ctx.Done()
	log.Println("Worker exited successfully.")
}
