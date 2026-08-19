-- Migration 0010: payment_api_keys table
-- Allows admin UI to manage merchant payment-gateway credentials at runtime.
-- Values are encrypted at rest with AES-256-GCM keyed by ADMIN_API_KEY_ENCRYPTION_KEY.
-- On rotation, the relevant service (order-service, payment-orchestrator) is
-- expected to subscribe to the `payment.keys.updated` Kafka event and hot-reload.

BEGIN;

CREATE TABLE IF NOT EXISTS payment_api_keys (
    id            UUID PRIMARY KEY,
    provider      VARCHAR(40)  NOT NULL,        -- e.g. 'stripe', 'payfast', 'jazzcash', 'easypaisa'
    key_name      VARCHAR(60)  NOT NULL,        -- e.g. 'secret_key', 'webhook_secret', 'merchant_id'
    encrypted_value BYTEA     NOT NULL,        -- nonce(12) || ciphertext || tag(16)
    version       INT          NOT NULL DEFAULT 1,
    rotated_by    VARCHAR(80)  NOT NULL,        -- admin user_tracking_id
    rotated_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (provider, key_name)
);

CREATE INDEX IF NOT EXISTS idx_payment_api_keys_provider ON payment_api_keys(provider);

-- Audit trail: who changed which key, when, and from what previous fingerprint.
CREATE TABLE IF NOT EXISTS payment_api_key_audit (
    id            BIGSERIAL PRIMARY KEY,
    payment_key_id UUID NOT NULL REFERENCES payment_api_keys(id) ON DELETE CASCADE,
    provider      VARCHAR(40)  NOT NULL,
    key_name      VARCHAR(60)  NOT NULL,
    action        VARCHAR(20)  NOT NULL,        -- 'create', 'update', 'delete'
    prev_fingerprint VARCHAR(16),               -- first 8 bytes of sha256(prev_encrypted) — opaque, no plaintext leak
    new_fingerprint  VARCHAR(16),
    actor_id      VARCHAR(80)  NOT NULL,
    actor_ip      VARCHAR(64),
    created_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_payment_api_key_audit_provider ON payment_api_key_audit(provider, created_at DESC);

COMMIT;
