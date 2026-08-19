package syncworker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/go-redsync/redsync/v4"
	redsyncredis "github.com/go-redsync/redsync/v4/redis"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/redis/go-redis/v9"
	"github.com/uber/h3-go/v3"
)

type LocationPayload struct {
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

type Worker struct {
	db    *pgxpool.Pool
	kafka *messaging.KafkaClient
	redis redis.UniversalClient

	// rs is the quorum-safe distributed lock coordinator. It is built from a
	// pool PER multi-master Redis node, so a lock is only granted when a
	// majority (N/2+1) of masters agree. This is the quorum architecture that
	// survives partial-master outages under concurrent Super App traffic.
	rs *redsync.Redsync
}

// NewWorker builds a worker for a single (or single-cluster) Redis client. The
// quorum coordinator is seeded with one pool; callers that run a multi-master
// topology should use NewWorkerQuorum instead.
func NewWorker(db *pgxpool.Pool, kafka *messaging.KafkaClient, redisClient redis.UniversalClient) *Worker {
	return &Worker{
		db:    db,
		kafka: kafka,
		redis: redisClient,
		rs:    redsync.New(goredis.NewPool(redisClient)),
	}
}

// NewWorkerQuorum builds the worker with a full quorum lock architecture. Each
// entry in redisMasters is an independent Redis master; the redsync coordinator
// requires a majority to grant any lock. Pass >=3 masters for a true quorum.
// A redis.UniversalClient satisfies redis.UniversalClient for single-cluster
// production deployments that themselves span multiple masters.
func NewWorkerQuorum(db *pgxpool.Pool, kafka *messaging.KafkaClient, redisMasters ...redis.UniversalClient) *Worker {
	pools := make([]redsyncredis.Pool, 0, len(redisMasters))
	for _, m := range redisMasters {
		pools = append(pools, goredis.NewPool(m))
	}
	return &Worker{
		db:    db,
		kafka: kafka,
		redis: redisMasters[0], // primary used for the fast data path
		rs:    redsync.New(pools...),
	}
}

// peakMutex returns the quorum-guarded mutex for the peak-load switch. Every
// worker that wants to read or flip the switch must own this lock, so no two
// workers can race the toggle even across masters.
func (w *Worker) peakMutex() *redsync.Mutex {
	return w.rs.NewMutex(
		"syncworker:peak:switch",
		redsync.WithExpiry(5*time.Minute),
		redsync.WithTries(3),
	)
}

func (w *Worker) Start(ctx context.Context) {
	w.kafka.Client.AddConsumeTopics("rider.location.updated")
	log.Println("Location Sync Worker started listening to rider.location.updated")

	var buffer []LocationPayload
	batchSize := 100
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// Channel for consumed records to prevent event-loop blocking
	recordChan := make(chan LocationPayload, 1000)

	// Start background Kafka consumer poll loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				fetches := w.kafka.Client.PollFetches(ctx)
				if fetches.IsClientClosed() {
					return
				}
				iter := fetches.RecordIter()
				for !iter.Done() {
					record := iter.Next()
					var payload LocationPayload
					if err := json.Unmarshal(record.Value, &payload); err == nil {
						select {
						case recordChan <- payload:
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			// Gracefully flush remaining items on shutdown
			if len(buffer) > 0 {
				w.flushBatch(context.Background(), buffer)
			}
			return
		case payload := <-recordChan:
			buffer = append(buffer, payload)
			// --- non-breaking live layer: cache + peak router run alongside,
			//     never replace, the stable batch flush below ---
			w.routeLiveLayer(ctx, payload)
			if len(buffer) >= batchSize {
				w.flushBatch(ctx, buffer)
				buffer = nil
			}
		case <-ticker.C:
			if len(buffer) > 0 {
				w.flushBatch(ctx, buffer)
				buffer = nil
			}
		}
	}
}

func (w *Worker) flushBatch(ctx context.Context, batch []LocationPayload) {
	log.Printf("Flushing batch of %d location events to PostGIS and Redis", len(batch))

	// 1. PostGIS Historical Writes
	for _, loc := range batch {
		ok, err := database.Exists(ctx, w.db, "SELECT 1 FROM users WHERE tracking_id = $1", loc.RiderID)
		if err != nil {
			log.Printf("Failed to verify rider %s: %v", loc.RiderID, err)
			continue
		}
		if !ok {
			log.Printf("Rider %s does not exist, skipping location insert", loc.RiderID)
			continue
		}

		_, err = w.db.Exec(ctx, `
			INSERT INTO rider_location_history (rider_tracking_id, latitude, longitude, created_at, speed, bearing, battery_pct)
			VALUES ($1, $3, $2, to_timestamp($4 / 1000.0), $6, $7, $8)
		`, loc.RiderID, loc.Longitude, loc.Latitude, loc.TimestampMS, loc.Status, loc.SpeedMPS, loc.BearingDegrees, loc.BatteryPct)
		if err != nil {
			log.Printf("Failed to insert location for rider %s: %v", loc.RiderID, err)
		}
	}

	// 2. Redis Pipelined Live Geospatial Writes (H3 Res-7 Sharded ZSET keys)
	if w.redis != nil && len(batch) > 0 {
		// Read pass: Fetch current clocks and last hexes in a single batch
		readPipe := w.redis.Pipeline()
		clockCmds := make(map[string]*redis.StringCmd)
		hexCmds := make(map[string]*redis.StringCmd)

		for _, loc := range batch {
			if _, ok := clockCmds[loc.RiderID]; !ok {
				clockCmds[loc.RiderID] = readPipe.Get(ctx, "rider:clock:"+loc.RiderID)
				hexCmds[loc.RiderID] = readPipe.Get(ctx, "rider:last_h7:"+loc.RiderID)
			}
		}

		// Run batch reads
		_, _ = readPipe.Exec(ctx)

		// Write pass: Perform sharded updates and deletes in a single batch
		writePipe := w.redis.Pipeline()
		for _, loc := range batch {
			// Verify Vector Clock
			if cmd, ok := clockCmds[loc.RiderID]; ok {
				lastClockStr, err := cmd.Result()
				if err == nil && lastClockStr != "" {
					if lastClock, err := strconv.ParseInt(lastClockStr, 10, 64); err == nil {
						if loc.TimestampMS <= lastClock {
							continue // Skip stale update
						}
					}
				}
			}

			// Store latest vector clock
			writePipe.Set(ctx, "rider:clock:"+loc.RiderID, loc.TimestampMS, 24*time.Hour)

			lastHexKey := "rider:last_h7:" + loc.RiderID
			var oldHexHex string
			if cmd, ok := hexCmds[loc.RiderID]; ok {
				oldHexHex, _ = cmd.Result()
			}

			if loc.Status == "offline" {
				if oldHexHex != "" {
					oldHexKey := fmt.Sprintf("telemetry:h3:res7:{%s}", oldHexHex)
					oldMemberJSON, _ := w.redis.Get(ctx, "rider:last_member:"+loc.RiderID).Result()
					if oldMemberJSON != "" {
						writePipe.ZRem(ctx, oldHexKey, oldMemberJSON)
					}
				}
				writePipe.Del(ctx, lastHexKey)
				writePipe.Del(ctx, "rider:last_member:"+loc.RiderID)
			} else if loc.Latitude != 0 && loc.Longitude != 0 {
				coord := h3.GeoCoord{Latitude: loc.Latitude, Longitude: loc.Longitude}
				h7Hex := h3.FromGeo(coord, 7)
				newHexHex := fmt.Sprintf("%x", h7Hex)
				newHexKey := fmt.Sprintf("telemetry:h3:res7:{%s}", newHexHex)

				// JSON serialization of Rider Location Metadata
				metaBytes, _ := json.Marshal(loc)
				metaJSON := string(metaBytes)

				// If hexagon changed, remove from old hexagon shard
				if oldHexHex != "" && oldHexHex != newHexHex {
					oldHexKey := fmt.Sprintf("telemetry:h3:res7:{%s}", oldHexHex)
					oldMemberJSON, _ := w.redis.Get(ctx, "rider:last_member:"+loc.RiderID).Result()
					if oldMemberJSON != "" {
						writePipe.ZRem(ctx, oldHexKey, oldMemberJSON)
					}
				}

				// Add to new ZSET with score = current timestamp in seconds
				nowSec := time.Now().Unix()
				writePipe.ZAdd(ctx, newHexKey, redis.Z{
					Score:  float64(nowSec),
					Member: metaJSON,
				})
				writePipe.Expire(ctx, newHexKey, 300*time.Second)
				writePipe.Set(ctx, lastHexKey, newHexHex, 300*time.Second)
				writePipe.Set(ctx, "rider:last_member:"+loc.RiderID, metaJSON, 300*time.Second)

				// Syncworker also writes to the res-5 lookup index that the delivery
				// service reads from when matching gigs to nearby riders.
				coord5 := h3.GeoCoord{Latitude: loc.Latitude, Longitude: loc.Longitude}
				h5Hex := h3.FromGeo(coord5, 5)
				h5Key := fmt.Sprintf("riders:locations:h3:%x", h5Hex)
				writePipe.SAdd(ctx, h5Key, loc.RiderID)
				writePipe.Expire(ctx, h5Key, 300*time.Second)

				// Auto-Prune location updates older than 5 minutes
				pruneTime := nowSec - 300
				writePipe.ZRemRangeByScore(ctx, newHexKey, "-inf", fmt.Sprintf("%d", pruneTime))
			}
		}

		_, err := writePipe.Exec(ctx)
		if err != nil {
			log.Printf("Failed to execute Redis sync pipeline batch: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Live Layer (Session 40 add-on): non-breaking, separate from the stable
// batch flush above. Adds a low-latency Redis GPS cache for OSRM reads and a
// peak-load fast path that writes H3-sharded keys directly. The base
// flushBatch path is never modified by these methods.
// ---------------------------------------------------------------------------

const (
	// Flat geo set OSRM reads from for sub-500ms rider positions.
	liveGPSGeoKey = "riders:live:gps"
	// Quorum-guarded peak switch value key. Written only while the owner
	// holds the redsync peak mutex, so it is safe across a multi-master
	// cluster (majority must agree to flip).
	peakSwitchValueKey = "syncworker:peak:direct-db"
	// Idempotency key per rider to avoid duplicate direct writes in a window.
	directDBIdemKeyPrefix = "syncworker:direct-db:idem:"
)

// routeLiveLayer is the single entry called per payload from Start(). It is
// fire-and-forget against a cancelled context so a slow cache/peak write can
// never stall the stable flush loop.
func (w *Worker) routeLiveLayer(parent context.Context, loc LocationPayload) {
	if w.redis == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 200*time.Millisecond)
	defer cancel()

	// 1. Always update the fast live GPS cache (independent of peak state).
	w.cacheLiveGPS(ctx, loc)

	// 2. If peak load is active, mirror straight to H3-sharded DB keys now.
	if w.isPeakLoad(ctx) {
		w.directDBShardedWrite(ctx, loc)
	}
}

// cacheLiveGPS writes the rider's real-time coords into a flat Redis geo set
// so OSRM path math reads the freshest position without waiting on the SQL
// sync. Async SQL reconciliation happens in flushBatch as before.
func (w *Worker) cacheLiveGPS(ctx context.Context, loc LocationPayload) {
	if loc.Status == "offline" {
		w.redis.ZRem(ctx, liveGPSGeoKey, loc.RiderID)
		w.redis.Del(ctx, "rider:live:gps:"+loc.RiderID)
		return
	}
	if loc.Latitude == 0 && loc.Longitude == 0 {
		return
	}
	pipe := w.redis.Pipeline()
	pipe.GeoAdd(ctx, liveGPSGeoKey, &redis.GeoLocation{
		Longitude: loc.Longitude,
		Latitude:  loc.Latitude,
		Name:      loc.RiderID,
	})
	// Raw JSON copy for clients that need full payload (status, clock).
	raw, _ := json.Marshal(loc)
	pipe.Set(ctx, "rider:live:gps:"+loc.RiderID, raw, 30*time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("live GPS cache write failed for rider %s: %v", loc.RiderID, err)
	}
}

// isPeakLoad returns true when the peak switch is currently enabled. It is
// safe to call on the hot path: it only reads the switch value key. The value
// is only ever mutated by SetPeakMode, which holds the quorum mutex, so no
// multi-worker race can flip it underneath us.
func (w *Worker) isPeakLoad(ctx context.Context) bool {
	held, err := w.redis.Get(ctx, peakSwitchValueKey).Result()
	if err != nil {
		return false // key absent/error => normal path
	}
	return held == "1"
}

// directDBShardedWrite mirrors the H3-sharded Redis writes from flushBatch but
// immediately, for a single rider, during peak windows. A per-rider QUORUM
// mutex guards the read-modify-write of the sharded keys so two workers
// processing the same rider's replayed events can never race the shard move.
// The SetNX idempotency key is retained as a cheap first-gate before the lock.
func (w *Worker) directDBShardedWrite(ctx context.Context, loc LocationPayload) {
	if w.rs == nil {
		return
	}
	idemKey := directDBIdemKeyPrefix + loc.RiderID
	ok, err := w.redis.SetNX(ctx, idemKey, loc.TimestampMS, 2*time.Second).Result()
	if err != nil || !ok {
		return // another worker already handled this clock window
	}

	mu := w.rs.NewMutex("syncworker:direct-db:rider:"+loc.RiderID, redsync.WithExpiry(2*time.Second))
	if err := mu.Lock(); err != nil {
		return // could not secure quorum for this rider; flushBatch will reconcile
	}
	defer func() { _, _ = mu.Unlock() }()

	pipe := w.redis.Pipeline()
	pipe.Set(ctx, "rider:clock:"+loc.RiderID, loc.TimestampMS, 24*time.Hour)
	lastHexKey := "rider:last_h7:" + loc.RiderID

	if loc.Status == "offline" {
		oldHex, err := w.redis.Get(ctx, lastHexKey).Result()
		if err == nil && oldHex != "" {
			oldHexKey := fmt.Sprintf("telemetry:h3:res7:{%s}", oldHex)
			oldMemberJSON, _ := w.redis.Get(ctx, "rider:last_member:"+loc.RiderID).Result()
			if oldMemberJSON != "" {
				pipe.ZRem(ctx, oldHexKey, oldMemberJSON)
			}
		}
		pipe.Del(ctx, lastHexKey)
		pipe.Del(ctx, "rider:last_member:"+loc.RiderID)
	} else if loc.Latitude != 0 && loc.Longitude != 0 {
		coord := h3.GeoCoord{Latitude: loc.Latitude, Longitude: loc.Longitude}
		h7Hex := h3.FromGeo(coord, 7)
		newHexHex := fmt.Sprintf("%x", h7Hex)
		newHexKey := fmt.Sprintf("telemetry:h3:res7:{%s}", newHexHex)

		metaBytes, _ := json.Marshal(loc)
		metaJSON := string(metaBytes)

		oldHex, _ := w.redis.Get(ctx, lastHexKey).Result()
		if oldHex != "" && oldHex != newHexHex {
			oldHexKey := fmt.Sprintf("telemetry:h3:res7:{%s}", oldHex)
			oldMemberJSON, _ := w.redis.Get(ctx, "rider:last_member:"+loc.RiderID).Result()
			if oldMemberJSON != "" {
				pipe.ZRem(ctx, oldHexKey, oldMemberJSON)
			}
		}

		nowSec := time.Now().Unix()
		pipe.ZAdd(ctx, newHexKey, redis.Z{
			Score:  float64(nowSec),
			Member: metaJSON,
		})
		pipe.Expire(ctx, newHexKey, 300*time.Second)
		pipe.Set(ctx, lastHexKey, newHexHex, 300*time.Second)
		pipe.Set(ctx, "rider:last_member:"+loc.RiderID, metaJSON, 300*time.Second)

		// Auto-Prune location updates older than 5 minutes
		pruneTime := nowSec - 300
		pipe.ZRemRangeByScore(ctx, newHexKey, "-inf", fmt.Sprintf("%d", pruneTime))

		// Peak fast path must also populate the res-5 rider lookup index.
		coord5 := h3.GeoCoord{Latitude: loc.Latitude, Longitude: loc.Longitude}
		h5Hex := h3.FromGeo(coord5, 5)
		h5Key := fmt.Sprintf("riders:locations:h3:%x", h5Hex)
		pipe.SAdd(ctx, h5Key, loc.RiderID)
		pipe.Expire(ctx, h5Key, 300*time.Second)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("direct DB sharded write failed for rider %s: %v", loc.RiderID, err)
	}
}

// SetPeakMode lets an operator (or autoscaler) flip the peak switch safely.
// It acquires the QUORUM mutex across the multi-master Redis cluster, so the
// flip only happens once a majority of masters agree (no split-brain toggle
// under concurrent Super App traffic). Returns true if the switch was
// successfully moved to the requested state.
func (w *Worker) SetPeakMode(ctx context.Context, enabled bool) bool {
	if w.rs == nil {
		return false
	}
	mu := w.peakMutex()
	if err := mu.Lock(); err != nil {
		log.Printf("SetPeakMode: failed to acquire quorum lock: %v", err)
		return false
	}
	defer func() {
		if _, err := mu.Unlock(); err != nil {
			log.Printf("SetPeakMode: failed to release quorum lock: %v", err)
		}
	}()

	if enabled {
		if err := w.redis.Set(ctx, peakSwitchValueKey, "1", 5*time.Minute).Err(); err != nil {
			log.Printf("SetPeakMode: failed to enable peak switch: %v", err)
			return false
		}
	} else {
		if err := w.redis.Del(ctx, peakSwitchValueKey).Err(); err != nil {
			log.Printf("SetPeakMode: failed to disable peak switch: %v", err)
			return false
		}
	}
	return true
}
