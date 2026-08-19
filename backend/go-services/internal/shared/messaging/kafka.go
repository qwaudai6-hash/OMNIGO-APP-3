package messaging

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

// KafkaClient wraps the franz-go client
type KafkaClient struct {
	Client *kgo.Client
}

// NewKafkaClient initializes a connection to the Kafka brokers
func NewKafkaClient(brokers []string, clientID string) (*KafkaClient, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(clientID),
		kgo.ProduceRequestTimeout(5 * time.Second),
	}

	user := os.Getenv("KAFKA_USER")
	pass := os.Getenv("KAFKA_PASSWORD")
	if user != "" && pass != "" {
		opts = append(opts,
			kgo.DialTLSConfig(new(tls.Config)),
			kgo.SASL(scram.Auth{
				User: user,
				Pass: pass,
			}.AsSha256Mechanism()),
		)
	}

	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create kafka client: %w", err)
	}

	// Test connection
	if err := client.Ping(context.Background()); err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to ping kafka brokers: %w", err)
	}

	return &KafkaClient{
		Client: client,
	}, nil
}

// Close releases the Kafka client
func (k *KafkaClient) Close() {
	k.Client.Close()
}
