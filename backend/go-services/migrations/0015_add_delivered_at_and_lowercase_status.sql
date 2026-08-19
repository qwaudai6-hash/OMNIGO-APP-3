-- ════════════════════════════════════════════════════════════════
--  OMNIGO Platform — Migration 0015
--  Adds `delivered_at` to orders so the escrow release cron can
--  select eligible rows after the 48-hour dispute window. Also adds
--  a CHECK constraint that pins the canonical lowercase order
--  status enum so the Flutter timeline in
--  `order_detail_screen.dart` and the Go status writes can't drift
--  apart again.
-- ════════════════════════════════════════════════════════════════

-- 1. Add delivered_at column (idempotent).
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ;

-- 2. Back-fill delivered_at for any historical rows that already
-- have a non-pending / non-cancelled status. This is best-effort —
-- moving forward, MarkOrderDelivered() will set it on every
-- delivery-completed event.
UPDATE orders
SET delivered_at = NOW()
WHERE delivered_at IS NULL
  AND status IN ('delivered', 'completed', 'shipped', 'in_transit');

-- 3. Drop the old (case-insensitive) constraint if any. We don't
-- currently have one, but this is a no-op safety net for future
-- schema drift.
ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_status;

-- 4. Add the canonical lowercase enum constraint. If the column
-- already has a rogue value (e.g. legacy 'SHIPPED' uppercase),
-- normalise it first so the constraint can be installed.
UPDATE orders SET status = LOWER(status) WHERE status <> LOWER(status);

ALTER TABLE orders
    ADD CONSTRAINT chk_orders_status CHECK (
        status IN (
            'pending',
            'accepted',
            'shipped',
            'in_transit',
            'delivered',
            'completed',
            'cancelled',
            'failed',
            'refunded'
        )
    );

-- 5. Index for the escrow-cron lookups.
CREATE INDEX IF NOT EXISTS idx_orders_delivered_at
    ON orders (delivered_at)
    WHERE delivered_at IS NOT NULL;
