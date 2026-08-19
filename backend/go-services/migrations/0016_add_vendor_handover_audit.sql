-- ════════════════════════════════════════════════════════════════
--  OMNIGO Platform — Migration 0016
--  Adds vendor handover audit trail to the orders table. The vendor
--  records a photo and timestamp when they hand the package to the
--  rider, so the order lifecycle has evidence from both sides and
--  admin can settle any dispute.
-- ════════════════════════════════════════════════════════════════

ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS handover_photo_url TEXT,
    ADD COLUMN IF NOT EXISTS handover_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS handover_notes TEXT,
    ADD COLUMN IF NOT EXISTS handed_over_by_tracking_id VARCHAR(50);

CREATE INDEX IF NOT EXISTS idx_orders_handover_at
    ON orders (handover_at)
    WHERE handover_at IS NOT NULL;
