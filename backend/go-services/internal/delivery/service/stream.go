package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// Stream names used by the delivery bid system.
const (
	StreamRiderEvents    = "stream:rider:events"
	StreamCustomerEvents = "stream:customer:events"
	GroupRider           = "group:rider"
	GroupCustomer        = "group:customer"
)

// StreamPublisher wraps Redis Streams for durable event delivery.
// Unlike Pub/Sub, messages persist in the stream and survive disconnects.
type StreamPublisher struct {
	rdb redis.UniversalClient
}

func NewStreamPublisher(rdb redis.UniversalClient) *StreamPublisher {
	return &StreamPublisher{rdb: rdb}
}

// EnsureStream creates the stream and consumer group if they don't exist.
func (sp *StreamPublisher) EnsureStream(ctx context.Context, stream, group string) error {
	if sp.rdb == nil {
		return nil
	}
	// XGROUP CREATE MKSTREAM creates stream + group idempotently
	err := sp.rdb.XGroupCreateMkStream(ctx, stream, group, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return fmt.Errorf("failed to create stream group %s/%s: %w", stream, group, err)
	}
	return nil
}

// Publish adds a message to a Redis Stream with automatic trimming.
// Messages persist until trimmed — survive client disconnects, restarts, deploys.
func (sp *StreamPublisher) Publish(ctx context.Context, stream string, payload interface{}) error {
	if sp.rdb == nil {
		return nil
	}

	bytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal stream payload: %w", err)
	}

	// XADD with MAXLEN ~ 10000 approximate trim to cap memory usage
	err = sp.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		MaxLen: 10000,
		Approx: true,
		Values: map[string]interface{}{
			"data": string(bytes),
			"ts":   time.Now().UnixMilli(),
		},
	}).Err()

	if err != nil {
		log.Printf("Warning: failed to publish to stream %s: %v", stream, err)
		return err
	}
	return nil
}

// PublishWithID adds a message with a custom ID (for deduplication).
func (sp *StreamPublisher) PublishWithID(ctx context.Context, stream, msgID string, payload interface{}) error {
	if sp.rdb == nil {
		return nil
	}

	bytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	return sp.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		MaxLen: 10000,
		Approx: true,
		ID:     msgID,
		Values: map[string]interface{}{
			"data": string(bytes),
			"ts":   time.Now().UnixMilli(),
		},
	}).Err()
}
