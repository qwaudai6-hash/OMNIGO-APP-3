-- Migration 0024: Prevent duplicate active delivery gigs per order
--
-- Problem: BUG-03 (TOCTOU race in CreateGig). Two concurrent Kafka
-- handlers processing the same orders.created event could both pass the
-- check-then-act guard and INSERT duplicate rows. Production data shows
-- up to 30 duplicate delivery rows per order for some orders.
--
-- The application code (CreateGig) now serializes concurrent inserts
-- using SELECT ... FOR UPDATE on the parent order. This migration adds
-- a database-level safety net as a second line of defense: a partial
-- UNIQUE index that only allows ONE active gig (non-cancelled,
-- non-completed) per order.
--
-- The full UNIQUE on order_tracking_id cannot be added because
-- cancelled/completed gigs are kept for audit history — cancelled gigs
-- from previous orders are expected. The partial index allows multiple
-- historical rows but only one active one.
--
-- IMPORTANT: Before applying this migration, clean up the existing
-- duplicate active rows in production:
--   DELETE FROM deliveries a USING deliveries b
--   WHERE a.order_tracking_id = b.order_tracking_id
--     AND a.status NOT IN ('cancelled', 'completed')
--     AND b.status NOT IN ('cancelled', 'completed')
--     AND a.ctid < b.ctid;
-- This keeps only the oldest active gig per order.

CREATE UNIQUE INDEX IF NOT EXISTS ux_deliveries_active_order
ON deliveries (order_tracking_id)
WHERE status NOT IN ('cancelled', 'completed');
