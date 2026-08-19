package fraud

import (
	"context"
	"testing"
)

func TestFraudDetectorFailOpenWithoutRedis(t *testing.T) {
	detector := NewDetector(nil, nil)
	ctx := context.Background()

	// Velocity check with nil redis should pass without error
	err := detector.CheckVelocity(ctx, "usr_123", "192.168.1.1")
	if err != nil {
		t.Errorf("expected fail-open on nil redis, got error: %v", err)
	}

	// Anomaly check with nil db should pass without error
	err = detector.CheckOrderAnomaly(ctx, "usr_123", 25000.0)
	if err != nil {
		t.Errorf("expected nil error on small amounts, got: %v", err)
	}

	// RecordAttempt on nil redis should not panic
	detector.RecordAttempt(ctx, "usr_123", "192.168.1.1", false)
	detector.RecordAttempt(ctx, "usr_123", "192.168.1.1", true)
}
