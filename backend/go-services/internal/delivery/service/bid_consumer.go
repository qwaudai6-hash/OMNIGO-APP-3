package service

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// BidEventConsumer reads bid events from Redis Streams and dispatches them
// to connected WebSocket clients. Replaces Pub/Sub with durable delivery.
type BidEventConsumer struct {
	rdb         redis.UniversalClient
	subscribers map[string]chan map[string]interface{} // bidID → channel
}

func NewBidEventConsumer(rdb redis.UniversalClient) *BidEventConsumer {
	return &BidEventConsumer{
		rdb:         rdb,
		subscribers: make(map[string]chan map[string]interface{}),
	}
}

// Subscribe registers a channel to receive events for a specific bid.
func (c *BidEventConsumer) Subscribe(bidID string) chan map[string]interface{} {
	ch := make(chan map[string]interface{}, 10)
	c.subscribers[bidID] = ch
	return ch
}

// Unsubscribe removes a bid subscription.
func (c *BidEventConsumer) Unsubscribe(bidID string) {
	if ch, ok := c.subscribers[bidID]; ok {
		close(ch)
		delete(c.subscribers, bidID)
	}
}

// StartRiderConsumer begins consuming from the rider events stream.
func (c *BidEventConsumer) StartRiderConsumer(ctx context.Context) {
	if c.rdb == nil {
		return
	}

	group := GroupRider
	stream := StreamRiderEvents

	// Ensure consumer group exists
	_ = c.rdb.XGroupCreateMkStream(ctx, stream, group, "0").Err()

	log.Printf("[BidConsumer] Starting rider event consumer on %s", stream)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// XREADGROUP with BLOCK for efficient waiting
		results, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    group,
			Consumer: "rider-consumer",
			Streams:  []string{stream, ">"},
			Count:    10,
			Block:    5 * time.Second,
		}).Result()

		if err != nil {
			if err == redis.Nil || err.Error() == "context canceled" {
				continue
			}
			log.Printf("[BidConsumer] Read error: %v", err)
			time.Sleep(time.Second)
			continue
		}

		// results is []redis.XStream; each XStream has .Messages
		for _, xstream := range results {
			for _, msg := range xstream.Messages {
				c.processRiderMessage(ctx, msg)
				// ACK after processing
				_ = c.rdb.XAck(ctx, stream, group, msg.ID).Err()
			}
		}
	}
}

// StartCustomerConsumer begins consuming from the customer events stream.
func (c *BidEventConsumer) StartCustomerConsumer(ctx context.Context) {
	if c.rdb == nil {
		return
	}

	group := GroupCustomer
	stream := StreamCustomerEvents

	_ = c.rdb.XGroupCreateMkStream(ctx, stream, group, "0").Err()

	log.Printf("[BidConsumer] Starting customer event consumer on %s", stream)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		results, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    group,
			Consumer: "customer-consumer",
			Streams:  []string{stream, ">"},
			Count:    10,
			Block:    5 * time.Second,
		}).Result()

		if err != nil {
			if err == redis.Nil || err.Error() == "context canceled" {
				continue
			}
			log.Printf("[BidConsumer] Read error: %v", err)
			time.Sleep(time.Second)
			continue
		}

		for _, xstream := range results {
			for _, msg := range xstream.Messages {
				c.processCustomerMessage(ctx, msg)
				_ = c.rdb.XAck(ctx, stream, group, msg.ID).Err()
			}
		}
	}
}

func (c *BidEventConsumer) processRiderMessage(ctx context.Context, msg redis.XMessage) {
	dataStr, ok := msg.Values["data"].(string)
	if !ok {
		return
	}

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(dataStr), &event); err != nil {
		log.Printf("[BidConsumer] Failed to unmarshal rider event: %v", err)
		return
	}

	action, _ := event["action"].(string)
	switch action {
	case "RIDE_BID_BROADCAST":
		bidID, _ := event["bid_id"].(string)
		if bidID != "" {
			c.dispatchToSubscribers(bidID, event)
		}
	case "DELIVERY_BID_ACCEPTED":
		bidID, _ := event["bid_id"].(string)
		if bidID != "" {
			c.dispatchToSubscribers(bidID, event)
		}
	default:
		log.Printf("[BidConsumer] Unknown rider action: %s", action)
	}
}

func (c *BidEventConsumer) processCustomerMessage(ctx context.Context, msg redis.XMessage) {
	dataStr, ok := msg.Values["data"].(string)
	if !ok {
		return
	}

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(dataStr), &event); err != nil {
		log.Printf("[BidConsumer] Failed to unmarshal customer event: %v", err)
		return
	}

	action, _ := event["action"].(string)
	switch action {
	case "RIDER_COUNTER_OFFER":
		bidID, _ := event["bid_id"].(string)
		if bidID != "" {
			c.dispatchToSubscribers(bidID, event)
		}
	default:
		log.Printf("[BidConsumer] Unknown customer action: %s", action)
	}
}

func (c *BidEventConsumer) dispatchToSubscribers(bidID string, event map[string]interface{}) {
	if ch, ok := c.subscribers[bidID]; ok {
		select {
		case ch <- event:
		default:
			log.Printf("[BidConsumer] Dropped event for bid %s (channel full)", bidID)
		}
	}
}
