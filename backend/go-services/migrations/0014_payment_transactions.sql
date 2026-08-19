-- Migration 0014: payment_transactions table and related indexes
-- Stores every payment attempt/capture/refund and idempotency for order payments.

CREATE TABLE IF NOT EXISTS payment_transactions (
    id                  BIGSERIAL PRIMARY KEY,
    transaction_id      VARCHAR(100) UNIQUE NOT NULL,   -- internal OMNIGO txn id
    order_tracking_id   VARCHAR(50) NOT NULL,
    gateway             VARCHAR(30) NOT NULL,         -- stripe | payfast | jazzcash | easypaisa | cod | wallet
    gateway_txn_id      VARCHAR(255),                  -- external gateway reference when available
    amount              NUMERIC(12,2) NOT NULL,
    currency            VARCHAR(10) NOT NULL DEFAULT 'PKR',
    status              VARCHAR(30) NOT NULL DEFAULT 'pending',
                                          -- pending | authorized | captured | failed | refunded | reversed | chargeback
    kind                VARCHAR(30) NOT NULL DEFAULT 'payment',
                                          -- payment | refund | reversal | wallet_load | payout
    idempotency_key     VARCHAR(255) UNIQUE,
    metadata            JSONB,
    error_message       TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payment_transactions_order ON payment_transactions(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_payment_transactions_gateway_txn ON payment_transactions(gateway_txn_id);
CREATE INDEX IF NOT EXISTS idx_payment_transactions_status ON payment_transactions(status);
CREATE INDEX IF NOT EXISTS idx_payment_transactions_idempotency ON payment_transactions(idempotency_key);
CREATE INDEX IF NOT EXISTS idx_payment_transactions_created_at ON payment_transactions(created_at DESC);

-- Soft constraint to help catch obvious bad statuses without breaking future gateways.
ALTER TABLE payment_transactions
    DROP CONSTRAINT IF EXISTS chk_payment_transactions_status;
ALTER TABLE payment_transactions
    ADD CONSTRAINT chk_payment_transactions_status
    CHECK (status IN ('pending', 'processing', '3ds_required', 'authorized', 'captured', 'settlement_pending', 'gateway_pending', 'failed', 'refunded', 'reversed', 'chargeback'));

-- Concurrency protection: only one active payment attempt per order at any time
CREATE UNIQUE INDEX IF NOT EXISTS ux_payment_active_order
ON payment_transactions(order_tracking_id)
WHERE status IN ('pending', 'processing', '3ds_required', 'settlement_pending', 'gateway_pending');

-- Helper for idempotency: if a caller re-uses an idempotency key, return existing record.
-- This is enforced by the UNIQUE index on idempotency_key above.

-- Update orders.payment_status automatically when a payment transaction is captured or refunded.
CREATE OR REPLACE FUNCTION update_order_payment_status()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.status = 'captured' THEN
        UPDATE orders SET payment_status = 'paid', updated_at = NOW()
        WHERE order_tracking_id = NEW.order_tracking_id AND payment_status <> 'paid';
    ELSIF NEW.status = 'refunded' OR NEW.status = 'reversed' OR NEW.status = 'chargeback' THEN
        UPDATE orders SET payment_status = 'refunded', updated_at = NOW()
        WHERE order_tracking_id = NEW.order_tracking_id;
    ELSIF NEW.status = 'failed' THEN
        -- Do not overwrite 'paid' from a failed retry; only mark unpaid if currently pending.
        UPDATE orders SET payment_status = 'unpaid', updated_at = NOW()
        WHERE order_tracking_id = NEW.order_tracking_id AND payment_status = 'pending';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_payment_transactions_update_order ON payment_transactions;
CREATE TRIGGER trg_payment_transactions_update_order
    AFTER INSERT OR UPDATE OF status ON payment_transactions
    FOR EACH ROW
    EXECUTE FUNCTION update_order_payment_status();

-- Create payment_idempotency table for explicit key locking during in-flight requests.
CREATE TABLE IF NOT EXISTS payment_idempotency (
    key             VARCHAR(255) PRIMARY KEY,
    request_hash    VARCHAR(64) NOT NULL,   -- sha256 of payload for safety
    transaction_id  VARCHAR(100),           -- internal txn once known
    locked_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '24 hours'
);

CREATE INDEX IF NOT EXISTS idx_payment_idempotency_expires ON payment_idempotency(expires_at);

-- Lightweight cleanup of expired idempotency locks (optional, can also be done via cron).
CREATE OR REPLACE FUNCTION cleanup_expired_payment_idempotency()
RETURNS integer AS $$
DECLARE
    deleted_count integer;
BEGIN
    DELETE FROM payment_idempotency WHERE expires_at < NOW();
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN deleted_count;
END;
$$ LANGUAGE plpgsql;
