package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/twmb/franz-go/pkg/kgo"
)

type FlattenedOrderEvent struct {
	ID                 int64   `json:"id"`
	TrackingID         string  `json:"tracking_id"`
	UserTrackID        string  `json:"user_tracking_id"`
	VendorStoreTrackID string  `json:"vendor_store_tracking_id"`
	TotalAmount        float64 `json:"total_amount"`
	Currency           string  `json:"currency"`
	Status             string  `json:"status"`
	CreatedAt          int64   `json:"created_at"`
}

type GraphSyncWorker struct {
	kafkaClient *kgo.Client
	neo4jDriver  neo4j.DriverWithContext
	neo4jDb      string
}

func NewGraphSyncWorker(brokers []string, neo4jUri, username, password, dbName string) (*GraphSyncWorker, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup("neo4j-graph-sync-group"),
		kgo.ConsumeTopics("dbstream.public.orders"),
	}
	
	kClient, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to init kafka client: %w", err)
	}

	driver, err := neo4j.NewDriverWithContext(neo4jUri, neo4j.BasicAuth(username, password, ""))
	if err != nil {
		kClient.Close()
		return nil, fmt.Errorf("failed to connect to neo4j: %w", err)
	}

	return &GraphSyncWorker{
		kafkaClient: kClient,
		neo4jDriver:  driver,
		neo4jDb:      dbName,
	}, nil
}

// BootstrapConstraints creates uniqueness constraints programmatically on worker startup
func (w *GraphSyncWorker) BootstrapConstraints(ctx context.Context) error {
	session := w.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode:   neo4j.AccessModeWrite,
		DatabaseName: w.neo4jDb,
	})
	defer session.Close(ctx)

	constraints := []string{
		`CREATE CONSTRAINT customer_utid_unique IF NOT EXISTS FOR (c:Customer) REQUIRE c.utid IS UNIQUE`,
		`CREATE CONSTRAINT store_utid_unique IF NOT EXISTS FOR (s:Store) REQUIRE s.utid IS UNIQUE`,
		`CREATE CONSTRAINT order_utid_unique IF NOT EXISTS FOR (o:Order) REQUIRE o.utid IS UNIQUE`,
	}

	for _, query := range constraints {
		_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			result, err := tx.Run(ctx, query, nil)
			if err != nil {
				return nil, err
			}
			return result.Consume(ctx)
		})
		if err != nil {
			return fmt.Errorf("failed to apply bootstrap constraint (%s): %w", query, err)
		}
	}

	log.Println("[Neo4j Bootstrap] Uniqueness constraints for Customer, Store, and Order successfully applied.")
	return nil
}

// Start runs the batch-processing loop using UNWIND pipelines
func (w *GraphSyncWorker) Start(ctx context.Context) {
	log.Println("Starting Graph Sync Worker in UNWIND batching mode...")

	const batchSize = 100
	const flushTimeout = 500 * time.Millisecond

	batch := make([]map[string]interface{}, 0, batchSize)
	ticker := time.NewTicker(flushTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Flush any remaining records on shutdown
			if len(batch) > 0 {
				w.FlushBatch(context.Background(), batch)
			}
			return

		case <-ticker.C:
			if len(batch) > 0 {
				w.FlushBatch(ctx, batch)
				batch = make([]map[string]interface{}, 0, batchSize)
			}

		default:
			// Poll with a short timeout to prevent busy looping
			pollCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			fetches := w.kafkaClient.PollFetches(pollCtx)
			cancel()

			if fetches.IsClientClosed() {
				return
			}

			iter := fetches.RecordIter()
			for !iter.Done() {
				record := iter.Next()
				
				var event FlattenedOrderEvent
				if err := json.Unmarshal(record.Value, &event); err != nil {
					log.Printf("Skip: JSON unmarshal error: %v", err)
					continue
				}

				if event.TrackingID == "" || event.UserTrackID == "" || event.VendorStoreTrackID == "" {
					continue
				}

				// Flatten event into map parameter for Cypher UNWIND batch
				batch = append(batch, map[string]interface{}{
					"customer_id": event.UserTrackID,
					"store_id":    event.VendorStoreTrackID,
					"order_id":    event.TrackingID,
					"amount":      event.TotalAmount,
					"currency":    event.Currency,
					"timestamp":   event.CreatedAt,
				})

				if len(batch) >= batchSize {
					w.FlushBatch(ctx, batch)
					batch = make([]map[string]interface{}, 0, batchSize)
					ticker.Reset(flushTimeout)
				}
			}
		}
	}
}

// FlushBatch executes a single transactional batch query in Neo4j using UNWIND
func (w *GraphSyncWorker) FlushBatch(ctx context.Context, batch []map[string]interface{}) {
	session := w.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode:   neo4j.AccessModeWrite,
		DatabaseName: w.neo4jDb,
	})
	defer session.Close(ctx)

	// Optimistic UNWIND query resolving deadlock lock contentions
	cypherQuery := `
		UNWIND $events AS event
		MERGE (c:Customer {utid: event.customer_id})
		MERGE (s:Store {utid: event.store_id})
		CREATE (o:Order {
			utid: event.order_id,
			amount: event.amount,
			currency: event.currency,
			timestamp: event.timestamp
		})
		CREATE (c)-[:ORDERED]->(o)
		CREATE (o)-[:SOLD_BY]->(s)
	`

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		result, err := tx.Run(ctx, cypherQuery, map[string]interface{}{"events": batch})
		if err != nil {
			return nil, err
		}
		return result.Consume(ctx)
	})

	if err != nil {
		log.Printf("Batch Write Error: Failed to flush %d records: %v", len(batch), err)
		return
	}

	log.Printf("[Neo4j Batch Sync] Successfully flushed %d records to Graph Database", len(batch))
}

func (w *GraphSyncWorker) Close() {
	if w.kafkaClient != nil {
		w.kafkaClient.Close()
	}
	if w.neo4jDriver != nil {
		w.neo4jDriver.Close(context.Background())
	}
}

func main() {
	brokers := []string{"localhost:9092"}
	neo4jUri := "neo4j://localhost:7687"
	username := "neo4j"
	password := os.Getenv("NEO4J_PASSWORD") // BRAIN-03: hardcoded secret removed
	dbName := "neo4j"

	worker, err := NewGraphSyncWorker(brokers, neo4jUri, username, password, dbName)
	if err != nil {
		log.Fatalf("Fatal: Graph Sync Worker startup failed: %v", err)
	}
	defer worker.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Apply bootstrap uniqueness constraints
	err = worker.BootstrapConstraints(ctx)
	if err != nil {
		log.Printf("Warning: Bootstrap constraints failed (could be due to database starting up): %v", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received termination signal, shutting down worker...")
		cancel()
	}()

	worker.Start(ctx)
}
