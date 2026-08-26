-- Migration: 0023_add_unique_escrow_hold.sql
-- Description: Adds a unique index on escrow_holds (order_tracking_id, vendor_tracking_id)
-- to prevent duplicate escrow hold creation on retries.

CREATE UNIQUE INDEX IF NOT EXISTS uq_escrow_order_vendor 
    ON escrow_holds(order_tracking_id, vendor_tracking_id);
