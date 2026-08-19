package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/delivery/models"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		allowed := []string{
			"https://omnigo-app-production.up.railway.app",
			"https://omnigo.app",
			"https://www.omnigo.app",
			"http://localhost",
			"http://127.0.0.1",
		}
		for _, o := range allowed {
			if strings.HasPrefix(origin, o) {
				return true
			}
		}
		return false
	},
}

type WebSocketGateway struct {
	redisClient *redis.Client
	kafkaClient *messaging.KafkaClient
	brokers     []string
	db          *pgxpool.Pool

	mu        sync.Mutex
	riders    map[string]*websocket.Conn
	customers map[string]*websocket.Conn
	vendors   map[string]*websocket.Conn
}

func NewWebSocketGateway(rdb *redis.Client, kafkaClient *messaging.KafkaClient, brokers []string, db *pgxpool.Pool) *WebSocketGateway {
	return &WebSocketGateway{
		redisClient: rdb,
		kafkaClient: kafkaClient,
		brokers:     brokers,
		db:          db,
		riders:      make(map[string]*websocket.Conn),
		customers:   make(map[string]*websocket.Conn),
		vendors:     make(map[string]*websocket.Conn),
	}
}

type TelemetryPayload struct {
	RiderID        string  `json:"tracking_id"`
	CustomerID     string  `json:"customer_id"`
	OrderID        string  `json:"order_id"`
	TimestampMS    int64   `json:"timestamp_ms"`
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	SpeedMPS       float64 `json:"speed_mps"`
	BearingDegrees float64 `json:"bearing_degrees"`
	BatteryPct     int     `json:"battery_pct"`
	IsCharging     bool    `json:"is_charging"`
	Status         string  `json:"status"`
}

// RegisterEnvelope is the first frame a client must send after the WS handshake.
// It declares the connection type and binds the socket to a tracking_id.
//
//	{
//	  "type": "register",
//	  "client_type": "rider" | "customer" | "vendor",
//	  "tracking_id": "RIDR-xxxx" | "CUST-xxxx" | "VEND-xxxx" | "STOR-xxxx"
//	}
type RegisterEnvelope struct {
	Type       string `json:"type"`
	ClientType string `json:"client_type"`
	TrackingID string `json:"tracking_id"`
}

func (gw *WebSocketGateway) registerClient(clientType, trackingID string, ws *websocket.Conn) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	switch clientType {
	case "rider":
		gw.riders[trackingID] = ws
	case "customer":
		gw.customers[trackingID] = ws
	case "vendor":
		gw.vendors[trackingID] = ws
	}
}

func (gw *WebSocketGateway) unregisterClient(clientType, trackingID string) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	switch clientType {
	case "rider":
		delete(gw.riders, trackingID)
	case "customer":
		delete(gw.customers, trackingID)
	case "vendor":
		delete(gw.vendors, trackingID)
	}
}

func (gw *WebSocketGateway) HandleConnection(c *gin.Context) {
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Failed to upgrade websocket: %v", err)
		return
	}
	defer ws.Close()

	ctx := context.Background()

	// Track which client type owns this connection so unregister uses the right map.
	var clientType string
	var trackingID string

	// Set up a final cleanup once we know the binding.
	var finalizeOnce sync.Once
	finalize := func() {
		finalizeOnce.Do(func() {
			if clientType != "" && trackingID != "" {
				gw.unregisterClient(clientType, trackingID)
			}
		})
	}
	defer finalize()

	// Read the first frame as a registration envelope. We allow a 5-second
	// grace window for the script to send {"type":"register", ...}. If the
	// first frame is a heartbeat ping or a telemetry payload, we fall back
	// to the legacy "rider" assumption so older rider clients keep working.
	firstFrame := true
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			log.Printf("WebSocket error or closed: %v", err)
			return
		}

		// Try to parse as a registration envelope first.
		var env RegisterEnvelope
		if firstFrame {
			firstFrame = false
			if err := json.Unmarshal(raw, &env); err == nil && env.Type == "register" && env.ClientType != "" && env.TrackingID != "" {
				clientType = env.ClientType
				trackingID = env.TrackingID
				gw.registerClient(clientType, trackingID, ws)
				log.Printf("WebSocket registered: type=%s id=%s", clientType, trackingID)
				continue
			}
		}

		// Try to parse as telemetry payload (rider client).
		var payload TelemetryPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			// Could be a heartbeat ping; ignore silently.
			continue
		}

		// Promote to rider registration if we haven't bound yet.
		if payload.RiderID != "" && trackingID == "" {
			trackingID = payload.RiderID
			clientType = "rider"
			gw.registerClient(clientType, trackingID, ws)
		}

		if payload.Status == "offline" {
			if trackingID != "" {
				gw.unregisterClient(clientType, trackingID)
			}
			// Publish offline event to Kafka so syncworker handles Redis cleanup
			if gw.kafkaClient != nil {
				eventBytes, _ := json.Marshal(payload)
				record := &kgo.Record{
					Topic: "rider.location.updated",
					Key:   []byte(payload.RiderID),
					Value: eventBytes,
				}
				_, _ = gw.kafkaClient.Client.ProduceSync(ctx, record).First()
			}
			continue
		}

		if payload.Latitude != 0 && payload.Longitude != 0 {
			if gw.kafkaClient != nil {
				eventBytes, _ := json.Marshal(payload)
				record := &kgo.Record{
					Topic: "rider.location.updated",
					Key:   []byte(payload.RiderID),
					Value: eventBytes,
				}
				gw.kafkaClient.Client.Produce(ctx, record, func(r *kgo.Record, err error) {
					if err != nil {
						log.Printf("Failed to publish rider location event: %v", err)
					}
				})
			}
		}
	}
}

// StartConsuming subscribes to deliveries.broadcasted and ride.broadcasted,
// forwarding broadcast events to eligible riders/drivers via WebSocket.
// It also starts a Redis Pub/Sub listener for Chat messages.
func (gw *WebSocketGateway) StartConsuming(ctx context.Context) {
	// Start Redis Chat consumer
	if gw.redisClient != nil {
		go gw.StartRedisConsumer(ctx)
	}

	if len(gw.brokers) == 0 || gw.brokers[0] == "" {
		log.Println("Kafka brokers not configured, skipping broadcast consumer")
		return
	}

	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(gw.brokers...),
		kgo.ConsumerGroup("websocket-gateway-broadcasts"),
		kgo.ConsumeTopics("deliveries.broadcasted", "ride.broadcasted"),
	)
	if err != nil {
		log.Printf("Warning: Failed to create broadcast consumer: %v", err)
		return
	}
	defer consumer.Close()

	log.Println("Broadcast consumer started, listening on deliveries.broadcasted and ride.broadcasted")

	for {
		fetches := consumer.PollFetches(ctx)
		if fetches.IsClientClosed() {
			return
		}

		iter := fetches.RecordIter()
		for !iter.Done() {
			record := iter.Next()
			switch record.Topic {
			case "deliveries.broadcasted":
				gw.handleBroadcast(record.Value)
			case "ride.broadcasted":
				gw.handleRideBroadcast(record.Value)
			}
		}
	}
}

// BroadcastMessage is the JSON envelope sent to riders over WebSocket.
type BroadcastMessage struct {
	Action             string  `json:"action"`
	TrackingID         string  `json:"tracking_id"`
	OrderTrackingID    string  `json:"order_tracking_id"`
	VendorStoreTrackID string  `json:"vendor_store_tracking_id"`
	PickupLat          float64 `json:"pickup_lat"`
	PickupLng          float64 `json:"pickup_lng"`
	DropoffLat         float64 `json:"dropoff_lat"`
	DropoffLng         float64 `json:"dropoff_lng"`
	RiderEarning       float64 `json:"rider_earning"`
}

func (gw *WebSocketGateway) handleBroadcast(data []byte) {
	var gig models.DeliveryGig
	if err := json.Unmarshal(data, &gig); err != nil {
		log.Printf("Failed to unmarshal delivery broadcast: %v", err)
		return
	}

	msg := BroadcastMessage{
		Action:             "GIG_BROADCAST",
		TrackingID:         gig.TrackingID,
		OrderTrackingID:    gig.OrderTrackingID,
		VendorStoreTrackID: gig.VendorStoreTrackID,
		PickupLat:          gig.PickupLat,
		PickupLng:          gig.PickupLng,
		DropoffLat:         gig.DropoffLat,
		DropoffLng:         gig.DropoffLng,
		RiderEarning:       gig.RiderEarning,
	}

	payload, _ := json.Marshal(msg)

	gw.mu.Lock()
	defer gw.mu.Unlock()

	for _, riderID := range gig.EligibleRiders {
		conn, ok := gw.riders[riderID]
		if !ok {
			continue
		}
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			log.Printf("Failed to send GIG_BROADCAST to rider %s: %v", riderID, err)
			conn.Close()
			delete(gw.riders, riderID)
		}
	}
}

type RideBroadcastMessage struct {
	Action      string  `json:"action"`
	TrackingID  string  `json:"tracking_id"`
	VehicleType string  `json:"vehicle_type"`
	PickupLat   float64 `json:"pickup_lat"`
	PickupLng   float64 `json:"pickup_lng"`
	FareAmount  float64 `json:"fare_amount"`
}

func (gw *WebSocketGateway) handleRideBroadcast(data []byte) {
	var broadcast struct {
		TrackingID      string   `json:"tracking_id"`
		VehicleType     string   `json:"vehicle_type"`
		PickupLat       float64  `json:"pickup_lat"`
		PickupLng       float64  `json:"pickup_lng"`
		FareAmount      float64  `json:"fare_amount"`
		EligibleDrivers []string `json:"eligible_drivers"`
	}
	if err := json.Unmarshal(data, &broadcast); err != nil {
		log.Printf("Failed to unmarshal ride broadcast: %v", err)
		return
	}

	msg := RideBroadcastMessage{
		Action:      "RIDE_BROADCAST",
		TrackingID:  broadcast.TrackingID,
		VehicleType: broadcast.VehicleType,
		PickupLat:   broadcast.PickupLat,
		PickupLng:   broadcast.PickupLng,
		FareAmount:  broadcast.FareAmount,
	}

	payload, _ := json.Marshal(msg)

	gw.mu.Lock()
	defer gw.mu.Unlock()

	for _, driverID := range broadcast.EligibleDrivers {
		conn, ok := gw.riders[driverID]
		if !ok {
			continue
		}
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			log.Printf("Failed to send RIDE_BROADCAST to driver %s: %v", driverID, err)
			conn.Close()
			delete(gw.riders, driverID)
		}
	}
}

func (gw *WebSocketGateway) StartRedisConsumer(ctx context.Context) {
	// Subscribe to both chat.broadcast (existing chat plumbing) and
	// rider:telemetry:pubsub (syncworker → ws gateway telemetry bridge so
	// customers and vendors can see the rider moving on the map).
	pubsub := gw.redisClient.Subscribe(ctx, "chat.broadcast", "rider:telemetry:pubsub")
	defer pubsub.Close()

	ch := pubsub.Channel()
	log.Println("Redis Pub/Sub consumer started — listening on chat.broadcast, rider:telemetry:pubsub")

	for msg := range ch {
		switch msg.Channel {
		case "chat.broadcast":
			gw.dispatchChatBroadcast([]byte(msg.Payload))
		case "rider:telemetry:pubsub":
			gw.dispatchTelemetryBroadcast(ctx, []byte(msg.Payload))
		}
	}
}

// dispatchChatBroadcast forwards a chat message to the sender and receiver
// over their respective rider sockets (chat is rider-only today).
func (gw *WebSocketGateway) dispatchChatBroadcast(raw []byte) {
	var chatMsg struct {
		ReceiverID string `json:"receiver_id"`
		SenderID   string `json:"sender_id"`
	}
	if err := json.Unmarshal(raw, &chatMsg); err != nil {
		log.Printf("Failed to unmarshal chat broadcast: %v", err)
		return
	}

	gw.mu.Lock()
	defer gw.mu.Unlock()

	// Look up the receiver across rider/customer/vendor maps.
	for _, m := range []map[string]*websocket.Conn{gw.riders, gw.customers, gw.vendors} {
		if conn, ok := m[chatMsg.ReceiverID]; ok {
			if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				conn.Close()
				delete(m, chatMsg.ReceiverID)
			}
		}
		if conn, ok := m[chatMsg.SenderID]; ok {
			if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				conn.Close()
				delete(m, chatMsg.SenderID)
			}
		}
	}
}

// TelemetryFrame is the payload published by the syncworker into
// rider:telemetry:pubsub. It encodes the rider's most-recent GPS fix.
type TelemetryFrame struct {
	RiderID   string  `json:"rider_id"`
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	UpdatedAt int64   `json:"updated_at"`
	OrderID   string  `json:"order_id,omitempty"`
}

// dispatchTelemetryBroadcast forwards a rider's GPS fix to the customer
// who owns the active delivery, and to the vendor whose store is shipping
// it. The lookup is O(1) via an in-memory LRU cache keyed by rider_tracking_id
// to avoid hammering Postgres on every 1-second GPS tick.
func (gw *WebSocketGateway) dispatchTelemetryBroadcast(ctx context.Context, raw []byte) {
	var frame TelemetryFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		log.Printf("Failed to unmarshal telemetry frame: %v", err)
		return
	}
	if frame.RiderID == "" || frame.Lat == 0 || frame.Lng == 0 {
		return
	}

	customerID, vendorID, err := gw.lookupGigRecipients(ctx, frame.RiderID)
	if err != nil {
		log.Printf("Failed to lookup gig recipients for rider %s: %v", frame.RiderID, err)
		return
	}

	envelope := map[string]interface{}{
		"action":    "RIDER_TELEMETRY",
		"rider_id":  frame.RiderID,
		"lat":       frame.Lat,
		"lng":       frame.Lng,
		"timestamp": frame.UpdatedAt,
	}
	payload, _ := json.Marshal(envelope)

	gw.mu.Lock()
	defer gw.mu.Unlock()

	if customerID != "" {
		if conn, ok := gw.customers[customerID]; ok {
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				conn.Close()
				delete(gw.customers, customerID)
			}
		}
	}
	if vendorID != "" {
		if conn, ok := gw.vendors[vendorID]; ok {
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				conn.Close()
				delete(gw.vendors, vendorID)
			}
		}
	}
}

// activeGigCache is a tiny TTL cache so we don't query Postgres on every
// GPS tick (one per second per active rider). A 30-second TTL is more than
// enough for a single delivery lifecycle.
var (
	activeGigCacheMu sync.Mutex
	activeGigCache   = map[string]gigRecipientsCacheEntry{}
)

type gigRecipientsCacheEntry struct {
	customerID string
	vendorID   string
	expiresAt  int64
}

func (gw *WebSocketGateway) lookupGigRecipients(ctx context.Context, riderTrackingID string) (string, string, error) {
	now := time.Now().UnixMilli()
	activeGigCacheMu.Lock()
	if entry, ok := activeGigCache[riderTrackingID]; ok && entry.expiresAt > now {
		activeGigCacheMu.Unlock()
		return entry.customerID, entry.vendorID, nil
	}
	activeGigCacheMu.Unlock()

	if gw.db == nil {
		return "", "", nil
	}

	query := `SELECT d.customer_tracking_id, s.vendor_tracking_id
	          FROM deliveries d
	          LEFT JOIN stores s ON s.store_tracking_id = d.vendor_store_tracking_id
	          WHERE d.rider_tracking_id = $1
	            AND d.status IN ('accepted', 'picked_up', 'in_transit')
	          ORDER BY d.updated_at DESC
	          LIMIT 1`
	var customerID, vendorID *string
	row := gw.db.QueryRow(ctx, query, riderTrackingID)
	if err := row.Scan(&customerID, &vendorID); err != nil {
		if err == pgx.ErrNoRows {
			// Cache the negative lookup for a short window so we don't keep
			// hammering the DB for riders who aren't on a delivery.
			activeGigCacheMu.Lock()
			activeGigCache[riderTrackingID] = gigRecipientsCacheEntry{
				customerID: "",
				vendorID:   "",
				expiresAt:  now + 15_000,
			}
			activeGigCacheMu.Unlock()
			return "", "", nil
		}
		return "", "", err
	}

	cust := ""
	if customerID != nil {
		cust = *customerID
	}
	vend := ""
	if vendorID != nil {
		vend = *vendorID
	}

	activeGigCacheMu.Lock()
	activeGigCache[riderTrackingID] = gigRecipientsCacheEntry{
		customerID: cust,
		vendorID:   vend,
		expiresAt:  now + 30_000,
	}
	activeGigCacheMu.Unlock()

	return cust, vend, nil
}
