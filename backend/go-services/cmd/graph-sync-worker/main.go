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
	TrackingID         string  `json:"order_tracking_id"`
	UserTrackID        string  `json:"customer_tracking_id"`
	VendorStoreTrackID string  `json:"store_tracking_id"`
	TotalAmount        float64 `json:"total_amount"`
	Currency           string  `json:"currency"`
	Status             string  `json:"status"`
	CreatedAt          int64   `json:"created_at"`
}

type UserEvent struct {
	TrackingID  string `json:"tracking_id"`
	Email       string `json:"email"`
	FullName    string `json:"full_name"`
	Role        string `json:"role"`
	VehicleType string `json:"vehicle_type"`
}

type StoreEvent struct {
	StoreTrackingID  string  `json:"store_tracking_id"`
	VendorTrackingID string  `json:"vendor_tracking_id"`
	StoreName        string  `json:"store_name"`
	Latitude         float64 `json:"latitude"`
	Longitude        float64 `json:"longitude"`
}

type ProductEvent struct {
	ProductTrackingID string  `json:"product_tracking_id"`
	VendorTrackingID  string  `json:"vendor_tracking_id"`
	StoreTrackingID   string  `json:"store_tracking_id"`
	SKU               string  `json:"sku"`
	Name              string  `json:"name"`
	BasePrice         float64 `json:"base_price"`
	Category          string  `json:"category"`
}

type DeliveryEvent struct {
	TrackingID      string  `json:"tracking_id"`
	OrderTrackingID string  `json:"order_tracking_id"`
	RiderTrackingID *string `json:"rider_tracking_id"`
	Status          string  `json:"status"`
	DeliveryFee     float64 `json:"delivery_fee"`
}

type PaymentTransactionEvent struct {
	TransactionID   string  `json:"transaction_id"`
	OrderTrackingID string  `json:"order_tracking_id"`
	Gateway         string  `json:"gateway"`
	GatewayTxnID    string  `json:"gateway_txn_id"`
	Amount          float64 `json:"amount"`
	Currency        string  `json:"currency"`
	Status          string  `json:"status"`
	CreatedAt       string  `json:"created_at"`
}

type RideEvent struct {
	TrackingID      string  `json:"tracking_id"`
	CustomerTrackID string  `json:"customer_tracking_id"`
	DriverTrackID   *string `json:"driver_tracking_id"`
	Status          string  `json:"status"`
	FareAmount      float64 `json:"fare_amount"`
	VehicleType     string  `json:"vehicle_type"`
}

type GraphSyncWorker struct {
	kafkaClient *kgo.Client
	neo4jDriver neo4j.DriverWithContext
	neo4jDb     string
}

func NewGraphSyncWorker(brokers []string, neo4jUri, username, password, dbName string) (*GraphSyncWorker, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup("neo4j-graph-sync-group"),
		kgo.ConsumeTopics(
			"dbstream.public.orders",
			"dbstream.public.users",
			"dbstream.public.stores",
			"dbstream.public.products",
			"dbstream.public.deliveries",
			"dbstream.public.payment_transactions",
			"dbstream.public.rides",
		),
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
		neo4jDriver: driver,
		neo4jDb:     dbName,
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
		`CREATE CONSTRAINT vendor_utid_unique IF NOT EXISTS FOR (v:Vendor) REQUIRE v.utid IS UNIQUE`,
		`CREATE CONSTRAINT rider_utid_unique IF NOT EXISTS FOR (r:Rider) REQUIRE r.utid IS UNIQUE`,
		`CREATE CONSTRAINT product_utid_unique IF NOT EXISTS FOR (p:Product) REQUIRE p.utid IS UNIQUE`,
		`CREATE CONSTRAINT delivery_utid_unique IF NOT EXISTS FOR (d:Delivery) REQUIRE d.utid IS UNIQUE`,
		`CREATE CONSTRAINT txn_utid_unique IF NOT EXISTS FOR (t:Transaction) REQUIRE t.utid IS UNIQUE`,
		`CREATE CONSTRAINT ride_utid_unique IF NOT EXISTS FOR (rd:Ride) REQUIRE rd.utid IS UNIQUE`,
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
			log.Printf("Warning: Failed to apply constraint (%s): %v", query, err)
		}
	}
	log.Println("Graph Database constraints checked/bootstrapped successfully.")
	return nil
}

// Start begins processing change-data-capture logs from Kafka into Neo4j
func (w *GraphSyncWorker) Start(ctx context.Context) {
	log.Println("Neo4j Graph Synchronization Worker started — listening on dbstream.* CDC topics...")

	const batchSize = 100
	const flushTimeout = 500 * time.Millisecond

	ordersBatch := make([]map[string]interface{}, 0, batchSize)
	custBatch := make([]map[string]interface{}, 0, batchSize)
	vendBatch := make([]map[string]interface{}, 0, batchSize)
	riderBatch := make([]map[string]interface{}, 0, batchSize)
	storesBatch := make([]map[string]interface{}, 0, batchSize)
	productsBatch := make([]map[string]interface{}, 0, batchSize)
	deliveriesBatch := make([]map[string]interface{}, 0, batchSize)
	txnsBatch := make([]map[string]interface{}, 0, batchSize)
	ridesBatch := make([]map[string]interface{}, 0, batchSize)

	ticker := time.NewTicker(flushTimeout)
	defer ticker.Stop()

	flushAll := func() {
		if len(ordersBatch) > 0 {
			w.FlushOrders(ctx, ordersBatch)
			ordersBatch = make([]map[string]interface{}, 0, batchSize)
		}
		if len(custBatch) > 0 {
			w.FlushCustomers(ctx, custBatch)
			custBatch = make([]map[string]interface{}, 0, batchSize)
		}
		if len(vendBatch) > 0 {
			w.FlushVendors(ctx, vendBatch)
			vendBatch = make([]map[string]interface{}, 0, batchSize)
		}
		if len(riderBatch) > 0 {
			w.FlushRiders(ctx, riderBatch)
			riderBatch = make([]map[string]interface{}, 0, batchSize)
		}
		if len(storesBatch) > 0 {
			w.FlushStores(ctx, storesBatch)
			storesBatch = make([]map[string]interface{}, 0, batchSize)
		}
		if len(productsBatch) > 0 {
			w.FlushProducts(ctx, productsBatch)
			productsBatch = make([]map[string]interface{}, 0, batchSize)
		}
		if len(deliveriesBatch) > 0 {
			w.FlushDeliveries(ctx, deliveriesBatch)
			deliveriesBatch = make([]map[string]interface{}, 0, batchSize)
		}
		if len(txnsBatch) > 0 {
			w.FlushTransactions(ctx, txnsBatch)
			txnsBatch = make([]map[string]interface{}, 0, batchSize)
		}
		if len(ridesBatch) > 0 {
			w.FlushRides(ctx, ridesBatch)
			ridesBatch = make([]map[string]interface{}, 0, batchSize)
		}
	}

	for {
		select {
		case <-ctx.Done():
			flushAll()
			return

		case <-ticker.C:
			flushAll()

		default:
			pollCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			fetches := w.kafkaClient.PollFetches(pollCtx)
			cancel()

			if fetches.IsClientClosed() {
				return
			}

			iter := fetches.RecordIter()
			for !iter.Done() {
				record := iter.Next()

				switch record.Topic {
				case "dbstream.public.orders":
					var event FlattenedOrderEvent
					if err := json.Unmarshal(record.Value, &event); err != nil {
						log.Printf("Skip: JSON unmarshal error: %v", err)
						continue
					}
					if event.TrackingID != "" && event.UserTrackID != "" && event.VendorStoreTrackID != "" {
						ordersBatch = append(ordersBatch, map[string]interface{}{
							"customer_id": event.UserTrackID,
							"store_id":    event.VendorStoreTrackID,
							"order_id":    event.TrackingID,
							"amount":      event.TotalAmount,
							"currency":    event.Currency,
							"timestamp":   event.CreatedAt,
						})
					}

				case "dbstream.public.users":
					var event UserEvent
					if err := json.Unmarshal(record.Value, &event); err != nil {
						log.Printf("Skip: JSON unmarshal error: %v", err)
						continue
					}
					if event.TrackingID != "" {
						item := map[string]interface{}{
							"tracking_id": event.TrackingID,
							"full_name":   event.FullName,
							"email":       event.Email,
							"role":        event.Role,
							"vehicle":     event.VehicleType,
						}
						switch event.Role {
						case "customer":
							custBatch = append(custBatch, item)
						case "vendor":
							vendBatch = append(vendBatch, item)
						case "rider":
							riderBatch = append(riderBatch, item)
						}
					}

				case "dbstream.public.stores":
					var event StoreEvent
					if err := json.Unmarshal(record.Value, &event); err != nil {
						log.Printf("Skip: JSON unmarshal error: %v", err)
						continue
					}
					if event.StoreTrackingID != "" && event.VendorTrackingID != "" {
						storesBatch = append(storesBatch, map[string]interface{}{
							"store_tracking_id":  event.StoreTrackingID,
							"vendor_tracking_id": event.VendorTrackingID,
							"store_name":         event.StoreName,
							"latitude":           event.Latitude,
							"longitude":          event.Longitude,
						})
					}

				case "dbstream.public.products":
					var event ProductEvent
					if err := json.Unmarshal(record.Value, &event); err != nil {
						log.Printf("Skip: JSON unmarshal error: %v", err)
						continue
					}
					if event.ProductTrackingID != "" && event.StoreTrackingID != "" {
						productsBatch = append(productsBatch, map[string]interface{}{
							"product_tracking_id": event.ProductTrackingID,
							"store_tracking_id":   event.StoreTrackingID,
							"name":                event.Name,
							"base_price":          event.BasePrice,
							"sku":                 event.SKU,
							"category":            event.Category,
						})
					}

				case "dbstream.public.deliveries":
					var event DeliveryEvent
					if err := json.Unmarshal(record.Value, &event); err != nil {
						log.Printf("Skip: JSON unmarshal error: %v", err)
						continue
					}
					if event.TrackingID != "" && event.OrderTrackingID != "" {
						riderID := ""
						if event.RiderTrackingID != nil && *event.RiderTrackingID != "" {
							riderID = *event.RiderTrackingID
						}
						deliveriesBatch = append(deliveriesBatch, map[string]interface{}{
							"tracking_id":       event.TrackingID,
							"order_tracking_id": event.OrderTrackingID,
							"rider_tracking_id": riderID,
							"status":            event.Status,
							"delivery_fee":      event.DeliveryFee,
						})
					}

				case "dbstream.public.payment_transactions":
					var event PaymentTransactionEvent
					if err := json.Unmarshal(record.Value, &event); err != nil {
						log.Printf("Skip: JSON unmarshal error: %v", err)
						continue
					}
					if event.TransactionID != "" {
						txnsBatch = append(txnsBatch, map[string]interface{}{
							"transaction_id":    event.TransactionID,
							"order_tracking_id": event.OrderTrackingID,
							"gateway":           event.Gateway,
							"gateway_txn_id":    event.GatewayTxnID,
							"amount":            event.Amount,
							"currency":          event.Currency,
							"status":            event.Status,
							"timestamp":         event.CreatedAt,
						})
					}

				case "dbstream.public.rides":
					var event RideEvent
					if err := json.Unmarshal(record.Value, &event); err != nil {
						log.Printf("Skip: JSON unmarshal error: %v", err)
						continue
					}
					if event.TrackingID != "" && event.CustomerTrackID != "" {
						driverID := ""
						if event.DriverTrackID != nil && *event.DriverTrackID != "" {
							driverID = *event.DriverTrackID
						}
						ridesBatch = append(ridesBatch, map[string]interface{}{
							"tracking_id":          event.TrackingID,
							"customer_tracking_id": event.CustomerTrackID,
							"driver_tracking_id":   driverID,
							"status":               event.Status,
							"fare_amount":          event.FareAmount,
							"vehicle_type":         event.VehicleType,
						})
					}
				}

				if len(ordersBatch) >= batchSize || len(custBatch) >= batchSize || len(vendBatch) >= batchSize ||
					len(riderBatch) >= batchSize || len(storesBatch) >= batchSize || len(productsBatch) >= batchSize ||
					len(deliveriesBatch) >= batchSize || len(txnsBatch) >= batchSize || len(ridesBatch) >= batchSize {
					flushAll()
					ticker.Reset(flushTimeout)
				}
			}
		}
	}
}

// FlushOrders executes a single transactional batch query in Neo4j using UNWIND
func (w *GraphSyncWorker) FlushOrders(ctx context.Context, batch []map[string]interface{}) {
	session := w.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode:   neo4j.AccessModeWrite,
		DatabaseName: w.neo4jDb,
	})
	defer session.Close(ctx)

	cypherQuery := `
		UNWIND $events AS event
		MERGE (c:Customer {utid: event.customer_id})
		MERGE (s:Store {utid: event.store_id})
		MERGE (o:Order {utid: event.order_id})
		SET o.amount = event.amount,
			o.currency = event.currency,
			o.timestamp = event.timestamp
		MERGE (c)-[:ORDERED]->(o)
		MERGE (o)-[:SOLD_BY]->(s)
	`

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		result, err := tx.Run(ctx, cypherQuery, map[string]interface{}{"events": batch})
		if err != nil {
			return nil, err
		}
		return result.Consume(ctx)
	})

	if err != nil {
		log.Printf("Batch Write Error (Orders): Failed to flush %d records: %v", len(batch), err)
		return
	}
	log.Printf("[Neo4j Batch Sync] Successfully flushed %d Orders to Graph Database", len(batch))
}

// FlushCustomers merges customer node details
func (w *GraphSyncWorker) FlushCustomers(ctx context.Context, batch []map[string]interface{}) {
	session := w.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode:   neo4j.AccessModeWrite,
		DatabaseName: w.neo4jDb,
	})
	defer session.Close(ctx)

	cypherQuery := `
		UNWIND $events AS event
		MERGE (c:Customer {utid: event.tracking_id})
		SET c.name = event.full_name, c.email = event.email
	`

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		result, err := tx.Run(ctx, cypherQuery, map[string]interface{}{"events": batch})
		if err != nil {
			return nil, err
		}
		return result.Consume(ctx)
	})

	if err != nil {
		log.Printf("Batch Write Error (Customers): Failed to flush %d records: %v", len(batch), err)
		return
	}
	log.Printf("[Neo4j Batch Sync] Successfully flushed %d Customers to Graph Database", len(batch))
}

// FlushVendors merges vendor node details
func (w *GraphSyncWorker) FlushVendors(ctx context.Context, batch []map[string]interface{}) {
	session := w.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode:   neo4j.AccessModeWrite,
		DatabaseName: w.neo4jDb,
	})
	defer session.Close(ctx)

	cypherQuery := `
		UNWIND $events AS event
		MERGE (v:Vendor {utid: event.tracking_id})
		SET v.name = event.full_name, v.email = event.email
	`

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		result, err := tx.Run(ctx, cypherQuery, map[string]interface{}{"events": batch})
		if err != nil {
			return nil, err
		}
		return result.Consume(ctx)
	})

	if err != nil {
		log.Printf("Batch Write Error (Vendors): Failed to flush %d records: %v", len(batch), err)
		return
	}
	log.Printf("[Neo4j Batch Sync] Successfully flushed %d Vendors to Graph Database", len(batch))
}

// FlushRiders merges rider node details
func (w *GraphSyncWorker) FlushRiders(ctx context.Context, batch []map[string]interface{}) {
	session := w.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode:   neo4j.AccessModeWrite,
		DatabaseName: w.neo4jDb,
	})
	defer session.Close(ctx)

	cypherQuery := `
		UNWIND $events AS event
		MERGE (r:Rider {utid: event.tracking_id})
		SET r.name = event.full_name, r.email = event.email, r.vehicle = event.vehicle
	`

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		result, err := tx.Run(ctx, cypherQuery, map[string]interface{}{"events": batch})
		if err != nil {
			return nil, err
		}
		return result.Consume(ctx)
	})

	if err != nil {
		log.Printf("Batch Write Error (Riders): Failed to flush %d records: %v", len(batch), err)
		return
	}
	log.Printf("[Neo4j Batch Sync] Successfully flushed %d Riders to Graph Database", len(batch))
}

// FlushStores merges stores and creates ownership link with vendors
func (w *GraphSyncWorker) FlushStores(ctx context.Context, batch []map[string]interface{}) {
	session := w.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode:   neo4j.AccessModeWrite,
		DatabaseName: w.neo4jDb,
	})
	defer session.Close(ctx)

	cypherQuery := `
		UNWIND $events AS event
		MERGE (s:Store {utid: event.store_tracking_id})
		SET s.name = event.store_name, s.latitude = event.latitude, s.longitude = event.longitude
		WITH s, event
		MERGE (v:Vendor {utid: event.vendor_tracking_id})
		MERGE (v)-[:OWNS]->(s)
	`

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		result, err := tx.Run(ctx, cypherQuery, map[string]interface{}{"events": batch})
		if err != nil {
			return nil, err
		}
		return result.Consume(ctx)
	})

	if err != nil {
		log.Printf("Batch Write Error (Stores): Failed to flush %d records: %v", len(batch), err)
		return
	}
	log.Printf("[Neo4j Batch Sync] Successfully flushed %d Stores to Graph Database", len(batch))
}

// FlushProducts merges products and creates belonging relation to stores
func (w *GraphSyncWorker) FlushProducts(ctx context.Context, batch []map[string]interface{}) {
	session := w.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode:   neo4j.AccessModeWrite,
		DatabaseName: w.neo4jDb,
	})
	defer session.Close(ctx)

	cypherQuery := `
		UNWIND $events AS event
		MERGE (p:Product {utid: event.product_tracking_id})
		SET p.name = event.name, p.price = event.base_price, p.sku = event.sku, p.category = event.category
		WITH p, event
		MERGE (s:Store {utid: event.store_tracking_id})
		MERGE (p)-[:BELONGS_TO]->(s)
	`

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		result, err := tx.Run(ctx, cypherQuery, map[string]interface{}{"events": batch})
		if err != nil {
			return nil, err
		}
		return result.Consume(ctx)
	})

	if err != nil {
		log.Printf("Batch Write Error (Products): Failed to flush %d records: %v", len(batch), err)
		return
	}
	log.Printf("[Neo4j Batch Sync] Successfully flushed %d Products to Graph Database", len(batch))
}

// FlushDeliveries merges delivery details and assignment connections
func (w *GraphSyncWorker) FlushDeliveries(ctx context.Context, batch []map[string]interface{}) {
	session := w.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode:   neo4j.AccessModeWrite,
		DatabaseName: w.neo4jDb,
	})
	defer session.Close(ctx)

	cypherQuery := `
		UNWIND $events AS event
		MERGE (d:Delivery {utid: event.tracking_id})
		SET d.status = event.status, d.fee = event.delivery_fee
		WITH d, event
		MERGE (o:Order {utid: event.order_tracking_id})
		MERGE (d)-[:DELIVERS]->(o)
		WITH d, event
		WHERE event.rider_tracking_id <> ""
		MERGE (r:Rider {utid: event.rider_tracking_id})
		MERGE (r)-[:ASSIGNED_TO]->(d)
	`

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		result, err := tx.Run(ctx, cypherQuery, map[string]interface{}{"events": batch})
		if err != nil {
			return nil, err
		}
		return result.Consume(ctx)
	})

	if err != nil {
		log.Printf("Batch Write Error (Deliveries): Failed to flush %d records: %v", len(batch), err)
		return
	}
	log.Printf("[Neo4j Batch Sync] Successfully flushed %d Deliveries to Graph Database", len(batch))
}

// FlushTransactions merges financial transactions and links to orders
func (w *GraphSyncWorker) FlushTransactions(ctx context.Context, batch []map[string]interface{}) {
	session := w.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode:   neo4j.AccessModeWrite,
		DatabaseName: w.neo4jDb,
	})
	defer session.Close(ctx)

	cypherQuery := `
		UNWIND $events AS event
		MERGE (t:Transaction {utid: event.transaction_id})
		SET t.gateway = event.gateway, t.gateway_txn_id = event.gateway_txn_id, t.amount = event.amount, t.currency = event.currency, t.status = event.status, t.created_at = event.timestamp
		WITH t, event
		WHERE event.order_tracking_id <> ""
		MERGE (o:Order {utid: event.order_tracking_id})
		MERGE (o)-[:PAID_VIA]->(t)
	`

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		result, err := tx.Run(ctx, cypherQuery, map[string]interface{}{"events": batch})
		if err != nil {
			return nil, err
		}
		return result.Consume(ctx)
	})

	if err != nil {
		log.Printf("Batch Write Error (Transactions): Failed to flush %d records: %v", len(batch), err)
		return
	}
	log.Printf("[Neo4j Batch Sync] Successfully flushed %d Transactions to Graph Database", len(batch))
}

// FlushRides merges ride hailing records and creates relationships to customer and driver
func (w *GraphSyncWorker) FlushRides(ctx context.Context, batch []map[string]interface{}) {
	session := w.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode:   neo4j.AccessModeWrite,
		DatabaseName: w.neo4jDb,
	})
	defer session.Close(ctx)

	cypherQuery := `
		UNWIND $events AS event
		MERGE (rd:Ride {utid: event.tracking_id})
		SET rd.status = event.status, rd.fare = event.fare_amount, rd.vehicle = event.vehicle_type
		WITH rd, event
		WHERE event.customer_tracking_id <> ""
		MERGE (c:Customer {utid: event.customer_tracking_id})
		MERGE (c)-[:REQUESTED]->(rd)
		WITH rd, event
		WHERE event.driver_tracking_id <> ""
		MERGE (r:Rider {utid: event.driver_tracking_id})
		MERGE (r)-[:FULFILLED]->(rd)
	`

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		result, err := tx.Run(ctx, cypherQuery, map[string]interface{}{"events": batch})
		if err != nil {
			return nil, err
		}
		return result.Consume(ctx)
	})

	if err != nil {
		log.Printf("Batch Write Error (Rides): Failed to flush %d records: %v", len(batch), err)
		return
	}
	log.Printf("[Neo4j Batch Sync] Successfully flushed %d Rides to Graph Database", len(batch))
}

func (w *GraphSyncWorker) Close() {
	if w.kafkaClient != nil {
		w.kafkaClient.Close()
	}
	if w.neo4jDriver != nil {
		w.neo4jDriver.Close(context.Background())
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s env var is required", key)
	}
	return v
}

func main() {
	brokers := []string{requireEnv("KAFKA_BROKERS")}
	neo4jUri := requireEnv("NEO4J_URI")
	username := requireEnv("NEO4J_USER")
	password := requireEnv("NEO4J_PASSWORD")
	dbName := envOrDefault("NEO4J_DB_NAME", "neo4j")

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
