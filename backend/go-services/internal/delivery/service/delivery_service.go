package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/omnigo/backend/internal/delivery/models"
	"github.com/omnigo/backend/internal/delivery/repository"
	paymentSvc "github.com/omnigo/backend/internal/payment/service"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/shared/tracking"
	walletSvc "github.com/omnigo/backend/internal/wallet/service"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/uber/h3-go/v3"
)

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

type DeliveryService struct {
	repo         *repository.DeliveryRepository
	kafka        *messaging.KafkaClient
	redis        redis.UniversalClient
	osrmURL      string
	httpClient   *http.Client
	walletCredit *walletSvc.RiderWalletService
	codService   *paymentSvc.CODService
}

func NewDeliveryService(repo *repository.DeliveryRepository, kafka *messaging.KafkaClient, rdb redis.UniversalClient, osrmURL string) *DeliveryService {
	if osrmURL == "" {
		osrmURL = "https://router.project-osrm.org"
	}
	return &DeliveryService{
		repo:       repo,
		kafka:      kafka,
		redis:      rdb,
		osrmURL:    strings.TrimRight(osrmURL, "/"),
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// WithRiderWallet attaches the rider earnings service so completed deliveries
// can credit the rider wallet atomically.
func (s *DeliveryService) WithRiderWallet(wc *walletSvc.RiderWalletService) *DeliveryService {
	s.walletCredit = wc
	return s
}

// WithCODService attaches the COD accounting service so completed COD deliveries
// move funds through the ledger.
func (s *DeliveryService) WithCODService(cs *paymentSvc.CODService) *DeliveryService {
	s.codService = cs
	return s
}

func generateDeliveryUTID() string {
	return tracking.Generate("DEL")
}

// StartKafkaConsumer runs in the background listening to orders.created
func (s *DeliveryService) StartKafkaConsumer(ctx context.Context) {
	if s.kafka == nil {
		log.Println("Kafka client not initialized, skipping consumer.")
		return
	}

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[CRITICAL RECOVER] Delivery StartKafkaConsumer panicked: %v", r)
		}
	}()

	s.kafka.Client.AddConsumeTopics("orders.created", "orders.cancelled")

	for {
		fetches := s.kafka.Client.PollFetches(ctx)
		if fetches.IsClientClosed() {
			return
		}

		iter := fetches.RecordIter()
		for !iter.Done() {
			record := iter.Next()

			if record.Topic == "orders.cancelled" {
				var cancelPayload struct {
					OrderTrackingID string `json:"order_tracking_id"`
					OrderID         string `json:"order_id"`
					Reason          string `json:"reason"`
				}
				if err := json.Unmarshal(record.Value, &cancelPayload); err == nil {
					orderID := cancelPayload.OrderTrackingID
					if orderID == "" {
						orderID = cancelPayload.OrderID
					}
					if orderID != "" {
						log.Printf("[Delivery] Order %s cancelled. Cancelling active/pending delivery gigs.", orderID)
						if err := s.repo.CancelDeliveryForOrder(ctx, orderID, "Order cancelled by customer/vendor"); err != nil {
							// BUG-07 FIX: Log error for ops visibility instead of silently discarding.
							log.Printf("[Delivery] CRITICAL: Failed to cancel delivery for order %s: %v — rider may still see active delivery", orderID, err)
						}
					}
				}
				continue
			}

			var orderEvent models.OrderEvent
			if err := json.Unmarshal(record.Value, &orderEvent); err != nil {
				log.Printf("Failed to unmarshal order event: %v", err)
				continue
			}

			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[RECOVER] HandleNewOrder panicked for order %s: %v", orderEvent.OrderID, r)
					}
				}()
				s.HandleNewOrder(ctx, orderEvent)
			}()
		}
	}
}

// HandleNewOrder matches delivery gigs to nearby riders using dynamic H3 Hexagonal Ring expansion
func (s *DeliveryService) HandleNewOrder(ctx context.Context, order models.OrderEvent) {
	log.Printf("Processing Delivery Gig: Order %s from Store %s", order.OrderID, order.VendorStoreTrackID)

	// Resolve store pickup coordinates from the stores table
	pickupLat, pickupLng, err := s.repo.GetStoreCoordinates(ctx, order.VendorStoreTrackID)
	if err != nil {
		log.Printf("Dispatch Warning: could not resolve store coordinates for %s: %v", order.VendorStoreTrackID, err)
	}

	otpVal, _ := rand.Int(rand.Reader, big.NewInt(10000))
	otpCode := fmt.Sprintf("%04d", otpVal.Int64())

	km, _, _, _ := s.estimateDistanceAndETA(ctx, pickupLng, pickupLat, order.DropoffLng, order.DropoffLat)

	baseFare := envFloat("DELIVERY_BASE_FARE", 50.0)
	perKmRate := envFloat("DELIVERY_PER_KM_RATE", 15.0)
	surgeMultiplier := 1.0 // H3 surge applied downstream
	nightMultiplier := 1.0

	hour := time.Now().Hour()
	if hour >= 23 || hour <= 6 {
		nightMultiplier = envFloat("DELIVERY_NIGHT_MULTIPLIER", 1.5)
	}

	totalFare := (baseFare + (perKmRate * km)) * surgeMultiplier * nightMultiplier
	adminComm := totalFare * (envFloat("DELIVERY_COMMISSION_PERCENT", 5.0) / 100.0) // admin commission on delivery fee
	riderEarning := totalFare - adminComm

	gig := &models.DeliveryGig{
		TrackingID:         generateDeliveryUTID(),
		OrderTrackingID:    order.OrderID,
		VendorStoreTrackID: order.VendorStoreTrackID,
		CustomerTrackID:    order.UserTrackID,
		DeliveryFee:        totalFare,
		AdminCommission:    adminComm,
		RiderEarning:       riderEarning,
		Tips:               order.Tips,
		PetrolAllowance:    order.PetrolAllowance,
		PickupLat:          pickupLat,
		PickupLng:          pickupLng,
		DropoffLat:         order.DropoffLat,
		DropoffLng:         order.DropoffLng,
		OTPCode:            otpCode,
		IsCOD:              order.IsCOD,
		OrderTotal:         order.TotalAmount,
		CustomerPhone:      order.CustomerPhone,
	}

	if err := s.repo.CreateGig(ctx, gig); err != nil {
		log.Printf("Database Error: Failed to save gig: %v", err)
		return
	}

	// Calculate origin hexagon at resolution 5 from the store pickup location
	centerCoord := h3.GeoCoord{Latitude: pickupLat, Longitude: pickupLng}
	vendorHex := h3.FromGeo(centerCoord, 5)
	log.Printf("Dispatch Origin Hex: %x", vendorHex)

	var riders []string
	var mu sync.Mutex

	// 1. Dynamic Parallel Live Redis Geospatial Queries (k=1 up to k=5, 5km to 25km radius)
	if s.redis != nil {
		for k := 1; k <= 5; k++ {
			neighbors := h3.KRing(vendorHex, k)
			radiusKm := float64(k * 5)
			var wg sync.WaitGroup

			for _, hex := range neighbors {
				wg.Add(1)
				go func(h h3.H3Index) {
					defer wg.Done()
					key := fmt.Sprintf("riders:locations:h3:%x", h)
					res, err := s.redis.GeoRadius(ctx, key, pickupLng, pickupLat, &redis.GeoRadiusQuery{
						Radius: radiusKm,
						Unit:   "km",
					}).Result()

					if err == nil && len(res) > 0 {
						mu.Lock()
						for _, loc := range res {
							// Avoid duplicates
							found := false
							for _, r := range riders {
								if r == loc.Name {
									found = true
									break
								}
							}
							if !found {
								riders = append(riders, loc.Name)
							}
						}
						mu.Unlock()
					}
				}(hex)
			}
			wg.Wait()

			if len(riders) > 0 {
				log.Printf("Redis Sharded Matches Found: %d riders located within %.0fkm (k=%d ring)", len(riders), radiusKm, k)
				break
			}
		}
	}

	if len(riders) == 0 {
		log.Printf("Dispatch Warning: No riders found in any search ring grid boundaries for Gig: %s", gig.TrackingID)
		return
	}

	// Take up to top 5 riders for "Fastest Finger First" notification
	var topRiders []string
	if len(riders) > 5 {
		topRiders = riders[:5]
	} else {
		topRiders = riders
	}

	// Hand over to the Notification Engine
	s.BroadcastGigAlert(ctx, gig, topRiders)
}

func (s *DeliveryService) UpdateLocation(ctx context.Context, req *models.UpdateLocationRequest) error {
	return s.repo.UpdateRiderLocation(ctx, req.RiderTrackID, req.Longitude, req.Latitude)
}

// AcceptGig handles a rider claiming a delivery. Eligibility and assignment are
// done in a single Postgres FOR UPDATE transaction, so no separate Redis lock
// is needed for race protection. Redis is only used for post-commit cleanup.
func (s *DeliveryService) AcceptGig(ctx context.Context, req *models.AcceptGigRequest) error {
	// Check suspension status
	if s.redis != nil {
		suspended, err := s.redis.Exists(ctx, fmt.Sprintf("rider:suspended:%s", req.RiderTrackID)).Result()
		if err == nil && suspended > 0 {
			return fmt.Errorf("conflict: rider is suspended due to delivery scam")
		}
	}

	// Resolve rider's last known H3 hex from the sync-worker's res-5 index with coordinate fallback.
	var riderHex h3.H3Index
	if s.redis != nil {
		lastHexHex, err := s.redis.Get(ctx, fmt.Sprintf("rider:last_h5:%s", req.RiderTrackID)).Result()
		if err == nil && lastHexHex != "" {
			_, _ = fmt.Sscanf(lastHexHex, "%x", &riderHex)
		} else {
			// Fallback to rider:coords
			if coordsJSON, err := s.redis.Get(ctx, fmt.Sprintf("rider:coords:%s", req.RiderTrackID)).Result(); err == nil && coordsJSON != "" {
				var riderLoc struct {
					Lat float64 `json:"lat"`
					Lng float64 `json:"lng"`
				}
				if json.Unmarshal([]byte(coordsJSON), &riderLoc) == nil && riderLoc.Lat != 0 && riderLoc.Lng != 0 {
					riderHex = h3.FromGeo(h3.GeoCoord{Latitude: riderLoc.Lat, Longitude: riderLoc.Lng}, 5)
				}
			}
		}
	}

	err := s.repo.AcceptGigWithEligibility(ctx, req.TrackingID, req.RiderTrackID, riderHex)
	if err != nil {
		return err
	}

	// Best-effort cleanup of any leftover Redis lock marker from older clients.
	if s.redis != nil {
		s.redis.Del(ctx, fmt.Sprintf("gig:lock:%s", req.TrackingID))
	}

	// Invalidate route cache to force recalculation with rider's start position
	if s.redis != nil {
		s.redis.Del(ctx, fmt.Sprintf("route:delivery:%s", req.TrackingID))
	}

	// Publish Gig Accepted event for order-service to pick up
	if s.kafka != nil {
		eventBytes, _ := json.Marshal(req)
		record := &kgo.Record{
			Topic: "deliveries.accepted",
			Key:   []byte(req.TrackingID),
			Value: eventBytes,
		}
		s.kafka.Client.Produce(ctx, record, nil)
	}

	return nil
}

// UpdateGigStatus transitions a gig's status through a strict state machine.
// Emits a status_updated Kafka event enriched with the assigned rider id.
func (s *DeliveryService) UpdateGigStatus(ctx context.Context, trackingID string, req *models.UpdateGigStatusRequest) (*models.DeliveryGig, error) {
	// 1. Fetch gig first to check OTP and details
	gig, err := s.repo.GetGigByTrackingID(ctx, trackingID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch gig for update: %v", err)
	}

	// 2. Validate photo requirements based on status
	if req.Status == models.StatusPickedUp {
		if req.PhotoURL == "" {
			return nil, fmt.Errorf("pickup photo is required")
		}
	}

	// 3. Validate OTP and photo for complete status
	if req.Status == models.StatusCompleted {
		if req.PhotoURL == "" {
			return nil, fmt.Errorf("delivery proof photo is required")
		}
		if req.OTPCode == "" {
			return nil, fmt.Errorf("customer verification OTP is required")
		}
		if gig.OTPCode != req.OTPCode {
			return nil, fmt.Errorf("invalid customer verification OTP")
		}
	}

	var pickupPhoto, deliveryPhoto string
	if req.Status == models.StatusPickedUp {
		pickupPhoto = req.PhotoURL
	} else if req.Status == models.StatusCompleted {
		deliveryPhoto = req.PhotoURL
	}

	prevStatus, assignedRider, err := s.repo.UpdateGigStatus(ctx, trackingID, req.Status, pickupPhoto, deliveryPhoto, gig.RiderEarning)
	if err != nil {
		return nil, err
	}

	// Reload gig to reflect status/photos changes
	gig, err = s.repo.GetGigByTrackingID(ctx, trackingID)
	if err != nil {
		log.Printf("Warning: status updated but failed to reload gig %s: %v", trackingID, err)
		gig = &models.DeliveryGig{TrackingID: trackingID, Status: req.Status, AssignedRiderID: assignedRider}
	}

	// Record double-entry ledger transfer for rider earnings.
	// NOTE: The actual Postgres wallet balance is now updated atomically in UpdateGigStatus.
	if req.Status == models.StatusCompleted && s.walletCredit != nil && assignedRider != "" {
		// Retry ledger transfer up to 3 times before giving up.
		var creditErr error
		for attempt := 0; attempt < 3; attempt++ {
			creditErr = s.walletCredit.CreditDelivery(
				ctx,
				assignedRider,
				trackingID,
				gig.RiderEarning,
				gig.AdminCommission,
			)
			if creditErr == nil {
				break
			}
			log.Printf("Warning: wallet credit attempt %d failed for rider %s: %v", attempt+1, assignedRider, creditErr)
			time.Sleep(time.Duration(500*(attempt+1)) * time.Millisecond)
		}
		if creditErr != nil {
			log.Printf("CRITICAL: delivery completed but wallet credit FAILED after 3 attempts for rider %s: %v", assignedRider, creditErr)
			// Write a compensating outbox event so a reconciliation worker can retry.
			if s.kafka != nil {
				outboxPayload, _ := json.Marshal(map[string]interface{}{
					"event":                "wallet_credit_retry",
					"rider_tracking_id":    assignedRider,
					"delivery_tracking_id": trackingID,
					"rider_earning":        gig.RiderEarning,
					"admin_commission":     gig.AdminCommission,
					"timestamp":            time.Now().UnixMilli(),
				})
				s.kafka.Client.Produce(ctx, &kgo.Record{
					Topic: "wallet.credit.retry",
					Key:   []byte(trackingID),
					Value: outboxPayload,
				}, func(_ *kgo.Record, err error) {
					if err != nil {
						log.Printf("CRITICAL: failed to produce wallet.credit.retry event for %s: %v", trackingID, err)
					}
				})
			}
		}

		if gig.IsCOD {
			// Record rider cash collection liability.
			if err := s.walletCredit.AddCODCollection(ctx, assignedRider, gig.OrderTotal); err != nil {
				log.Printf("Warning: failed to add COD collection to wallet for rider %s: %v", assignedRider, err)
			}

			// Ensure active debt record is created so the rider sees it and can settle via JazzCash/EasyPaisa
			if err := s.repo.RecordCODDebt(ctx, gig.OrderTrackingID, assignedRider, gig.OrderTotal); err != nil {
				log.Printf("Warning: failed to record COD debt for rider %s: %v", assignedRider, err)
			}

			// Move collected cash into central escrow so the platform can split it.
			if s.codService != nil {
				if err := s.codService.OnCashCollected(ctx, gig.OrderTrackingID, gig.OrderTotal); err != nil {
					log.Printf("Warning: COD cash collected ledger transfer failed for order %s: %v", gig.OrderTrackingID, err)
				}

				// Release COD escrow split using the gig's own store/vendor tracking
				// and the rider earning recorded on the gig.
				if err := s.codService.ReleaseAfterDelivery(
					ctx,
					gig.OrderTrackingID,
					gig.VendorStoreTrackID,
					gig.AssignedRiderID,
					gig.OrderTotal,
					gig.AdminCommission,
					gig.RiderEarning,
				); err != nil {
					log.Printf("Warning: COD release after delivery failed for order %s: %v", gig.OrderTrackingID, err)
				}

				// CLAIM-2/3 FIX: credit the vendor wallet + write the paid_out
				// escrow audit row + auto-create the rider's cod_debts row so
				// the Pay Now flow has a bill to settle.
				if err := s.repo.SettleCODVendorAndDebt(
					ctx,
					gig.OrderTrackingID,
					gig.VendorStoreTrackID,
					assignedRider,
					gig.OrderTotal,
					gig.AdminCommission,
					gig.RiderEarning,
				); err != nil {
					log.Printf("CRITICAL: COD vendor settlement/debt recording failed for order %s: %v", gig.OrderTrackingID, err)
				}
			}
		}
	}

	// Invalidate route cache to prevent stale routes after state transitions
	if s.redis != nil {
		s.redis.Del(ctx, fmt.Sprintf("route:delivery:%s", trackingID))
	}

	// Publish Status Updated event
	if s.kafka != nil {
		eventPayload := map[string]interface{}{
			"tracking_id":              trackingID,
			"order_tracking_id":        gig.OrderTrackingID,
			"status":                   req.Status,
			"previous_status":          prevStatus,
			"assigned_rider_id":        assignedRider,
			"vendor_store_tracking_id": gig.VendorStoreTrackID,
			"timestamp":                time.Now().UnixMilli(),
		}
		eventBytes, _ := json.Marshal(eventPayload)
		record := &kgo.Record{
			Topic: "deliveries.status_updated",
			Key:   []byte(trackingID),
			Value: eventBytes,
		}
		s.kafka.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
			if err != nil {
				log.Printf("Warning: Failed to produce deliveries.status_updated event: %v", err)
			}
		})
	}

	return gig, nil
}

// GetRoute returns the OSRM driving route for a gig, cached in Redis.
func (s *DeliveryService) GetRoute(ctx context.Context, trackingID string) (*models.RouteResponse, error) {
	gig, err := s.repo.GetGigByTrackingID(ctx, trackingID)
	if err != nil {
		return nil, fmt.Errorf("failed to load gig: %w", err)
	}

	if gig.PickupLat == 0 && gig.PickupLng == 0 {
		return nil, fmt.Errorf("gig has no pickup coordinates")
	}
	if gig.DropoffLat == 0 && gig.DropoffLng == 0 {
		return nil, fmt.Errorf("gig has no dropoff coordinates")
	}

	cacheKey := fmt.Sprintf("route:delivery:%s", trackingID)
	if s.redis != nil {
		cached, err := s.redis.Get(ctx, cacheKey).Result()
		if err == nil && cached != "" {
			var resp models.RouteResponse
			if json.Unmarshal([]byte(cached), &resp) == nil {
				resp.Source = "cache"
				return &resp, nil
			}
		}
	}

	// Default to Pickup -> Dropoff
	originLng, originLat := gig.PickupLng, gig.PickupLat
	destLng, destLat := gig.DropoffLng, gig.DropoffLat

	// State-aware dynamic routing: check rider location if gig is accepted
	if gig.AssignedRiderID != "" && s.redis != nil {
		coordsKey := fmt.Sprintf("rider:coords:%s", gig.AssignedRiderID)
		if coordsJSON, err := s.redis.Get(ctx, coordsKey).Result(); err == nil && coordsJSON != "" {
			var riderLoc struct {
				Lat float64 `json:"lat"`
				Lng float64 `json:"lng"`
			}
			if json.Unmarshal([]byte(coordsJSON), &riderLoc) == nil && riderLoc.Lat != 0 && riderLoc.Lng != 0 {
				originLng, originLat = riderLoc.Lng, riderLoc.Lat
				if gig.Status == models.StatusAccepted {
					// Rider heading to pickup
					destLng, destLat = gig.PickupLng, gig.PickupLat
				} else if gig.Status == models.StatusPickedUp || gig.Status == models.StatusInTransit {
					// Rider heading to dropoff
					destLng, destLat = gig.DropoffLng, gig.DropoffLat
				}
			}
		}
	}

	coords := fmt.Sprintf("%f,%f;%f,%f", originLng, originLat, destLng, destLat)
	osrmURL := fmt.Sprintf("%s/route/v1/driving/%s?overview=full&geometries=geojson", s.osrmURL, coords)

	resp, err := s.httpClient.Get(osrmURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		// Haversine geometric fallback
		distKm := haversineKm(originLat, originLng, destLat, destLng)
		etaSec := (distKm / 30.0) * 3600.0 // average 30 km/h city riding speed
		result := &models.RouteResponse{
			DistanceMeters:  distKm * 1000.0,
			DurationSeconds: etaSec,
			Coordinates:     [][]float64{{originLng, originLat}, {destLng, destLat}},
			Source:          "haversine_fallback",
		}
		if s.redis != nil {
			cachedBytes, _ := json.Marshal(result)
			s.redis.Set(ctx, cacheKey, string(cachedBytes), 15*time.Second)
		}
		return result, nil
	}
	defer resp.Body.Close()

	var osrmResp struct {
		Code   string `json:"code"`
		Routes []struct {
			Distance float64 `json:"distance"`
			Duration float64 `json:"duration"`
			Geometry struct {
				Coordinates [][]float64 `json:"coordinates"`
				Type        string      `json:"type"`
			} `json:"geometry"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&osrmResp); err != nil {
		distKm := haversineKm(originLat, originLng, destLat, destLng)
		return &models.RouteResponse{
			DistanceMeters:  distKm * 1000.0,
			DurationSeconds: (distKm / 30.0) * 3600.0,
			Coordinates:     [][]float64{{originLng, originLat}, {destLng, destLat}},
			Source:          "haversine_fallback",
		}, nil
	}
	if osrmResp.Code != "Ok" || len(osrmResp.Routes) == 0 {
		distKm := haversineKm(originLat, originLng, destLat, destLng)
		return &models.RouteResponse{
			DistanceMeters:  distKm * 1000.0,
			DurationSeconds: (distKm / 30.0) * 3600.0,
			Coordinates:     [][]float64{{originLng, originLat}, {destLng, destLat}},
			Source:          "haversine_fallback",
		}, nil
	}

	route := osrmResp.Routes[0]
	result := &models.RouteResponse{
		DistanceMeters:  route.Distance,
		DurationSeconds: route.Duration,
		Coordinates:     route.Geometry.Coordinates,
		Source:          "osrm",
	}

	if s.redis != nil {
		cachedBytes, _ := json.Marshal(result)
		// Cache with 15 seconds TTL for active rider navigation updates
		s.redis.Set(ctx, cacheKey, string(cachedBytes), 15*time.Second)
	}

	return result, nil
}

// EstimateRide returns per-vehicle fare estimates with H3 density surge and preview routes.
func (s *DeliveryService) EstimateRide(ctx context.Context, req *models.RideEstimateRequest) (*models.RideEstimateResponse, error) {
	// Try OSRM route for accurate distance, ETA, and preview route polyline
	estimatedKm, etaSeconds, coords, osrmErr := s.estimateDistanceAndETA(ctx, req.PickupLng, req.PickupLat, req.DropoffLng, req.DropoffLat)
	routeSource := "osrm"
	if osrmErr != nil {
		// OSRM unavailable — use haversine for distance/price only, mark route as estimated
		// The client will render a dashed "approximate" polyline instead of real roads
		estimatedKm = haversineKm(req.PickupLat, req.PickupLng, req.DropoffLat, req.DropoffLng)
		etaSeconds = estimatedKm * 180 // ~3 min/km urban estimate
		coords = [][]float64{{req.PickupLng, req.PickupLat}, {req.DropoffLng, req.DropoffLat}}
		routeSource = "estimated"
	}

	surge := s.computeH3Surge(ctx, req.PickupLat, req.PickupLng)

	// Timezone-aware PKT Night Surcharge check (1.25x between 10 PM and 6 AM)
	nightMultiplier := 1.0
	loc, err := time.LoadLocation("Asia/Karachi")
	var now time.Time
	if err == nil {
		now = time.Now().In(loc)
	} else {
		now = time.Now().UTC().Add(5 * time.Hour) // PKT is UTC+5
	}
	hour := now.Hour()
	if hour >= 22 || hour < 6 {
		nightMultiplier = 1.25
	}

	vehicleConfigs := []models.FareBreakdown{
		{VehicleType: "bike", BaseFare: 50, PerKmRate: 15, Currency: "PKR"},
		{VehicleType: "rickshaw", BaseFare: 80, PerKmRate: 20, Currency: "PKR"},
		{VehicleType: "car", BaseFare: 150, PerKmRate: 35, Currency: "PKR"},
	}

	var estimates []models.FareBreakdown
	for _, v := range vehicleConfigs {
		if req.VehicleType != "" && v.VehicleType != req.VehicleType {
			continue
		}

		// Per minute rate config: Bike: PKR 2, Rickshaw: PKR 3, Car: PKR 5
		var perMinRate float64
		switch v.VehicleType {
		case "bike":
			perMinRate = 2.0
		case "rickshaw":
			perMinRate = 3.0
		case "car":
			perMinRate = 5.0
		}

		durationMinutes := etaSeconds / 60.0

		v.EstimatedKm = estimatedKm
		v.SurgeMultiplier = surge
		v.EtaSeconds = etaSeconds

		// Apply formula: (Base + (PerKm * Dist) + (PerMin * Dur)) * Surge * Night
		fareRaw := (v.BaseFare + (v.PerKmRate * estimatedKm) + (perMinRate * durationMinutes)) * surge * nightMultiplier
		v.TotalFare = roundToTwo(fareRaw)
		estimates = append(estimates, v)
	}

	return &models.RideEstimateResponse{
		Estimates:   estimates,
		Geometry:    coords,
		RouteSource: routeSource,
	}, nil
}

func (s *DeliveryService) estimateDistanceAndETA(ctx context.Context, pickupLng, pickupLat, dropoffLng, dropoffLat float64) (km, seconds float64, coords [][]float64, err error) {
	coordsStr := fmt.Sprintf("%f,%f;%f,%f", pickupLng, pickupLat, dropoffLng, dropoffLat)
	osrmURL := fmt.Sprintf("%s/route/v1/driving/%s?overview=full&geometries=geojson", s.osrmURL, coordsStr)

	resp, err := s.httpClient.Get(osrmURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		distKm := haversineKm(pickupLat, pickupLng, dropoffLat, dropoffLng)
		etaSec := (distKm / 30.0) * 3600.0
		return distKm, etaSec, [][]float64{{pickupLng, pickupLat}, {dropoffLng, dropoffLat}}, nil
	}
	defer resp.Body.Close()

	var osrmResp struct {
		Code   string `json:"code"`
		Routes []struct {
			Distance float64 `json:"distance"`
			Duration float64 `json:"duration"`
			Geometry struct {
				Coordinates [][]float64 `json:"coordinates"`
				Type        string      `json:"type"`
			} `json:"geometry"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&osrmResp); err != nil || osrmResp.Code != "Ok" || len(osrmResp.Routes) == 0 {
		distKm := haversineKm(pickupLat, pickupLng, dropoffLat, dropoffLng)
		etaSec := (distKm / 30.0) * 3600.0
		return distKm, etaSec, [][]float64{{pickupLng, pickupLat}, {dropoffLng, dropoffLat}}, nil
	}

	route := osrmResp.Routes[0]
	return route.Distance / 1000.0, route.Duration, route.Geometry.Coordinates, nil
}

func (s *DeliveryService) computeH3Surge(ctx context.Context, lat, lng float64) float64 {
	if s.redis == nil {
		return 1.0
	}
	coord := h3.GeoCoord{Latitude: lat, Longitude: lng}
	hex := h3.FromGeo(coord, 5) // Use Resolution 5 to match sync worker sharding
	supplyKey := fmt.Sprintf("riders:locations:h3:%x", hex)
	demandKey := fmt.Sprintf("demand:h3:%x", hex)

	// Increment demand and set TTL of 5 minutes atomically
	pipe := s.redis.Pipeline()
	incrCmd := pipe.Incr(ctx, demandKey)
	pipe.Expire(ctx, demandKey, 5*time.Minute)
	_, err := pipe.Exec(ctx)
	if err != nil {
		log.Printf("Warning: failed to increment demand in Redis pipeline: %v", err)
	}

	// 1. Get demand count
	var demand float64 = 1.0
	demandVal, err := incrCmd.Result()
	if err == nil {
		demand = float64(demandVal)
	}

	// 2. Get supply count (available riders in this hex)
	var supply float64 = 0.0
	density, err := s.redis.ZCard(ctx, supplyKey).Result()
	if err == nil {
		supply = float64(density)
	}

	// 3. Compute dynamic surge: multiplier = 1.0 + (demand / max(supply, 1)) * 0.15
	if supply < 1 {
		supply = 1.0
	}
	surge := 1.0 + (demand/supply)*envFloat("DELIVERY_SURGE_FACTOR", 0.15)

	// Cap the surge between 1.0x and 3.0x
	if surge < 1.0 {
		surge = 1.0
	}
	surgeCap := envFloat("DELIVERY_SURGE_CAP", 3.0)
	if surge > surgeCap {
		surge = surgeCap
	}

	log.Printf("Dynamic H3 Surge for hex %x: demand=%.1f, supply=%.1f, surge=%.2fx", hex, demand, supply, surge)
	return surge
}

// GetSurgeHeatmap scans Redis for active demand hexes, calculates surge for each, and returns those with surge > 1.0.
func (s *DeliveryService) GetSurgeHeatmap(ctx context.Context) ([]models.SurgeHex, error) {
	var results []models.SurgeHex

	if s.redis == nil {
		return results, nil
	}

	iter := s.redis.Scan(ctx, 0, "demand:h3:*", 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		hexStr := strings.TrimPrefix(key, "demand:h3:")

		// Parse hex string
		hexIndex, err := strconv.ParseUint(hexStr, 16, 64)
		if err != nil {
			continue
		}
		hex := h3.H3Index(hexIndex)

		demandStr, err := s.redis.Get(ctx, key).Result()
		if err != nil {
			continue
		}
		demandVal, err := strconv.ParseFloat(demandStr, 64)
		if err != nil {
			continue
		}
		demand := demandVal

		supplyKey := fmt.Sprintf("riders:locations:h3:%x", hex)
		var supply float64 = 0.0
		density, err := s.redis.ZCard(ctx, supplyKey).Result()
		if err == nil {
			supply = float64(density)
		}

		if supply < 1 {
			supply = 1.0
		}
		surge := 1.0 + (demand/supply)*envFloat("DELIVERY_SURGE_FACTOR", 0.15)

		if surge < 1.0 {
			surge = 1.0
		}
		surgeCap := envFloat("DELIVERY_SURGE_CAP", 3.0)
		if surge > surgeCap {
			surge = surgeCap
		}

		if surge > 1.0 {
			geoBoundary := h3.ToGeoBoundary(hex)
			var boundary [][]float64
			for _, coord := range geoBoundary {
				boundary = append(boundary, []float64{coord.Longitude, coord.Latitude})
			}

			results = append(results, models.SurgeHex{
				HexID:           hexStr,
				SurgeMultiplier: surge,
				Boundary:        boundary,
			})
		}
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLng := (lng2 - lng1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLng/2)*math.Sin(dLng/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return r * c
}

func roundToTwo(v float64) float64 {
	return math.Round(v*100) / 100
}

// CancelGig handles a rider cancelling an active order mid-delivery
func (s *DeliveryService) CancelGig(ctx context.Context, req *models.CancelGigRequest) error {
	// First, release the lock in Redis so another rider can claim it
	if s.redis != nil {
		s.redis.Del(ctx, fmt.Sprintf("gig:lock:%s", req.TrackingID))
	}

	// Unassign in the database (we could set status to broadcasting again)
	_, _, err := s.repo.UpdateGigStatus(ctx, req.TrackingID, models.StatusBroadcasting, "", "", 0)
	if err != nil {
		return err
	}

	// Also clear rider_tracking_id on the parent order so it can be reassigned
	_, _ = s.repo.ClearOrderRider(ctx, req.TrackingID)

	// Fetch the gig details so we can re-broadcast
	gig, err := s.repo.GetGigByTrackingID(ctx, req.TrackingID)
	if err != nil {
		return err
	}

	// Re-broadcast logic: search progressively like initial dispatch (k=1 to k=5, 5km to 25km)
	centerCoord := h3.GeoCoord{Latitude: gig.PickupLat, Longitude: gig.PickupLng}
	vendorHex := h3.FromGeo(centerCoord, 5)

	var riders []string
	var mu sync.Mutex

	if s.redis != nil {
		for k := 1; k <= 5; k++ {
			neighbors := h3.KRing(vendorHex, k)
			radiusKm := float64(k * 5)
			var wg sync.WaitGroup

			for _, hex := range neighbors {
				wg.Add(1)
				go func(h h3.H3Index) {
					defer wg.Done()
					key := fmt.Sprintf("riders:locations:h3:%x", h)
					res, err := s.redis.GeoRadius(ctx, key, gig.PickupLng, gig.PickupLat, &redis.GeoRadiusQuery{
						Radius: radiusKm,
						Unit:   "km",
					}).Result()

					if err == nil && len(res) > 0 {
						mu.Lock()
						for _, loc := range res {
							// Exclude the rider who just cancelled
							if loc.Name != req.RiderTrackID {
								found := false
								for _, r := range riders {
									if r == loc.Name {
										found = true
										break
									}
								}
								if !found {
									riders = append(riders, loc.Name)
								}
							}
						}
						mu.Unlock()
					}
				}(hex)
			}
			wg.Wait()

			if len(riders) > 0 {
				break
			}
		}
	}

	// Take up to top 5 new riders
	var topRiders []string
	if len(riders) > 5 {
		topRiders = riders[:5]
	} else {
		topRiders = riders
	}

	// Re-broadcast to new riders
	s.BroadcastGigAlert(ctx, gig, topRiders)

	return nil
}

// DisputeGig processes a customer complaint, compares photos, and suspends the rider if guilty
func (s *DeliveryService) DisputeGig(ctx context.Context, req *models.DisputeOrderRequest, callerID string) error {
	// 1. Try to fetch gig — first by order_tracking_id, then by gig tracking_id
	gig, err := s.repo.GetGigByOrderTrackingID(ctx, req.TrackingID)
	if err != nil {
		gig, err = s.repo.GetGigByTrackingID(ctx, req.TrackingID)
		if err != nil {
			// No gig found — create a dispute record directly for the order
			if err := s.repo.CreateOrderDispute(ctx, req.TrackingID, req.Reason, req.PhotoURL, callerID); err != nil {
				return err
			}
			// Also mark the order's dispute_status so admin can see it
			return s.repo.UpdateOrderDisputeStatus(ctx, req.TrackingID, "disputed")
		}
	}

	// 2. Update gig dispute state
	err = s.repo.DisputeGig(ctx, gig.TrackingID, req.PhotoURL)
	if err != nil {
		return err
	}

	// 2b. Also update parent order's dispute_status so admin can see it
	if err := s.repo.UpdateOrderDisputeStatus(ctx, gig.OrderTrackingID, "disputed"); err != nil {
		log.Printf("Warning: failed to update order dispute_status for %s: %v", gig.OrderTrackingID, err)
	}

	// 2c. Insert into disputes table for admin visibility
	if err := s.repo.CreateOrderDispute(ctx, gig.OrderTrackingID, req.Reason, req.PhotoURL, callerID); err != nil {
		log.Printf("Warning: failed to insert dispute record for %s: %v", gig.OrderTrackingID, err)
	}

	// 3. Evidence-based scoring — each signal adds weight toward rider or vendor guilt.
	riderScore, vendorScore := 0, 0
	lowerReason := strings.ToLower(req.Reason)

	// Signal 1: Missing proof of delivery dropoff photo
	if gig.DeliveryPhotoURL == "" {
		riderScore += 35
	}

	// Signal 2: Rider mishandling / transit damage / stolen items
	if strings.Contains(lowerReason, "damaged") || strings.Contains(lowerReason, "broken") ||
		strings.Contains(lowerReason, "spilled") || strings.Contains(lowerReason, "crushed") ||
		strings.Contains(lowerReason, "rider") || strings.Contains(lowerReason, "not delivered") {
		riderScore += 30
	}

	// Signal 3: Vendor product defect / expired item / wrong item packed
	if strings.Contains(lowerReason, "vendor") || strings.Contains(lowerReason, "expired") ||
		strings.Contains(lowerReason, "quality") || strings.Contains(lowerReason, "wrong item") ||
		strings.Contains(lowerReason, "defective") {
		vendorScore += 35
	}

	// Signal 4: Customer uploaded photo proof
	if req.PhotoURL != "" {
		if riderScore > vendorScore {
			riderScore += 15
		} else if vendorScore > riderScore {
			vendorScore += 15
		}
	}

	// Threshold: need clear evidence (score >= 40) to assign guilt
	riderGuilty := riderScore >= 40 && riderScore > vendorScore

	if riderGuilty {
		// Suspend rider for 3 days
		if s.redis != nil && gig.AssignedRiderID != "" {
			suspendKey := fmt.Sprintf("rider:suspended:%s", gig.AssignedRiderID)
			s.redis.Set(ctx, suspendKey, "true", 3*24*time.Hour)
			log.Printf("Rider %s has been suspended for 3 days due to delivery scam on gig %s", gig.AssignedRiderID, gig.TrackingID)
		}
		s.repo.ResolveDispute(ctx, gig.TrackingID, "resolved_rider_guilty")
	} else {
		// Vendor scam
		s.repo.ResolveDispute(ctx, gig.TrackingID, "resolved_vendor_guilty")
	}

	return nil
}

// CreateRideBid processes a customer's custom fare negotiation request.
func (s *DeliveryService) CreateRideBid(ctx context.Context, req *models.CreateBidRequest) (*models.RideBid, error) {
	bidID := fmt.Sprintf("BID-%s", uuid.New().String()[:8])
	bid := &models.RideBid{
		BidID:           bidID,
		CustomerTrackID: req.CustomerTrackID,
		VehicleType:     req.VehicleType,
		ServiceType:     req.ServiceType,
		PickupLat:       req.PickupLat,
		PickupLng:       req.PickupLng,
		DropoffLat:      req.DropoffLat,
		DropoffLng:      req.DropoffLng,
		NegotiatedFare:  req.NegotiatedFare,
		Status:          "searching",
		CreatedAt:       time.Now(),
	}

	// Publish RIDE_BID_BROADCAST to Redis/NATS pub/sub for WebSocket gateway distribution
	if s.redis != nil {
		broadcastPayload := map[string]interface{}{
			"action":               "RIDE_BID_BROADCAST",
			"bid_id":               bidID,
			"customer_tracking_id": req.CustomerTrackID,
			"vehicle_type":         req.VehicleType,
			"negotiated_fare":      req.NegotiatedFare,
			"pickup_lat":           req.PickupLat,
			"pickup_lng":           req.PickupLng,
			"dropoff_lat":          req.DropoffLat,
			"dropoff_lng":          req.DropoffLng,
			"timestamp":            time.Now().UnixMilli(),
		}
		bytes, _ := json.Marshal(broadcastPayload)
		s.redis.Publish(ctx, "rider:events", string(bytes))
	}

	return bid, nil
}

// SubmitCounterBid processes a rider's counter-offer and pushes it to the customer via WebSocket.
func (s *DeliveryService) SubmitCounterBid(ctx context.Context, req *models.CounterBidRequest) error {
	if s.redis == nil {
		return nil
	}

	offerPayload := map[string]interface{}{
		"action":        "RIDER_COUNTER_OFFER",
		"bid_id":        req.BidID,
		"rider_id":      req.RiderTrackID,
		"rider_name":    req.RiderName,
		"rating":        req.Rating,
		"plate":         req.VehiclePlate,
		"proposed_fare": req.ProposedFare,
		"eta":           req.ETA,
		"timestamp":     time.Now().UnixMilli(),
	}
	bytes, err := json.Marshal(offerPayload)
	if err != nil {
		return err
	}

	// Broadcast counter offer to the customer session channel
	return s.redis.Publish(ctx, "customer:events", string(bytes)).Err()
}
