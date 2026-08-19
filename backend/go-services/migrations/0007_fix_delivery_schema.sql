-- Migration 0007: Fix delivery schema
-- 1. Add vendor_store_tracking_id column
-- 2. Add CHECK constraint on status column
-- 3. Add cancel_status for rider-initiated cancellation

BEGIN;

-- 1. Add vendor_store_tracking_id (nullable for existing rows)
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS vendor_store_tracking_id VARCHAR(50);

-- 2. Add CHECK constraint on status to enforce valid values at DB level
ALTER TABLE deliveries ADD CONSTRAINT chk_deliveries_status
    CHECK (status IN ('broadcasting', 'accepted', 'picked_up', 'in_transit', 'completed', 'failed'));

-- 3. Add cancel_reason column (for rider cancellation tracking)
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS cancel_reason TEXT;

COMMIT;
