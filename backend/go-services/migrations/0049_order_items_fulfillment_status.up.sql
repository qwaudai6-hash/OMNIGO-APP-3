-- FINANCIAL-AUDIT FIX #3: Add fulfillment and refund status tracking to order_items.
-- Enables partial refund processing when some items are out-of-stock or unfulfilled.
-- Previous behavior: all-or-nothing refund for entire order.
-- New behavior: per-item fulfillment tracking, partial refund capability.

ALTER TABLE order_items
    ADD COLUMN IF NOT EXISTS fulfillment_status VARCHAR(30) NOT NULL DEFAULT 'fulfilled',
    ADD COLUMN IF NOT EXISTS refund_status VARCHAR(30) NOT NULL DEFAULT 'not_refunded',
    ADD COLUMN IF NOT EXISTS refund_amount_paisa BIGINT NOT NULL DEFAULT 0;

COMMENT ON COLUMN order_items.fulfillment_status IS 'pending | fulfilled | unfulfilled | cancelled';
COMMENT ON COLUMN order_items.refund_status IS 'not_refunded | partial_refunded | fully_refunded';
COMMENT ON COLUMN order_items.refund_amount_paisa IS 'Amount refunded for this specific line item in paisa';
