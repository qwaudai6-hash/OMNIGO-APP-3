package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/delivery/models"
	"github.com/redis/go-redis/v9"
	"github.com/uber/h3-go/v3"
)

type DeliveryRepository struct {
	writer *pgxpool.Pool
	reader *pgxpool.Pool
	redis  *redis.ClusterClient
}

func NewDeliveryRepository(writer, reader *pgxpool.Pool, redisClient *redis.ClusterClient) *DeliveryRepository {
	return &DeliveryRepository{
		writer: writer,
		reader: reader,
		redis:  redisClient,
	}
}

// CreateGig inserts a new delivery gig into PostgreSQL
func (r *DeliveryRepository) CreateGig(ctx context.Context, gig *models.DeliveryGig) error {
	query := `
		INSERT INTO deliveries (tracking_id, order_tracking_id, status, admin_commission)
		VALUES ($1, $2, 'broadcasting', $3)
		RETURNING id, created_at, updated_at
	`
	err := r.writer.QueryRow(ctx, query,
		gig.TrackingID,
		gig.OrderTrackingID,
		gig.AdminCommission,
	).Scan(&gig.ID, &gig.CreatedAt, &gig.UpdatedAt)

	return err
}

// UpdateRiderLocation stores the rider's location in H3 Hexagonal Shards in Redis.
// Implements lock-free SRem/SAdd transitions to keep the sets clean.
func (r *DeliveryRepository) UpdateRiderLocation(ctx context.Context, riderTrackID string, lng, lat float64) error {
	centerCoord := h3.GeoCoord{Latitude: lat, Longitude: lng}
	centerHex := h3.FromGeo(centerCoord, 8)
	newHexKey := fmt.Sprintf("riders:h3:%x", centerHex)

	lastHexKey := fmt.Sprintf("rider:last_hex:%s", riderTrackID)
	oldHexHex, err := r.redis.Get(ctx, lastHexKey).Result()
	
	if err == nil && oldHexHex != "" {
		oldHexKey := fmt.Sprintf("riders:h3:%s", oldHexHex)
		if oldHexKey != newHexKey {
			// Remove from old hexagon set
			r.redis.SRem(ctx, oldHexKey, riderTrackID)
		}
	}

	// Add to new hexagon set
	err = r.redis.SAdd(ctx, newHexKey, riderTrackID).Err()
	if err != nil {
		return err
	}

	// Set expiration on the hexagon key to automatically garbage collect if riders go offline
	r.redis.Expire(ctx, newHexKey, 300*time.Second)

	// Update last known hexagon index (expires in 5 minutes of inactivity)
	r.redis.Set(ctx, lastHexKey, fmt.Sprintf("%x", centerHex), 300*time.Second)

	return nil
}

// GetRidersInHexagon fetches all riders stored inside a single H3 hexagon
func (r *DeliveryRepository) GetRidersInHexagon(ctx context.Context, hexIndex h3.H3Index) ([]string, error) {
	hexKey := fmt.Sprintf("riders:h3:%x", hexIndex)
	return r.redis.SMembers(ctx, hexKey).Result()
}
