-- Production-grade Stripe webhook events table.
-- Pattern: insert-first, process-second with UNIQUE constraint for dedup.
-- Source: theroadtoenterprise.com + monstar-lab.com production patterns.
CREATE TABLE IF NOT EXISTS stripe_events (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stripe_event_id   TEXT NOT NULL UNIQUE,          -- evt_xxx — dedup boundary
    event_type        TEXT NOT NULL,                  -- payment_intent.succeeded etc.
    payload           JSONB NOT NULL,                 -- Full Stripe event for replay
    received_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at      TIMESTAMPTZ,                    -- NULL = unprocessed (replay worker picks up)
    process_error     TEXT,                            -- Error reason if processing failed
    order_id          TEXT,                            -- Extracted for fast lookups
    payment_intent_id TEXT                             -- Extracted for fast lookups
);

-- Fast lookup by event type (audit, debugging)
CREATE INDEX IF NOT EXISTS idx_stripe_events_type ON stripe_events (event_type);

-- Partial index: unprocessed events (replay worker uses this)
CREATE INDEX IF NOT EXISTS idx_stripe_events_unprocessed
    ON stripe_events (received_at)
    WHERE processed_at IS NULL;

-- Fast lookup by order_id (customer support, reconciliation)
CREATE INDEX IF NOT EXISTS idx_stripe_events_order ON stripe_events (order_id)
    WHERE order_id IS NOT NULL;

-- Auto-cleanup: events older than 30 days can be archived
-- (handled by a scheduled job, not this migration)
