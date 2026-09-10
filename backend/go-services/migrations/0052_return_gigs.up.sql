-- Migration: 0052_return_gigs.sql
-- Description: Creates return_gigs table for rider return pickup/delivery tasks.

CREATE TABLE IF NOT EXISTS return_gigs (
    id SERIAL PRIMARY KEY,
    tracking_id VARCHAR(100) UNIQUE NOT NULL,
    return_request_id VARCHAR(100) NOT NULL,
    order_tracking_id VARCHAR(100) NOT NULL,
    vendor_store_tracking_id VARCHAR(100) NOT NULL,
    assigned_rider_id VARCHAR(100),
    customer_tracking_id VARCHAR(100) NOT NULL,
    
    -- Return details
    return_reason TEXT,
    items_summary TEXT,
    customer_name VARCHAR(200),
    customer_address TEXT,
    customer_phone VARCHAR(50),
    
    -- Status
    status VARCHAR(30) NOT NULL DEFAULT 'broadcasting'
        CHECK (status IN (
            'broadcasting',
            'accepted',
            'picked_up',
            'in_transit',
            'completed',
            'failed',
            'cancelled'
        )),
    
    -- Payment
    rider_earning NUMERIC(10,2) DEFAULT 0,
    delivery_fee NUMERIC(10,2) DEFAULT 0,
    
    -- Locations
    pickup_lat DOUBLE PRECISION NOT NULL,
    pickup_lng DOUBLE PRECISION NOT NULL,
    dropoff_lat DOUBLE PRECISION NOT NULL,
    dropoff_lng DOUBLE PRECISION NOT NULL,
    
    -- OTP & Proof
    otp_code VARCHAR(10),
    pickup_photo_url TEXT,
    delivery_photo_url TEXT,
    
    -- Flags
    is_return BOOLEAN DEFAULT TRUE,
    
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_return_gigs_tracking ON return_gigs(tracking_id);
CREATE INDEX IF NOT EXISTS idx_return_gigs_order ON return_gigs(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_return_gigs_return_request ON return_gigs(return_request_id);
CREATE INDEX IF NOT EXISTS idx_return_gigs_status ON return_gigs(status);
CREATE INDEX IF NOT EXISTS idx_return_gigs_rider ON return_gigs(assigned_rider_id);

COMMENT ON TABLE return_gigs IS 'Return delivery gigs for rider pickup from customer → vendor store';
COMMENT ON COLUMN return_gigs.is_return IS 'Always TRUE for return gigs (distinguishes from normal delivery gigs)';
COMMENT ON COLUMN return_gigs.otp_code IS 'OTP for customer verification on pickup';
