-- M3: PayFast IPN event logging — mirrors stripe_events pattern.
-- Stores raw IPN payloads for audit trail, replay, and debugging.

CREATE TABLE IF NOT EXISTS payfast_events (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    basket_id           TEXT NOT NULL,
    gateway_txn_id      TEXT,
    event_type          TEXT NOT NULL DEFAULT 'ipn_received',
    status_code         TEXT,
    amount              NUMERIC(12,2),
    payload             JSONB NOT NULL,
    received_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at        TIMESTAMPTZ,
    process_error       TEXT,
    order_id            TEXT
);

CREATE INDEX idx_payfast_events_type ON payfast_events (event_type);
CREATE INDEX idx_payfast_events_unprocessed ON payfast_events (received_at) WHERE processed_at IS NULL;
CREATE INDEX idx_payfast_events_order ON payfast_events (order_id) WHERE order_id IS NOT NULL;
CREATE UNIQUE INDEX idx_payfast_events_dedup ON payfast_events (basket_id, gateway_txn_id)
    WHERE gateway_txn_id IS NOT NULL;
CREATE UNIQUE INDEX idx_payfast_events_basket_only ON payfast_events (basket_id)
    WHERE gateway_txn_id IS NULL;
