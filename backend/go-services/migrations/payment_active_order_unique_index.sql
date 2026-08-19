-- Migration: Add unique partial index to prevent duplicate active payment attempts
-- This enforces at the database level that only one payment attempt can be active
-- (pending/processing/3ds_required/settlement_pending) for a given order at any time.
-- This is the real concurrency guard — the application-level SELECT count(*) check
-- in the handler provides a user-friendly error message but cannot prevent race conditions.

CREATE UNIQUE INDEX IF NOT EXISTS ux_payment_active_order
ON payment_transactions(order_tracking_id)
WHERE status IN ('pending', 'processing', '3ds_required', 'settlement_pending');
