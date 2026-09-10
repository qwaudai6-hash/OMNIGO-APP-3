-- Migration: 0051_return_requests.sql
-- Description: Creates return_requests table and adds return_deadline to orders.

-- Add return_deadline to orders table
ALTER TABLE orders ADD COLUMN IF NOT EXISTS return_deadline TIMESTAMPTZ;

-- Create return_requests table
CREATE TABLE IF NOT EXISTS return_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_tracking_id VARCHAR(100) NOT NULL,
    customer_tracking_id VARCHAR(100) NOT NULL,
    vendor_tracking_id VARCHAR(100) NOT NULL,
    store_tracking_id VARCHAR(100) NOT NULL,
    rider_tracking_id VARCHAR(100),
    gig_tracking_id VARCHAR(100),

    -- Return details
    reason TEXT NOT NULL,
    return_items JSONB NOT NULL DEFAULT '[]',

    -- Status tracking
    status VARCHAR(30) NOT NULL DEFAULT 'return_requested'
        CHECK (status IN (
            'return_requested',
            'rider_assigned',
            'return_pickup_completed',
            'return_in_transit',
            'return_delivered',
            'return_verified',
            'return_disputed',
            'return_completed',
            'return_cancelled'
        )),

    -- Time tracking
    requested_at TIMESTAMPTZ DEFAULT NOW(),
    pickup_deadline TIMESTAMPTZ,
    verified_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,

    -- Proof
    pickup_photo_url TEXT,
    delivery_photo_url TEXT,
    vendor_verification_photo TEXT,

    -- Payment
    return_delivery_fee_paisa BIGINT DEFAULT 0,
    payment_method VARCHAR(20),
    payment_status VARCHAR(20) DEFAULT 'pending',

    -- Escrow
    escrow_hold_id UUID,

    -- Dispute
    dispute_reason TEXT,
    dispute_resolved_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),

    CONSTRAINT positive_return_fee CHECK (return_delivery_fee_paisa >= 0)
);

CREATE INDEX IF NOT EXISTS idx_return_requests_order ON return_requests(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_return_requests_status ON return_requests(status);
CREATE INDEX IF NOT EXISTS idx_return_requests_rider ON return_requests(rider_tracking_id);
CREATE INDEX IF NOT EXISTS idx_return_requests_customer ON return_requests(customer_tracking_id);
CREATE INDEX IF NOT EXISTS idx_return_requests_vendor ON return_requests(vendor_tracking_id);

COMMENT ON TABLE return_requests IS 'Tracks product return requests with full audit trail';
COMMENT ON COLUMN return_requests.status IS 'Return lifecycle: return_requested → rider_assigned → pickup → transit → delivered → verified/disputed → completed';
COMMENT ON COLUMN return_requests.pickup_deadline IS '36 hours from delivery — customer must request return before this';
COMMENT ON COLUMN return_requests.return_items IS 'JSON array of items being returned: [{product_id, quantity, reason}]';
