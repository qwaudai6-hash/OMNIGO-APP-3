-- ════════════════════════════════════════════════════════════════
--  OMNIGO Platform — Migration 0017
--  Adds `resolved_at` to the disputes table. The dispute handler
--  (`payment_orchestrator/handlers/dispute_handler.go:142`) was
--  writing to this column on every resolution, which failed at
--  runtime because the column never existed.
-- ════════════════════════════════════════════════════════════════

ALTER TABLE disputes
    ADD COLUMN IF NOT EXISTS resolved_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS resolved_by_tracking_id VARCHAR(50);

CREATE INDEX IF NOT EXISTS idx_disputes_resolved_at
    ON disputes (resolved_at)
    WHERE resolved_at IS NOT NULL;
