-- Migration 0013: Add missing topic column to outbox_events
-- The 0001_init migration created outbox_events without the topic column.
-- The Go code inserts topic='orders.created' but the column doesn't exist.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'outbox_events' AND column_name = 'topic') THEN
        ALTER TABLE outbox_events ADD COLUMN topic VARCHAR(100);
        CREATE INDEX IF NOT EXISTS idx_outbox_events_topic ON outbox_events(topic);
    END IF;
END $$;
