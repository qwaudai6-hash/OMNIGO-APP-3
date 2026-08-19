-- ════════════════════════════════════════════════════════════════
--  OMNIGO Platform — Master Consolidated Schema & Migrations
--  Fully Idempotent (Safe to run on empty DB or partially updated DB)
-- ════════════════════════════════════════════════════════════════

-- ── Extensions ──────────────────────────────────────────────────
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ── Users ───────────────────────────────────────────────────────
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
    is_verified     BOOLEAN NOT NULL DEFAULT false,
    verification_status VARCHAR(30) DEFAULT 'pending',
    verification_reason TEXT,
    risk_score      REAL DEFAULT 0.0,
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

-- ── Refresh Tokens ──────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS user_refresh_tokens (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_tracking_id    VARCHAR(50) NOT NULL,
    token_hash          TEXT UNIQUE NOT NULL,
    expires_at          TIMESTAMPTZ NOT NULL,
    revoked             BOOLEAN NOT NULL DEFAULT false,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_hash ON user_refresh_tokens(token_hash);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_tracking ON user_refresh_tokens(user_tracking_id);

-- ── Device Tokens (FCM) ─────────────────────────────────────────
CREATE TABLE IF NOT EXISTS device_tokens (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token       TEXT NOT NULL,
    platform    VARCHAR(10),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, token)
);

-- ── Stores (Vendor storefronts) ─────────────────────────────────
CREATE TABLE IF NOT EXISTS stores (
    id                  BIGSERIAL PRIMARY KEY,
    vendor_tracking_id  VARCHAR(50) NOT NULL,
    store_tracking_id   VARCHAR(50) UNIQUE NOT NULL,
    store_name          VARCHAR(255) NOT NULL,
    latitude            DOUBLE PRECISION,
    longitude           DOUBLE PRECISION,
    is_active           BOOLEAN DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_stores_vendor ON stores(vendor_tracking_id);
CREATE INDEX IF NOT EXISTS idx_stores_tracking_id ON stores(store_tracking_id);

-- ── Products ────────────────────────────────────────────────────
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

-- ── Orders ──────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS orders (
    id                    BIGSERIAL PRIMARY KEY,
    order_tracking_id     VARCHAR(50) UNIQUE NOT NULL,
    customer_tracking_id  VARCHAR(50) NOT NULL,
    store_tracking_id     VARCHAR(50),
    vendor_tracking_id    VARCHAR(50) NOT NULL,
    status                VARCHAR(30) NOT NULL DEFAULT 'pending',
    total_amount          NUMERIC(12,2) NOT NULL DEFAULT 0,
    currency              VARCHAR(10) NOT NULL DEFAULT 'PKR',
    payment_gateway       VARCHAR(30) DEFAULT 'cod',
    customer_lat          DOUBLE PRECISION,
    customer_lng          DOUBLE PRECISION,
    device_session_nonce  TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_orders_tracking_id ON orders(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_orders_customer ON orders(customer_tracking_id);
CREATE INDEX IF NOT EXISTS idx_orders_vendor ON orders(vendor_tracking_id);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
CREATE INDEX IF NOT EXISTS idx_orders_device_session_nonce ON orders(device_session_nonce);

DO $$ 
BEGIN 
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'orders_idempotency_unique') THEN
        ALTER TABLE orders ADD CONSTRAINT orders_idempotency_unique UNIQUE (customer_tracking_id, device_session_nonce);
    END IF;
END $$;

-- ── Order Items ─────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS order_items (
    id                  BIGSERIAL PRIMARY KEY,
    order_tracking_id   VARCHAR(50) NOT NULL,
    product_tracking_id VARCHAR(50) NOT NULL,
    quantity            INTEGER NOT NULL CHECK (quantity > 0),
    unit_price          NUMERIC(12,2) NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_order_items_order ON order_items(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_order_items_product ON order_items(product_tracking_id);

-- ── Order Events (audit timeline) ───────────────────────────────
CREATE TABLE IF NOT EXISTS order_events (
    id                BIGSERIAL PRIMARY KEY,
    order_tracking_id VARCHAR(50) NOT NULL,
    event_type        VARCHAR(50) NOT NULL,
    payload           JSONB,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_order_events_order ON order_events(order_tracking_id);

-- ── Deliveries ──────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS deliveries (
    id                      BIGSERIAL PRIMARY KEY,
    tracking_id             VARCHAR(50) UNIQUE NOT NULL,
    order_tracking_id       VARCHAR(50),
    vendor_store_tracking_id VARCHAR(50),
    customer_tracking_id    VARCHAR(50),
    rider_tracking_id       VARCHAR(50),
    status                  VARCHAR(30) NOT NULL DEFAULT 'assigned',
    admin_commission        NUMERIC(10,2) DEFAULT 0,
    rider_earning           NUMERIC(10,2) DEFAULT 0,
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

-- ── Rides ───────────────────────────────────────────────────────
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

-- ── Rider Location History (telemetry) ──────────────────────────
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

-- ── Rider Wallet ────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS rider_wallet (
    id              BIGSERIAL PRIMARY KEY,
    rider_tracking_id VARCHAR(50) UNIQUE NOT NULL,
    balance         NUMERIC(12,2) NOT NULL DEFAULT 0,
    cash_in_hand    NUMERIC(10,2) DEFAULT 0.0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_rider_wallet_rider ON rider_wallet(rider_tracking_id);

-- ── Vendor Wallet ───────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS vendor_wallet (
    id               BIGSERIAL PRIMARY KEY,
    vendor_tracking_id VARCHAR(50) UNIQUE NOT NULL,
    balance          NUMERIC(12,2) NOT NULL DEFAULT 0,
    pending_payout   NUMERIC(12,2) DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_vendor_wallet_vendor ON vendor_wallet(vendor_tracking_id);

-- ── Vendor Payouts ──────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS vendor_payouts (
    id               BIGSERIAL PRIMARY KEY,
    vendor_tracking_id VARCHAR(50) NOT NULL,
    amount           NUMERIC(12,2) NOT NULL,
    status           VARCHAR(30) DEFAULT 'pending',
    reference        VARCHAR(100),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_vendor_payouts_vendor ON vendor_payouts(vendor_tracking_id);

-- ── Ledger (double-entry) ───────────────────────────────────────
CREATE TABLE IF NOT EXISTS ledger_entries (
    id              BIGSERIAL PRIMARY KEY,
    transaction_id  VARCHAR(64) UNIQUE NOT NULL,
    account         VARCHAR(50) NOT NULL,
    amount          NUMERIC(14,2) NOT NULL,
    currency        VARCHAR(10) NOT NULL DEFAULT 'PKR',
    reference_type  VARCHAR(30),
    reference_id    VARCHAR(50),
    signature       TEXT,
    signature_version INT DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ledger_transaction_id ON ledger_entries(transaction_id);
CREATE INDEX IF NOT EXISTS idx_ledger_account ON ledger_entries(account);
CREATE INDEX IF NOT EXISTS idx_ledger_reference ON ledger_entries(reference_type, reference_id);

-- ── Escrow Holds ────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS escrow_holds (
    id              BIGSERIAL PRIMARY KEY,
    order_tracking_id VARCHAR(50) NOT NULL,
    vendor_tracking_id VARCHAR(50) NOT NULL,
    amount          NUMERIC(12,2) NOT NULL,
    status          VARCHAR(30) DEFAULT 'held',
    released_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_escrow_order ON escrow_holds(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_escrow_vendor ON escrow_holds(vendor_tracking_id);

-- ── COD Debts (rider cash reconciliation) ───────────────────────
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

-- ── Disputes ────────────────────────────────────────────────────
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

-- ── Reviews ─────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS reviews (
    id                 BIGSERIAL PRIMARY KEY,
    product_tracking_id VARCHAR(50) NOT NULL,
    user_tracking_id   VARCHAR(50) NOT NULL,
    rating             SMALLINT NOT NULL CHECK (rating >= 1 AND rating <= 5),
    comment            TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_reviews_product ON reviews(product_tracking_id);

-- ── Favorites (wishlist) ────────────────────────────────────────
CREATE TABLE IF NOT EXISTS favorites (
    id                 BIGSERIAL PRIMARY KEY,
    user_tracking_id   VARCHAR(50) NOT NULL,
    product_tracking_id VARCHAR(50) NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_tracking_id, product_tracking_id)
);
CREATE INDEX IF NOT EXISTS idx_favorites_user ON favorites(user_tracking_id);

-- ── Outbox Events (reliable event emission) ─────────────────────
CREATE TABLE IF NOT EXISTS outbox_events (
    id              BIGSERIAL PRIMARY KEY,
    aggregate_type  VARCHAR(50) NOT NULL DEFAULT 'order',
    aggregate_id    VARCHAR(50) NOT NULL,
    event_type      VARCHAR(50) NOT NULL DEFAULT 'created',
    topic           VARCHAR(100),
    payload         JSONB NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'pending',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_outbox_events_status ON outbox_events(status);

-- ── Payment API Keys (encrypted at rest) ────────────────────────
CREATE TABLE IF NOT EXISTS payment_api_keys (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider      VARCHAR(40) NOT NULL,
    key_name      VARCHAR(60) NOT NULL,
    key_value     TEXT,
    encrypted_value BYTEA,
    version       INT NOT NULL DEFAULT 1,
    is_active     BOOLEAN DEFAULT true,
    rotated_by    VARCHAR(80) DEFAULT 'admin',
    rotated_at    TIMESTAMPTZ DEFAULT NOW(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(provider, key_name)
);
CREATE INDEX IF NOT EXISTS idx_payment_api_keys_provider ON payment_api_keys(provider);

CREATE TABLE IF NOT EXISTS payment_api_key_audit (
    id            BIGSERIAL PRIMARY KEY,
    payment_key_id UUID,
    provider      VARCHAR(40) NOT NULL,
    key_name      VARCHAR(60) NOT NULL,
    action        VARCHAR(20) NOT NULL,
    prev_fingerprint VARCHAR(16),
    new_fingerprint  VARCHAR(16),
    actor         VARCHAR(50),
    actor_id      VARCHAR(80),
    actor_ip      VARCHAR(64),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_payment_api_key_audit_provider ON payment_api_key_audit(provider, created_at DESC);

-- ── Idempotent Alteration Additions ─────────────────────────────
ALTER TABLE products ADD COLUMN IF NOT EXISTS category TEXT;
ALTER TABLE rides ADD COLUMN IF NOT EXISTS vehicle_type VARCHAR(30) NOT NULL DEFAULT 'bike';
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS vendor_store_tracking_id VARCHAR(50);
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS cancel_reason TEXT;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS customer_tracking_id VARCHAR(50);
ALTER TABLE rider_location_history ADD COLUMN IF NOT EXISTS speed REAL;
ALTER TABLE rider_location_history ADD COLUMN IF NOT EXISTS bearing REAL;
ALTER TABLE rider_location_history ADD COLUMN IF NOT EXISTS battery_pct SMALLINT;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS is_cod BOOLEAN DEFAULT false;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS order_total NUMERIC(10,2) DEFAULT 0.0;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS customer_phone VARCHAR(20) DEFAULT '';
ALTER TABLE rider_wallet ADD COLUMN IF NOT EXISTS cash_in_hand NUMERIC(10,2) DEFAULT 0.0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS verification_status VARCHAR(20) NOT NULL DEFAULT 'pending';
ALTER TABLE users ADD COLUMN IF NOT EXISTS verification_reason TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS risk_score REAL DEFAULT 0.0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS submitted_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS verified_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS entity_type VARCHAR(20);
ALTER TABLE users ADD COLUMN IF NOT EXISTS ntn_number VARCHAR(50);
ALTER TABLE users ADD COLUMN IF NOT EXISTS cnic_back_url TEXT;
ALTER TABLE user_refresh_tokens ADD COLUMN IF NOT EXISTS user_tracking_id VARCHAR(50);
ALTER TABLE user_refresh_tokens ADD COLUMN IF NOT EXISTS revoked BOOLEAN DEFAULT false;
ALTER TABLE user_refresh_tokens ALTER COLUMN user_id DROP NOT NULL;

-- Vendor Missing Fields
ALTER TABLE stores ADD COLUMN IF NOT EXISTS logo_url TEXT;
ALTER TABLE stores ADD COLUMN IF NOT EXISTS banner_url TEXT;

-- Rider Missing Fields
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS tips NUMERIC(10,2) DEFAULT 0.0;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS petrol_allowance NUMERIC(10,2) DEFAULT 0.0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS background_check_url TEXT;

-- Shopping Cart Schema
CREATE TABLE IF NOT EXISTS carts (
    id BIGSERIAL PRIMARY KEY,
    customer_tracking_id VARCHAR(50) UNIQUE NOT NULL,
    total_amount NUMERIC(12,2) DEFAULT 0.0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS cart_items (
    id BIGSERIAL PRIMARY KEY,
    cart_id BIGINT NOT NULL REFERENCES carts(id) ON DELETE CASCADE,
    product_tracking_id VARCHAR(50) NOT NULL,
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    unit_price NUMERIC(12,2) NOT NULL DEFAULT 0.0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS chat_messages (
    id UUID PRIMARY KEY,
    order_id VARCHAR(255) NOT NULL,
    sender_id VARCHAR(255) NOT NULL,
    receiver_id VARCHAR(255) NOT NULL,
    content TEXT NOT NULL,
    is_read BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    deleted_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX idx_chat_messages_order_id ON chat_messages(order_id);
CREATE INDEX idx_chat_messages_sender_id ON chat_messages(sender_id);
CREATE INDEX idx_chat_messages_receiver_id ON chat_messages(receiver_id);
CREATE TABLE IF NOT EXISTS ride_bids (
    id SERIAL PRIMARY KEY,
    ride_tracking_id VARCHAR(100) NOT NULL REFERENCES rides(tracking_id),
    rider_tracking_id VARCHAR(100) NOT NULL,
    bid_amount NUMERIC(10, 2) NOT NULL,
    status VARCHAR(20) DEFAULT 'pending',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_ride_bids_ride_track_id ON ride_bids(ride_tracking_id);

-- Escrow Fields for Orders
ALTER TABLE orders ADD COLUMN IF NOT EXISTS escrow_released BOOLEAN DEFAULT FALSE;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS dispute_status VARCHAR(20) DEFAULT 'NONE';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMP;

CREATE INDEX IF NOT EXISTS idx_orders_escrow ON orders(status, escrow_released, dispute_status, delivered_at);
