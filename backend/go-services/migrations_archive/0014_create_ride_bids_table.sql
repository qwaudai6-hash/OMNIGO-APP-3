CREATE TABLE IF NOT EXISTS ride_bids (
    id SERIAL PRIMARY KEY,
    ride_tracking_id VARCHAR(100) NOT NULL REFERENCES rides(tracking_id),
    rider_tracking_id VARCHAR(100) NOT NULL,
    bid_amount NUMERIC(10, 2) NOT NULL,
    status VARCHAR(20) DEFAULT 'pending',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_ride_bids_ride_track_id ON ride_bids(ride_tracking_id);
