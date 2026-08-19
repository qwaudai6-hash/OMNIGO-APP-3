package syncworker

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(s.Close)
	return redis.NewClient(&redis.Options{Addr: s.Addr()})
}

// TestNewWorkerSeedsQuorum verifies every worker carries a quorum coordinator
// so peak-switch and per-rider locks are never downgraded to single-node.
func TestNewWorkerSeedsQuorum(t *testing.T) {
	client := newTestRedisClient(t)
	w := NewWorker(nil, nil, client)
	if w.rs == nil {
		t.Fatal("expected quorum coordinator to be seeded by NewWorker")
	}
}

// TestNewWorkerQuorumMultiMaster confirms a worker built with multiple masters
// still produces a usable (non-nil) quorum coordinator for the quorum arch.
func TestNewWorkerQuorumMultiMaster(t *testing.T) {
	m1 := newTestRedisClient(t)
	m2 := newTestRedisClient(t)
	m3 := newTestRedisClient(t)
	w := NewWorkerQuorum(nil, nil, m1, m2, m3)
	if w.rs == nil {
		t.Fatal("expected quorum coordinator across 3 masters")
	}
	if w.redis != m1 {
		t.Fatal("expected primary data path to be first master")
	}
}

// TestRouteLiveLayerNilRedis is the safety guard: with no Redis the live layer
// must be a complete no-op and never panic, so the stable flush loop is
// unaffected when Redis is down.
func TestRouteLiveLayerNilRedis(t *testing.T) {
	w := &Worker{}
	// must not panic
	w.routeLiveLayer(t.Context(), LocationPayload{RiderID: "r1", Latitude: 1, Longitude: 2})
}
