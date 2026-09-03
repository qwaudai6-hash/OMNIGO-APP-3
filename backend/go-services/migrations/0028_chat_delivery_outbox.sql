-- M2: Chat outbox for reliable WS delivery.
-- Tracks which messages need WebSocket delivery retry.

CREATE TABLE IF NOT EXISTS chat_delivery_outbox (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id      TEXT NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,
    order_id        TEXT NOT NULL,
    receiver_id     TEXT NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'pending',  -- pending, delivered, failed
    attempts        INT NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    next_retry_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_chat_outbox_pending ON chat_delivery_outbox (next_retry_at)
    WHERE status = 'pending';
CREATE UNIQUE INDEX idx_chat_outbox_message ON chat_delivery_outbox (message_id);
