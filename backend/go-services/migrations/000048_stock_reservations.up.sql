-- Migration 000048: Stock Reservations table for saga-based order creation
-- Ensures atomic local reservation before external gRPC call, with guaranteed compensation.

CREATE TABLE IF NOT EXISTS stock_reservations (
    id                  BIGSERIAL PRIMARY KEY,
    order_tracking_id   VARCHAR(50) NOT NULL,
    product_tracking_id VARCHAR(50) NOT NULL,
    quantity            INTEGER NOT NULL CHECK (quantity > 0),
    status              VARCHAR(30) NOT NULL DEFAULT 'pending',
                                                -- pending | confirmed | failed | released
    grpc_request_id     VARCHAR(100),           -- for idempotent gRPC retry
    error_message       TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    confirmed_at        TIMESTAMPTZ,
    released_at         TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_stock_reservations_order ON stock_reservations(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_stock_reservations_status ON stock_reservations(status);
CREATE INDEX IF NOT EXISTS idx_stock_reservations_grpc ON stock_reservations(grpc_request_id);
CREATE INDEX IF NOT EXISTS idx_stock_reservations_created ON stock_reservations(created_at);

-- Trigger to auto-update updated_at
CREATE OR REPLACE FUNCTION update_stock_reservations_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_stock_reservations_updated_at ON stock_reservations;
CREATE TRIGGER trg_stock_reservations_updated_at
    BEFORE UPDATE ON stock_reservations
    FOR EACH ROW EXECUTE FUNCTION update_stock_reservations_updated_at();

-- Enforce: one reservation per order per product
CREATE UNIQUE INDEX IF NOT EXISTS ux_stock_reservation_order_product
ON stock_reservations(order_tracking_id, product_tracking_id)
WHERE status IN ('pending', 'confirmed');