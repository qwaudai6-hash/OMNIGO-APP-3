-- Delivery Bids Persistence: Redis-only → Postgres
-- Fixes H2: in-flight bids lost on Redis restart

CREATE TABLE IF NOT EXISTS delivery_bids (
    id                    BIGSERIAL PRIMARY KEY,
    bid_id                VARCHAR(50) UNIQUE NOT NULL,
    customer_tracking_id  VARCHAR(100) NOT NULL,
    vehicle_type          VARCHAR(30)  NOT NULL,
    service_type          VARCHAR(20)  DEFAULT 'passenger',
    pickup_lat            DOUBLE PRECISION NOT NULL,
    pickup_lng            DOUBLE PRECISION NOT NULL,
    dropoff_lat           DOUBLE PRECISION NOT NULL,
    dropoff_lng           DOUBLE PRECISION NOT NULL,
    negotiated_fare       NUMERIC(10,2) NOT NULL,
    status                VARCHAR(30) NOT NULL DEFAULT 'searching',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS delivery_bid_counters (
    id                    BIGSERIAL PRIMARY KEY,
    bid_id                VARCHAR(50) NOT NULL REFERENCES delivery_bids(bid_id),
    rider_tracking_id     VARCHAR(100) NOT NULL,
    rider_name            VARCHAR(100),
    rating                VARCHAR(10),
    vehicle_plate         VARCHAR(30),
    proposed_fare         NUMERIC(10,2) NOT NULL,
    eta                   VARCHAR(20),
    status                VARCHAR(20) NOT NULL DEFAULT 'pending',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_delivery_bids_customer ON delivery_bids(customer_tracking_id);
CREATE INDEX IF NOT EXISTS idx_delivery_bids_status ON delivery_bids(status);
CREATE INDEX IF NOT EXISTS idx_delivery_bids_created ON delivery_bids(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_delivery_bid_counters_bid ON delivery_bid_counters(bid_id);
CREATE INDEX IF NOT EXISTS idx_delivery_bid_counters_rider ON delivery_bid_counters(rider_tracking_id);
