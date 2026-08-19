-- ═══════════════════════════════════════════════════════════════════════════
--  OMNIGO Platform — Canonical Baseline Schema (golang-migrate v1)
--  Idempotent, versioned, no foreign keys (app-level integrity).
-- ═══════════════════════════════════════════════════════════════════════════

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ── Users ───────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS users (
    id              BIGSERIAL PRIMARY KEY,
    tracking_id     VARCHAR(50) UNIQUE NOT NULL,
    email           VARCHAR(255) UNIQUE NOT NULL,
    full_name       VARCHAR(255) NOT NULL,
    password_hash   TEXT NOT NULL,
    role            VARCHAR(30) NOT NULL DEFAULT 'customer',
    region          VARCHAR(10) NOT NULL DEFAULT 'PK',
    phone           VARCHAR(20),
    cnic_url        TEXT,
    cnic_back_url   TEXT,
    license_url     TEXT,
    vehicle_registration_url TEXT,
    vehicle_type    VARCHAR(30),
    vehicle_plate_number VARCHAR(30),
    business_name   VARCHAR(255),
    ntn_number      VARCHAR(50),
    address         TEXT,
    latitude        DOUBLE PRECISION,
    longitude       DOUBLE PRECISION,
    entity_type     VARCHAR(20),
    background_check_url TEXT,
    is_verified     BOOLEAN NOT NULL DEFAULT false,
    verification_status VARCHAR(30) DEFAULT 'pending',
    verification_reason TEXT,
    risk_score      REAL DEFAULT 0.0,
    email_verified  BOOLEAN NOT NULL DEFAULT false,
    two_factor_enabled BOOLEAN NOT NULL DEFAULT false,
    submitted_at    TIMESTAMPTZ,
    verified_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_tracking_id ON users(tracking_id);
CREATE INDEX IF NOT EXISTS idx_users_role ON users(role);
CREATE INDEX IF NOT EXISTS idx_users_verification_status ON users(verification_status);
CREATE INDEX IF NOT EXISTS idx_users_risk_score ON users(risk_score);

-- ── Refresh Tokens ──────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS user_refresh_tokens (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             BIGINT,
    user_tracking_id    VARCHAR(50) NOT NULL,
    token_hash          TEXT UNIQUE NOT NULL,
    expires_at          TIMESTAMPTZ NOT NULL,
    revoked             BOOLEAN NOT NULL DEFAULT false,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_hash ON user_refresh_tokens(token_hash);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_tracking ON user_refresh_tokens(user_tracking_id);

-- ── Device Tokens (FCM) ─────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS device_tokens (
    id          BIGSERIAL PRIMARY KEY,
    user_tracking_id VARCHAR(50) NOT NULL,
    fcm_token   TEXT NOT NULL,
    platform    VARCHAR(10),
    is_active   BOOLEAN DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_tracking_id, fcm_token)
);
CREATE INDEX IF NOT EXISTS idx_device_tokens_user ON device_tokens(user_tracking_id);

-- ── Stores (Vendor storefronts) ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS stores (
    id                  BIGSERIAL PRIMARY KEY,
    vendor_tracking_id  VARCHAR(50) NOT NULL,
    store_tracking_id   VARCHAR(50) UNIQUE NOT NULL,
    store_name          VARCHAR(255) NOT NULL,
    logo_url            TEXT,
    banner_url          TEXT,
    latitude            DOUBLE PRECISION,
    longitude           DOUBLE PRECISION,
    commission_rate     NUMERIC(5,2) DEFAULT 2.00,
    is_active           BOOLEAN DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_stores_vendor ON stores(vendor_tracking_id);
CREATE INDEX IF NOT EXISTS idx_stores_tracking_id ON stores(store_tracking_id);

-- ── Products ──────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS products (
    id                    BIGSERIAL PRIMARY KEY,
    product_tracking_id   VARCHAR(50) UNIQUE NOT NULL,
    vendor_tracking_id    VARCHAR(50) NOT NULL,
    store_tracking_id     VARCHAR(50),
    sku                   VARCHAR(100),
    name                  VARCHAR(255) NOT NULL,
    description           TEXT,
    base_price            NUMERIC(12,2) NOT NULL DEFAULT 0,
    stock                 INTEGER NOT NULL DEFAULT 0,
    is_featured           BOOLEAN DEFAULT false,
    image_url             TEXT,
    category              TEXT,
    is_active             BOOLEAN DEFAULT true,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_products_tracking_id ON products(product_tracking_id);
CREATE INDEX IF NOT EXISTS idx_products_vendor ON products(vendor_tracking_id);
CREATE INDEX IF NOT EXISTS idx_products_store ON products(store_tracking_id);
CREATE INDEX IF NOT EXISTS idx_products_category ON products(category, product_tracking_id);

-- ── Orders ──────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS orders (
    id                    BIGSERIAL PRIMARY KEY,
    order_tracking_id     VARCHAR(50) UNIQUE NOT NULL,
    customer_tracking_id  VARCHAR(50) NOT NULL,
    store_tracking_id     VARCHAR(50),
    vendor_tracking_id    VARCHAR(50) NOT NULL,
    rider_tracking_id     VARCHAR(50),
    status                VARCHAR(30) NOT NULL DEFAULT 'pending',
    delivery_type         VARCHAR(30),
    total_amount          NUMERIC(12,2) NOT NULL DEFAULT 0,
    admin_commission      NUMERIC(12,2) DEFAULT 0.00,
    currency              VARCHAR(10) NOT NULL DEFAULT 'PKR',
    payment_gateway       VARCHAR(30) DEFAULT 'cod',
    payment_status        VARCHAR(30) DEFAULT 'pending',
    customer_lat          DOUBLE PRECISION,
    customer_lng          DOUBLE PRECISION,
    otp_code              VARCHAR(30),
    device_session_nonce  TEXT,
    escrow_released       BOOLEAN DEFAULT FALSE,
    dispute_status        VARCHAR(20) DEFAULT 'NONE',
    delivered_at          TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_orders_tracking_id ON orders(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_orders_customer ON orders(customer_tracking_id);
CREATE INDEX IF NOT EXISTS idx_orders_vendor ON orders(vendor_tracking_id);
CREATE INDEX IF NOT EXISTS idx_orders_rider ON orders(rider_tracking_id);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
CREATE INDEX IF NOT EXISTS idx_orders_payment_status ON orders(payment_status);
CREATE INDEX IF NOT EXISTS idx_orders_device_session_nonce ON orders(device_session_nonce);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'orders_idempotency_unique') THEN
        ALTER TABLE orders ADD CONSTRAINT orders_idempotency_unique UNIQUE (customer_tracking_id, device_session_nonce);
    END IF;
END $$;

-- ── Order Items ───────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS order_items (
    id                  BIGSERIAL PRIMARY KEY,
    order_tracking_id   VARCHAR(50) NOT NULL,
    product_tracking_id VARCHAR(50) NOT NULL,
    quantity            INTEGER NOT NULL CHECK (quantity > 0),
    price_at_checkout   NUMERIC(12,2) NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_order_items_order ON order_items(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_order_items_product ON order_items(product_tracking_id);

-- ── Order Events (audit timeline) ─────────────────────────────────────────
CREATE TABLE IF NOT EXISTS order_events (
    id                BIGSERIAL PRIMARY KEY,
    order_tracking_id VARCHAR(50) NOT NULL,
    event_type        VARCHAR(50) NOT NULL,
    payload           JSONB,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_order_events_order ON order_events(order_tracking_id);

-- ── Deliveries ──────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS deliveries (
    id                      BIGSERIAL PRIMARY KEY,
    tracking_id             VARCHAR(50) UNIQUE NOT NULL,
    order_tracking_id       VARCHAR(50),
    vendor_store_tracking_id VARCHAR(50),
    customer_tracking_id    VARCHAR(50),
    rider_tracking_id       VARCHAR(50),
    status                  VARCHAR(30) NOT NULL DEFAULT 'broadcasting',
    admin_commission        NUMERIC(10,2) DEFAULT 0,
    rider_earning           NUMERIC(10,2) DEFAULT 0,
    tips                    NUMERIC(10,2) DEFAULT 0.0,
    petrol_allowance        NUMERIC(10,2) DEFAULT 0.0,
    pickup_lat              DOUBLE PRECISION,
    pickup_lng              DOUBLE PRECISION,
    dropoff_lat             DOUBLE PRECISION,
    dropoff_lng             DOUBLE PRECISION,
    otp_code                VARCHAR(10),
    proof_of_delivery_url   TEXT,
    proof_of_delivery_type  VARCHAR(20),
    pickup_photo_url        TEXT,
    delivery_photo_url      TEXT,
    customer_dispute_photo_url TEXT,
    dispute_status          VARCHAR(30) DEFAULT 'none',
    cancel_reason           TEXT,
    is_cod                  BOOLEAN DEFAULT false,
    order_total             NUMERIC(10,2) DEFAULT 0.0,
    customer_phone          VARCHAR(20) DEFAULT '',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_deliveries_tracking_id ON deliveries(tracking_id);
CREATE INDEX IF NOT EXISTS idx_deliveries_order ON deliveries(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_deliveries_rider ON deliveries(rider_tracking_id);
CREATE INDEX IF NOT EXISTS idx_deliveries_status ON deliveries(status);
CREATE INDEX IF NOT EXISTS idx_deliveries_dispute_status ON deliveries(dispute_status);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_deliveries_status') THEN
        ALTER TABLE deliveries ADD CONSTRAINT chk_deliveries_status
            CHECK (status IN ('broadcasting','accepted','picked_up','in_transit','completed','failed'));
    END IF;
END $$;

-- ── Rides ───────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS rides (
    id                    BIGSERIAL PRIMARY KEY,
    tracking_id           VARCHAR(50) UNIQUE NOT NULL,
    customer_tracking_id  VARCHAR(50) NOT NULL,
    rider_tracking_id     VARCHAR(50),
    status                VARCHAR(30) NOT NULL DEFAULT 'requested',
    admin_commission      NUMERIC(10,2) DEFAULT 0,
    fare_amount           NUMERIC(10,2) DEFAULT 0,
    vehicle_type          VARCHAR(30) NOT NULL DEFAULT 'bike',
    pickup_lat            DOUBLE PRECISION,
    pickup_lng            DOUBLE PRECISION,
    dropoff_lat           DOUBLE PRECISION,
    dropoff_lng           DOUBLE PRECISION,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_rides_tracking_id ON rides(tracking_id);
CREATE INDEX IF NOT EXISTS idx_rides_customer ON rides(customer_tracking_id);
CREATE INDEX IF NOT EXISTS idx_rides_rider ON rides(rider_tracking_id);
CREATE INDEX IF NOT EXISTS idx_rides_status ON rides(status);

-- ── Ride Bids ───────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS ride_bids (
    id                  BIGSERIAL PRIMARY KEY,
    ride_tracking_id    VARCHAR(100) NOT NULL,
    rider_tracking_id   VARCHAR(100) NOT NULL,
    bid_amount          NUMERIC(10,2) NOT NULL,
    status              VARCHAR(20) DEFAULT 'pending',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ride_bids_ride_track_id ON ride_bids(ride_tracking_id);
CREATE INDEX IF NOT EXISTS idx_ride_bids_rider ON ride_bids(rider_tracking_id);

-- ── Rider Location History (telemetry) ────────────────────────────────────
CREATE TABLE IF NOT EXISTS rider_location_history (
    id            BIGSERIAL PRIMARY KEY,
    rider_tracking_id VARCHAR(50) NOT NULL,
    latitude      DOUBLE PRECISION NOT NULL,
    longitude     DOUBLE PRECISION NOT NULL,
    speed         REAL,
    bearing       REAL,
    battery_pct   SMALLINT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_rider_loc_rider ON rider_location_history(rider_tracking_id);
CREATE INDEX IF NOT EXISTS idx_rider_loc_time ON rider_location_history(created_at);

-- ── Rider Wallet ──────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS rider_wallet (
    id              BIGSERIAL PRIMARY KEY,
    rider_tracking_id VARCHAR(50) UNIQUE NOT NULL,
    balance         NUMERIC(12,2) NOT NULL DEFAULT 0,
    cash_in_hand    NUMERIC(10,2) DEFAULT 0.0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_rider_wallet_rider ON rider_wallet(rider_tracking_id);

-- ── Vendor Wallet ─────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS vendor_wallet (
    id               BIGSERIAL PRIMARY KEY,
    vendor_tracking_id VARCHAR(50) UNIQUE NOT NULL,
    balance          NUMERIC(12,2) NOT NULL DEFAULT 0,
    pending_payout   NUMERIC(12,2) DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_vendor_wallet_vendor ON vendor_wallet(vendor_tracking_id);

-- ── Customer Wallet (Daraz-style stored value) ────────────────────────────
CREATE TABLE IF NOT EXISTS customer_wallet (
    customer_tracking_id VARCHAR(255) PRIMARY KEY,
    balance              NUMERIC(15,2) NOT NULL DEFAULT 0.00,
    lifetime_spent       NUMERIC(15,2) NOT NULL DEFAULT 0.00,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── Vendor Payouts ────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS vendor_payouts (
    id               BIGSERIAL PRIMARY KEY,
    vendor_tracking_id VARCHAR(50) NOT NULL,
    amount           NUMERIC(12,2) NOT NULL,
    status           VARCHAR(30) DEFAULT 'pending',
    reference        VARCHAR(100),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_vendor_payouts_vendor ON vendor_payouts(vendor_tracking_id);

-- ── Ledger (double-entry) ─────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS ledger_entries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id  UUID NOT NULL,
    account         VARCHAR(50) NOT NULL,
    amount          NUMERIC(15,2) NOT NULL,
    currency        VARCHAR(10) NOT NULL DEFAULT 'PKR',
    reference_type  VARCHAR(50),
    reference_id    VARCHAR(100),
    description     TEXT,
    idempotency_key VARCHAR(100) UNIQUE,
    signature       VARCHAR(64) DEFAULT '',
    signature_version INT DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ledger_transaction_id ON ledger_entries(transaction_id);
CREATE INDEX IF NOT EXISTS idx_ledger_account ON ledger_entries(account);
CREATE INDEX IF NOT EXISTS idx_ledger_reference ON ledger_entries(reference_type, reference_id);

-- ── Escrow Holds ──────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS escrow_holds (
    id              BIGSERIAL PRIMARY KEY,
    order_tracking_id VARCHAR(50) NOT NULL,
    vendor_tracking_id VARCHAR(50) NOT NULL,
    amount          NUMERIC(12,2) NOT NULL,
    status          VARCHAR(30) DEFAULT 'held',
    hold_until      TIMESTAMPTZ DEFAULT NOW() + INTERVAL '7 days',
    released_at     TIMESTAMPTZ,
    dispute_id      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_escrow_order ON escrow_holds(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_escrow_vendor ON escrow_holds(vendor_tracking_id);
CREATE INDEX IF NOT EXISTS idx_escrow_hold_until ON escrow_holds(hold_until);

-- ── COD Debts (rider cash reconciliation) ─────────────────────────────────
CREATE TABLE IF NOT EXISTS cod_debts (
    id              BIGSERIAL PRIMARY KEY,
    rider_tracking_id VARCHAR(50) NOT NULL,
    order_tracking_id VARCHAR(50) NOT NULL,
    amount          NUMERIC(10,2) NOT NULL,
    status          VARCHAR(30) DEFAULT 'pending',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    settled_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_cod_debts_rider ON cod_debts(rider_tracking_id);

-- ── Disputes ───────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS disputes (
    id            BIGSERIAL PRIMARY KEY,
    tracking_id   VARCHAR(50) UNIQUE NOT NULL,
    order_tracking_id VARCHAR(50),
    filed_by      VARCHAR(50) NOT NULL,
    reason        TEXT NOT NULL,
    status        VARCHAR(30) DEFAULT 'open',
    resolution    TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_disputes_tracking_id ON disputes(tracking_id);
CREATE INDEX IF NOT EXISTS idx_disputes_status ON disputes(status);

-- ── Reviews ─────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS reviews (
    id                 BIGSERIAL PRIMARY KEY,
    product_tracking_id VARCHAR(50) NOT NULL,
    user_tracking_id   VARCHAR(50) NOT NULL,
    rating             SMALLINT NOT NULL CHECK (rating >= 1 AND rating <= 5),
    comment            TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_reviews_product ON reviews(product_tracking_id);

-- ── Favorites (wishlist) ────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS favorites (
    id                 BIGSERIAL PRIMARY KEY,
    user_tracking_id   VARCHAR(50) NOT NULL,
    product_tracking_id VARCHAR(50) NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_tracking_id, product_tracking_id)
);
CREATE INDEX IF NOT EXISTS idx_favorites_user ON favorites(user_tracking_id);

-- ── Outbox Events (reliable event emission) ───────────────────────────────
CREATE TABLE IF NOT EXISTS outbox_events (
    id              BIGSERIAL PRIMARY KEY,
    aggregate_id    VARCHAR(50) NOT NULL,
    topic           VARCHAR(100) NOT NULL,
    payload         JSONB NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'pending',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_outbox_events_status ON outbox_events(status);
CREATE INDEX IF NOT EXISTS idx_outbox_events_created_at ON outbox_events(created_at);

-- ── Payment API Keys (encrypted at rest) ──────────────────────────────────
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
    UNIQUE(provider, key_name)
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

-- ── Shopping Cart ─────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS carts (
    id              BIGSERIAL PRIMARY KEY,
    user_id         VARCHAR(50) UNIQUE NOT NULL,
    store_id        VARCHAR(50),
    total_amount    NUMERIC(12,2) DEFAULT 0.0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_carts_user ON carts(user_id);
CREATE INDEX IF NOT EXISTS idx_carts_store ON carts(store_id);

CREATE TABLE IF NOT EXISTS cart_items (
    id              BIGSERIAL PRIMARY KEY,
    cart_id         BIGINT NOT NULL,
    product_id      BIGINT NOT NULL,
    quantity        INTEGER NOT NULL CHECK (quantity > 0),
    price           NUMERIC(12,2) NOT NULL DEFAULT 0.0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(cart_id, product_id)
);
CREATE INDEX IF NOT EXISTS idx_cart_items_cart ON cart_items(cart_id);
CREATE INDEX IF NOT EXISTS idx_cart_items_product ON cart_items(product_id);

-- ── Chat Messages ─────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS chat_messages (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id    VARCHAR(255) NOT NULL,
    sender_id   VARCHAR(255) NOT NULL,
    receiver_id VARCHAR(255) NOT NULL,
    content     TEXT NOT NULL,
    is_read     BOOLEAN DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_chat_messages_order_id ON chat_messages(order_id);
CREATE INDEX IF NOT EXISTS idx_chat_messages_sender_id ON chat_messages(sender_id);
CREATE INDEX IF NOT EXISTS idx_chat_messages_receiver_id ON chat_messages(receiver_id);

-- ── Payment Transactions ──────────────────────────────────────────────────
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
WHERE status IN ('pending', 'processing', '3ds_required', 'settlement_pending', 'gateway_pending');

-- ── Auth Flow Tables ──────────────────────────────────────────────────────
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
