-- ════════════════════════════════════════════════════════════════
--  OMNIGO Platform — Migration 0020
--  Hardens payment transactions and orders for enterprise production:
--    1. Adds callback_processed_at TIMESTAMPTZ to payment_transactions (3DS replay defense)
--    2. Adds vendor_escrow and delivery_escrow NUMERIC(12,2) to orders table
--    3. Recreates unique partial index ux_payment_active_order on payment_transactions
--       covering only active inflight states ('processing', '3ds_required', 'settlement_pending', 'gateway_pending')
--       so ephemeral 'pending' timeouts don't block subsequent user retries.
-- ════════════════════════════════════════════════════════════════

-- 1. 3DS Callback Replay Defense Column
ALTER TABLE payment_transactions
    ADD COLUMN IF NOT EXISTS callback_processed_at TIMESTAMPTZ;

-- 2. Orders Table Escrow Split Columns
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS vendor_escrow NUMERIC(12,2),
    ADD COLUMN IF NOT EXISTS delivery_escrow NUMERIC(12,2);

-- 3. Refine Unique Partial Index
DROP INDEX IF EXISTS ux_payment_active_order;

CREATE UNIQUE INDEX IF NOT EXISTS ux_payment_active_order
ON payment_transactions(order_tracking_id)
WHERE status IN ('processing', '3ds_required', 'settlement_pending', 'gateway_pending');

-- 4. Outbox Event Claiming Composite Index (High Concurrency FOR UPDATE SKIP LOCKED Optimization)
CREATE INDEX IF NOT EXISTS idx_outbox_events_status_created 
ON outbox_events(status, created_at);

