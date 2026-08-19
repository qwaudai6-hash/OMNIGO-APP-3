-- Migration: add DB-level unique constraint for checkout idempotency.
-- Fail-closed guard against duplicate orders if Redis SetNX is unavailable.

ALTER TABLE orders
ADD COLUMN IF NOT EXISTS device_session_nonce VARCHAR(64);

ALTER TABLE orders
ADD CONSTRAINT orders_idempotency_unique
UNIQUE (customer_tracking_id, device_session_nonce);

CREATE INDEX IF NOT EXISTS idx_orders_device_session_nonce
ON orders(device_session_nonce);
