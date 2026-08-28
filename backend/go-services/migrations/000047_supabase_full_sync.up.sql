-- ═══════════════════════════════════════════════════════════════════════════
--  000047: Full Supabase schema synchronization
--  Adds ALL columns/tables/indexes from 000040-000046 that may be missing.
--  100% idempotent — safe to run on any state.
-- ═══════════════════════════════════════════════════════════════════════════

-- ── Users: missing columns from baseline ────────────────────────────────
ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS two_factor_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS background_check_url TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS vehicle_registration_url TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS vehicle_plate_number VARCHAR(30);
ALTER TABLE users ADD COLUMN IF NOT EXISTS business_name VARCHAR(255);
ALTER TABLE users ADD COLUMN IF NOT EXISTS address TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS latitude DOUBLE PRECISION;
ALTER TABLE users ADD COLUMN IF NOT EXISTS longitude DOUBLE PRECISION;
DROP CONSTRAINT IF EXISTS users_phone_key;

-- ── Stores: missing columns ────────────────────────────────────────────
ALTER TABLE stores ADD COLUMN IF NOT EXISTS commission_rate NUMERIC(5,2) DEFAULT 2.00;
ALTER TABLE stores ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE stores ADD COLUMN IF NOT EXISTS logo_url TEXT;
ALTER TABLE stores ADD COLUMN IF NOT EXISTS banner_url TEXT;

-- ── Orders: ALL missing columns ────────────────────────────────────────
ALTER TABLE orders ADD COLUMN IF NOT EXISTS rider_tracking_id VARCHAR(50);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS delivery_type VARCHAR(30);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS admin_commission NUMERIC(12,2) DEFAULT 0.00;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS vendor_escrow NUMERIC(12,2) DEFAULT 0.00;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS delivery_escrow NUMERIC(12,2) DEFAULT 0.00;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS payment_status VARCHAR(30) DEFAULT 'pending';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS otp_code VARCHAR(30);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS escrow_released BOOLEAN DEFAULT FALSE;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS dispute_status VARCHAR(20) DEFAULT 'NONE';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS handover_photo_url TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS handover_at TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS handover_notes TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS handed_over_by_tracking_id VARCHAR(50);

-- ── Order Items: fix column name ───────────────────────────────────────
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='order_items' AND column_name='unit_price') THEN
        ALTER TABLE order_items RENAME COLUMN unit_price TO price_at_checkout;
    END IF;
END $$;

-- ── Deliveries: ALL missing columns ────────────────────────────────────
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS tips NUMERIC(10,2) DEFAULT 0.0;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS petrol_allowance NUMERIC(10,2) DEFAULT 0.0;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS proof_of_delivery_url TEXT;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS proof_of_delivery_type VARCHAR(20);
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS pickup_photo_url TEXT;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS delivery_photo_url TEXT;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS customer_dispute_photo_url TEXT;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS dispute_status VARCHAR(30) DEFAULT 'none';
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS cancel_reason TEXT;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS is_cod BOOLEAN DEFAULT false;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS order_total NUMERIC(10,2) DEFAULT 0.0;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS customer_phone VARCHAR(20) DEFAULT '';
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS delivery_fee NUMERIC(10,2) NOT NULL DEFAULT 0;

-- ── Rides: missing columns ─────────────────────────────────────────────
ALTER TABLE rides ADD COLUMN IF NOT EXISTS actual_distance_meters DOUBLE PRECISION;
ALTER TABLE rides ADD COLUMN IF NOT EXISTS actual_duration_seconds DOUBLE PRECISION;

-- ── Rider Wallet: missing columns ──────────────────────────────────────
ALTER TABLE rider_wallet ADD COLUMN IF NOT EXISTS cash_in_hand DECIMAL(14,2) NOT NULL DEFAULT 0.00;
ALTER TABLE rider_wallet ADD COLUMN IF NOT EXISTS lifetime_earnings DECIMAL(14,2) NOT NULL DEFAULT 0;
ALTER TABLE rider_wallet ADD COLUMN IF NOT EXISTS is_cash_blocked BOOLEAN NOT NULL DEFAULT FALSE;

-- ── Vendor Wallet: missing columns ─────────────────────────────────────
ALTER TABLE vendor_wallet ADD COLUMN IF NOT EXISTS lifetime_earnings DECIMAL(14,2) NOT NULL DEFAULT 0;
ALTER TABLE vendor_wallet ADD COLUMN IF NOT EXISTS total_payouts DECIMAL(14,2) NOT NULL DEFAULT 0;

-- ── Vendor Payouts: missing columns ────────────────────────────────────
ALTER TABLE vendor_payouts ALTER COLUMN id DROP DEFAULT;
ALTER TABLE vendor_payouts ADD COLUMN IF NOT EXISTS method VARCHAR(30) DEFAULT 'bank_transfer';
ALTER TABLE vendor_payouts ADD COLUMN IF NOT EXISTS batch_id VARCHAR(60);
ALTER TABLE vendor_payouts ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;
ALTER TABLE vendor_payouts ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='vendor_payouts' AND column_name='id' AND data_type='bigint') THEN
        ALTER TABLE vendor_payouts ALTER COLUMN id DROP DEFAULT;
        ALTER TABLE vendor_payouts ALTER COLUMN id TYPE uuid USING (md5(id::text || clock_timestamp()::text))::uuid;
    END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_vendor_payouts_batch ON vendor_payouts(batch_id);

-- ── COD Debts: missing columns ─────────────────────────────────────────
ALTER TABLE cod_debts ADD COLUMN IF NOT EXISTS amount_owed NUMERIC(10,2);
ALTER TABLE cod_debts ADD COLUMN IF NOT EXISTS webhook_event_id VARCHAR(100);
ALTER TABLE cod_debts ADD COLUMN IF NOT EXISTS settled_via VARCHAR(30);
UPDATE cod_debts SET amount_owed = amount WHERE amount_owed IS NULL AND amount IS NOT NULL;
ALTER TABLE cod_debts ALTER COLUMN amount_owed SET DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_cod_debts_order ON cod_debts(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_cod_debts_webhook_event ON cod_debts(webhook_event_id);

-- ── Escrow Holds: missing columns ─────────────────────────────────────
ALTER TABLE escrow_holds ALTER COLUMN id DROP DEFAULT;
ALTER TABLE escrow_holds ADD COLUMN IF NOT EXISTS hold_until TIMESTAMPTZ DEFAULT NOW() + INTERVAL '7 days';
ALTER TABLE escrow_holds ADD COLUMN IF NOT EXISTS dispute_id UUID;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='escrow_holds' AND column_name='id' AND data_type='bigint') THEN
        ALTER TABLE escrow_holds ALTER COLUMN id TYPE uuid USING (md5(id::text || clock_timestamp()::text))::uuid;
    END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS uq_escrow_order_vendor ON escrow_holds(order_tracking_id, vendor_tracking_id);

-- ── Disputes: missing columns ──────────────────────────────────────────
ALTER TABLE disputes ALTER COLUMN id DROP DEFAULT;
ALTER TABLE disputes ADD COLUMN IF NOT EXISTS resolved_at TIMESTAMPTZ;
ALTER TABLE disputes ADD COLUMN IF NOT EXISTS resolved_by VARCHAR(50);
ALTER TABLE disputes ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='disputes' AND column_name='id' AND data_type='bigint') THEN
        ALTER TABLE disputes ALTER COLUMN id TYPE uuid USING (md5(id::text || clock_timestamp()::text))::uuid;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='disputes' AND column_name='tracking_id') THEN
        ALTER TABLE disputes ALTER COLUMN tracking_id DROP NOT NULL;
    END IF;
END $$;

-- ── Reviews: missing columns ───────────────────────────────────────────
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- ── Ledger Entries: missing columns ────────────────────────────────────
ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS idempotency_key VARCHAR(100);
ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS signature VARCHAR(64) DEFAULT '';
ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS signature_version INT DEFAULT 1;
CREATE UNIQUE INDEX IF NOT EXISTS ux_ledger_idempotency ON ledger_entries(idempotency_key) WHERE idempotency_key IS NOT NULL;

-- ── Outbox Events: missing columns ─────────────────────────────────────
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS retry_count INT NOT NULL DEFAULT 0;
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS error_message TEXT;
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS processed_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_outbox_events_topic_status ON outbox_events(topic, status);
CREATE INDEX IF NOT EXISTS idx_outbox_events_created_at ON outbox_events(created_at);

-- ── Payment Transactions ───────────────────────────────────────────────
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
ALTER TABLE payment_transactions DROP CONSTRAINT IF EXISTS chk_payment_transactions_status;
ALTER TABLE payment_transactions ADD CONSTRAINT chk_payment_transactions_status
    CHECK (status IN ('pending', 'processing', '3ds_required', 'authorized', 'captured', 'settlement_pending', 'gateway_pending', 'failed', 'refunded', 'reversed', 'chargeback'));
CREATE UNIQUE INDEX IF NOT EXISTS ux_payment_active_order
    ON payment_transactions(order_tracking_id)
    WHERE status IN ('processing', '3ds_required', 'settlement_pending', 'gateway_pending');

-- ── Payment Idempotency ────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS payment_idempotency (
    key            TEXT PRIMARY KEY,
    request_hash   TEXT NOT NULL,
    transaction_id TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at     TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '24 hours'
);
CREATE INDEX IF NOT EXISTS idx_payment_idempotency_expires ON payment_idempotency(expires_at);

-- ── Stripe Events ──────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS stripe_events (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stripe_event_id   TEXT NOT NULL UNIQUE,
    event_type        TEXT NOT NULL,
    payload           JSONB NOT NULL,
    received_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at      TIMESTAMPTZ,
    process_error     TEXT,
    order_id          TEXT,
    payment_intent_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_stripe_events_type ON stripe_events (event_type);
CREATE INDEX IF NOT EXISTS idx_stripe_events_unprocessed ON stripe_events (received_at) WHERE processed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_stripe_events_order ON stripe_events (order_id) WHERE order_id IS NOT NULL;

-- ── Customer Wallet ────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS customer_wallet (
    id BIGSERIAL PRIMARY KEY,
    customer_tracking_id VARCHAR(50) UNIQUE NOT NULL,
    balance DECIMAL(14,2) NOT NULL DEFAULT 0.00,
    lifetime_spent DECIMAL(14,2) NOT NULL DEFAULT 0.00,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── Customer Saved Cards ───────────────────────────────────────────────
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

-- ── Chat Messages ──────────────────────────────────────────────────────
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

-- ── Auth Flow Tables ───────────────────────────────────────────────────
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

-- ── Payment API Keys ───────────────────────────────────────────────────
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

-- ── Ride Bids ──────────────────────────────────────────────────────────
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

-- ── Composite Indexes for Workers ──────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_orders_status_created ON orders (status, created_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_deliveries_status_created ON deliveries (status, created_at) WHERE status = 'broadcasting';

-- ── Hot Path Indexes ───────────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_deliveries_customer ON deliveries(customer_tracking_id);
CREATE INDEX IF NOT EXISTS idx_orders_store_tracking ON orders(store_tracking_id);
CREATE INDEX IF NOT EXISTS idx_favorites_product ON favorites(product_tracking_id);
CREATE INDEX IF NOT EXISTS idx_orders_payment_status ON orders(payment_status);
CREATE INDEX IF NOT EXISTS idx_orders_rider ON orders(rider_tracking_id);

-- ── Review Uniqueness ──────────────────────────────────────────────────
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'uq_reviews_product_customer') THEN
        ALTER TABLE reviews ADD CONSTRAINT uq_reviews_product_customer UNIQUE (product_tracking_id, user_tracking_id);
    END IF;
END $$;

-- ── Delivery Status Check (allow cancelled) ────────────────────────────
DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_deliveries_status'
          AND pg_get_constraintdef(oid) NOT LIKE '%cancelled%'
    ) THEN
        ALTER TABLE deliveries DROP CONSTRAINT chk_deliveries_status;
    END IF;
END $$;
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_deliveries_status') THEN
        ALTER TABLE deliveries ADD CONSTRAINT chk_deliveres_status
            CHECK (status IN ('broadcasting','assigned','accepted','picked_up',
                              'in_transit','completed','failed','cancelled','disputed'));
    END IF;
END $$;

-- ── Device Tokens: fix schema (use user_tracking_id not user_id FK) ───
CREATE TABLE IF NOT EXISTS device_tokens_v2 (
    id          BIGSERIAL PRIMARY KEY,
    user_tracking_id VARCHAR(50) NOT NULL,
    fcm_token   TEXT NOT NULL,
    platform    VARCHAR(10),
    is_active   BOOLEAN DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_tracking_id, fcm_token)
);
INSERT INTO device_tokens_v2 (user_tracking_id, fcm_token, platform, created_at, updated_at)
SELECT CAST(user_id AS TEXT), token, platform, created_at, updated_at
FROM device_tokens
ON CONFLICT DO NOTHING;
DROP TABLE IF EXISTS device_tokens;
ALTER TABLE device_tokens_v2 RENAME TO device_tokens;
CREATE INDEX IF NOT EXISTS idx_device_tokens_user ON device_tokens(user_tracking_id);
