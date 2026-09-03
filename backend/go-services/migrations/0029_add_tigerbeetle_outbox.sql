-- TigerBeetle Outbox: ensures all TB writes are relayed reliably
-- Fixes H5: fire-and-forget goroutines → transactional outbox

CREATE TABLE IF NOT EXISTS tigerbeetle_outbox (
    id              BIGSERIAL PRIMARY KEY,
    transaction_id  UUID NOT NULL,
    payload         JSONB NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    -- PENDING → PROCESSING → COMPLETED | FAILED
    retry_count     INT NOT NULL DEFAULT 0,
    max_retries     INT NOT NULL DEFAULT 10,
    error_message   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at    TIMESTAMPTZ
);

-- Fast lookup for relay worker (only PENDING rows)
CREATE INDEX idx_tb_outbox_pending
    ON tigerbeetle_outbox (created_at)
    WHERE status = 'PENDING';

-- Dedup: prevent duplicate enqueues for the same transaction
CREATE UNIQUE INDEX idx_tb_outbox_txid
    ON tigerbeetle_outbox (transaction_id)
    WHERE status IN ('PENDING', 'PROCESSING');
