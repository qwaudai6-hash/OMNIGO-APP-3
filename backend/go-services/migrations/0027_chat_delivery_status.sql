-- M1: Chat delivery status tracking (blue ticks like WhatsApp)
-- Adds delivered_at and read_at timestamps to chat_messages.

ALTER TABLE chat_messages
    ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS read_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_chat_messages_delivered_at
    ON chat_messages(delivered_at) WHERE delivered_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_chat_messages_read_at
    ON chat_messages(read_at) WHERE read_at IS NULL;
