-- Migration 0008: Add customer tracking id to deliveries

BEGIN;

ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS customer_tracking_id VARCHAR(50);

COMMIT;
