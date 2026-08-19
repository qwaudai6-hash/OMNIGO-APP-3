-- ════════════════════════════════════════════════════════════════
--  OMNIGO Platform — Migration 0019
--  Hardens outbox_events table for high-throughput concurrent workers:
--    1. Adds updated_at column for state lifecycle tracking
--    2. Adds retry_count for exponential backoff failure handling
--    3. Adds error_message for dead-letter diagnostics
--    4. Adds composite index on (topic, status)
-- ════════════════════════════════════════════════════════════════

ALTER TABLE outbox_events
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS retry_count INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS error_message TEXT;

CREATE INDEX IF NOT EXISTS idx_outbox_events_topic_status ON outbox_events(topic, status);
