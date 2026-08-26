-- 1. update user_refresh_tokens
ALTER TABLE user_refresh_tokens ALTER COLUMN id SET DEFAULT gen_random_uuid();
ALTER TABLE user_refresh_tokens ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- 2. update users
ALTER TABLE users ADD COLUMN IF NOT EXISTS background_check_url TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS two_factor_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_phone_key;

-- 3. update orders
ALTER TABLE orders ADD COLUMN IF NOT EXISTS vendor_escrow NUMERIC(12,2) DEFAULT 0.00;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS delivery_escrow NUMERIC(12,2) DEFAULT 0.00;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS payment_status VARCHAR(30) DEFAULT 'pending';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS escrow_released BOOLEAN DEFAULT FALSE;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS dispute_status VARCHAR(20) DEFAULT 'NONE';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS handover_photo_url TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS handover_at TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS handover_notes TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS handed_over_by_tracking_id VARCHAR(50);

-- 4. update deliveries
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS vendor_store_tracking_id VARCHAR(50);
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS customer_tracking_id VARCHAR(50);

-- 5. update rides
ALTER TABLE rides ADD COLUMN IF NOT EXISTS vehicle_type VARCHAR(30) NOT NULL DEFAULT 'bike';
ALTER TABLE rides ADD COLUMN IF NOT EXISTS actual_distance_meters DOUBLE PRECISION;
ALTER TABLE rides ADD COLUMN IF NOT EXISTS actual_duration_seconds DOUBLE PRECISION;

-- 6. update rider_wallet
ALTER TABLE rider_wallet ADD COLUMN IF NOT EXISTS cash_in_hand DECIMAL(14,2) NOT NULL DEFAULT 0.00;
ALTER TABLE rider_wallet ADD COLUMN IF NOT EXISTS is_cash_blocked BOOLEAN NOT NULL DEFAULT FALSE;

-- 7. update stores
ALTER TABLE stores ADD COLUMN IF NOT EXISTS banner_url TEXT;
ALTER TABLE stores ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT true;

-- 8. update outbox_events
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS retry_count INT NOT NULL DEFAULT 0;
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS error_message TEXT;

-- 9. update disputes
ALTER TABLE disputes ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- 10. update vendor_payouts
ALTER TABLE vendor_payouts ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- 11. update reviews
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'reviews' AND column_name = 'customer_tracking_id'
    ) THEN
        ALTER TABLE reviews RENAME COLUMN customer_tracking_id TO user_tracking_id;
    END IF;
END $$;

-- 11b. update favorites
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'favorites' AND column_name = 'customer_tracking_id'
    ) THEN
        ALTER TABLE favorites RENAME COLUMN customer_tracking_id TO user_tracking_id;
    END IF;
END $$;

-- 12. update ledger_entries
ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS signature VARCHAR(64) DEFAULT '';
ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS signature_version INT DEFAULT 1;

-- 13. Add 10 missing tables

CREATE TABLE IF NOT EXISTS payment_transactions (
    id                  BIGSERIAL PRIMARY KEY,
    transaction_id      VARCHAR(100) UNIQUE NOT NULL,
    order_tracking_id   VARCHAR(50) NOT NULL,
    gateway             VARCHAR(30) NOT NULL,
    gateway_txn_id      VARCHAR(255),
    amount              NUMERIC(12,2) NOT NULL,
    currency            VARCHAR(10) NOT NULL DEFAULT 'PKR',
    status              VARCHAR(30) NOT NULL DEFAULT 'pending',
    kind                VARCHAR(30) NOT NULL DEFAULT 'payment',
    idempotency_key     VARCHAR(255) UNIQUE,
    metadata            JSONB,
    error_message       TEXT,
    callback_processed_at TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payment_transactions_order ON payment_transactions(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_payment_transactions_gateway_txn ON payment_transactions(gateway_txn_id);
CREATE INDEX IF NOT EXISTS idx_payment_transactions_status ON payment_transactions(status);
CREATE INDEX IF NOT EXISTS idx_payment_transactions_idempotency ON payment_transactions(idempotency_key);
CREATE INDEX IF NOT EXISTS idx_payment_transactions_created_at ON payment_transactions(created_at DESC);

ALTER TABLE payment_transactions
    DROP CONSTRAINT IF EXISTS chk_payment_transactions_status;
ALTER TABLE payment_transactions
    ADD CONSTRAINT chk_payment_transactions_status
    CHECK (status IN ('pending', 'processing', '3ds_required', 'authorized', 'captured', 'settlement_pending', 'gateway_pending', 'failed', 'refunded', 'reversed', 'chargeback'));

CREATE UNIQUE INDEX IF NOT EXISTS ux_payment_active_order
ON payment_transactions(order_tracking_id)
WHERE status IN ('processing', '3ds_required', 'settlement_pending', 'gateway_pending');

CREATE TABLE IF NOT EXISTS payment_idempotency (
    key             VARCHAR(255) PRIMARY KEY,
    request_hash    VARCHAR(64) NOT NULL,
    transaction_id  VARCHAR(100),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_payment_idempotency_expires ON payment_idempotency(expires_at);

CREATE UNIQUE INDEX IF NOT EXISTS uq_escrow_order_vendor ON escrow_holds(order_tracking_id, vendor_tracking_id);

CREATE TABLE IF NOT EXISTS customer_wallet (
    id BIGSERIAL PRIMARY KEY,
    customer_tracking_id VARCHAR(50) UNIQUE NOT NULL,
    balance DECIMAL(14,2) NOT NULL DEFAULT 0.00,
    lifetime_spent DECIMAL(14,2) NOT NULL DEFAULT 0.00,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS carts (
    id BIGSERIAL PRIMARY KEY,
    user_id VARCHAR(50) UNIQUE NOT NULL,
    store_id VARCHAR(50),
    total_amount NUMERIC(12,2) DEFAULT 0.0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_carts_user ON carts(user_id);
CREATE INDEX IF NOT EXISTS idx_carts_store ON carts(store_id);

CREATE TABLE IF NOT EXISTS cart_items (
    id BIGSERIAL PRIMARY KEY,
    cart_id BIGINT NOT NULL,
    product_id BIGINT NOT NULL,
    quantity INT NOT NULL CHECK (quantity > 0),
    price NUMERIC(12,2) NOT NULL DEFAULT 0.0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(cart_id, product_id)
);
CREATE INDEX IF NOT EXISTS idx_cart_items_cart ON cart_items(cart_id);
CREATE INDEX IF NOT EXISTS idx_cart_items_product ON cart_items(product_id);

CREATE TABLE IF NOT EXISTS ride_bids (
    id BIGSERIAL PRIMARY KEY,
    tracking_id VARCHAR(50) UNIQUE,
    ride_tracking_id VARCHAR(50) NOT NULL,
    rider_tracking_id VARCHAR(50) NOT NULL,
    bid_amount DECIMAL(10,2) NOT NULL,
    status VARCHAR(30) NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS chat_messages (
    id VARCHAR(64) PRIMARY KEY,
    order_id VARCHAR(50) NOT NULL,
    sender_id VARCHAR(50) NOT NULL,
    receiver_id VARCHAR(50) NOT NULL,
    content TEXT NOT NULL,
    is_read BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_chat_messages_order_id ON chat_messages(order_id);
CREATE INDEX IF NOT EXISTS idx_chat_messages_sender_id ON chat_messages(sender_id);
CREATE INDEX IF NOT EXISTS idx_chat_messages_receiver_id ON chat_messages(receiver_id);

CREATE TABLE IF NOT EXISTS customer_saved_cards (
    id                   BIGSERIAL PRIMARY KEY,
    card_id              VARCHAR(100) UNIQUE NOT NULL,
    customer_tracking_id VARCHAR(50) NOT NULL,
    gateway              VARCHAR(30) NOT NULL DEFAULT 'payfast',
    instrument_token     VARCHAR(255) NOT NULL,
    card_brand           VARCHAR(30) NOT NULL,
    last_four            VARCHAR(4) NOT NULL,
    expiry_month         VARCHAR(2) NOT NULL,
    expiry_year          VARCHAR(4) NOT NULL,
    cardholder_name      VARCHAR(100),
    is_default           BOOLEAN NOT NULL DEFAULT false,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_saved_cards_customer ON customer_saved_cards(customer_tracking_id);
CREATE INDEX IF NOT EXISTS idx_saved_cards_token ON customer_saved_cards(instrument_token);

CREATE TABLE IF NOT EXISTS password_reset_tokens (
    id                BIGSERIAL PRIMARY KEY,
    user_tracking_id  VARCHAR(50) NOT NULL,
    token_hash        VARCHAR(128) NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    used_at           TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (token_hash)
);

CREATE INDEX IF NOT EXISTS idx_pwd_reset_user ON password_reset_tokens(user_tracking_id);
CREATE INDEX IF NOT EXISTS idx_pwd_reset_expires ON password_reset_tokens(expires_at);

CREATE TABLE IF NOT EXISTS email_verification_tokens (
    id                BIGSERIAL PRIMARY KEY,
    user_tracking_id  VARCHAR(50) NOT NULL,
    email             VARCHAR(255) NOT NULL,
    token_hash        VARCHAR(128) NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    verified_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (token_hash)
);

CREATE INDEX IF NOT EXISTS idx_email_verify_user ON email_verification_tokens(user_tracking_id);
CREATE INDEX IF NOT EXISTS idx_email_verify_expires ON email_verification_tokens(expires_at);

CREATE TABLE IF NOT EXISTS user_2fa_secrets (
    user_tracking_id  VARCHAR(50) PRIMARY KEY,
    secret_encrypted  TEXT NOT NULL,
    enabled           BOOLEAN NOT NULL DEFAULT false,
    enrolled_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at      TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS payment_api_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider        VARCHAR(40) NOT NULL,
    key_name        VARCHAR(60) NOT NULL,
    encrypted_value BYTEA NOT NULL,
    version         INT NOT NULL DEFAULT 1,
    is_active       BOOLEAN DEFAULT true,
    rotated_by      VARCHAR(80) DEFAULT 'admin',
    rotated_at      TIMESTAMPTZ DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (provider, key_name)
);

CREATE INDEX IF NOT EXISTS idx_payment_api_keys_provider ON payment_api_keys(provider);

CREATE TABLE IF NOT EXISTS payment_api_key_audit (
    id               BIGSERIAL PRIMARY KEY,
    payment_key_id   UUID,
    provider         VARCHAR(40) NOT NULL,
    key_name         VARCHAR(60) NOT NULL,
    action           VARCHAR(20) NOT NULL,
    prev_fingerprint VARCHAR(16),
    new_fingerprint  VARCHAR(16),
    actor            VARCHAR(50),
    actor_id         VARCHAR(80),
    actor_ip         VARCHAR(64),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payment_api_key_audit_provider ON payment_api_key_audit(provider, created_at DESC);
