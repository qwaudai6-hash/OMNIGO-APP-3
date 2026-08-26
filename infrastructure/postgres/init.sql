-- ============================================================
-- OMNIGO Super App — Authoritative Schema (Go-Aligned)
-- ------------------------------------------------------------
-- Source of truth: Go repository code. All tables use BIGSERIAL
-- PKs to match Go int64 model fields. Cross-table references use
-- VARCHAR tracking IDs (UTID) — not UUID FKs — so the polyglot
-- microservices can resolve entities without join overhead.
-- ============================================================

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ────────────────────────────────────────────────────────────
-- Unified Users Table
-- ────────────────────────────────────────────────────────────
CREATE TABLE users (
    id              BIGSERIAL PRIMARY KEY,
    tracking_id     VARCHAR(50) UNIQUE NOT NULL,        -- e.g. CUST-987654, VEND-123456, RIDR-555555
    email           VARCHAR(255) UNIQUE NOT NULL,
    phone           VARCHAR(20),
    full_name       VARCHAR(255) NOT NULL,
    password_hash   TEXT NOT NULL,
    role            VARCHAR(20) NOT NULL,              -- 'customer', 'vendor', 'rider', 'admin'
    region          VARCHAR(3)  NOT NULL DEFAULT 'PK', -- 'PK' or 'INT'
    -- Rider verification fields
    cnic_url        TEXT,
    license_url     TEXT,
    vehicle_type    VARCHAR(50),
    vehicle_plate_number VARCHAR(50),
    vehicle_registration_url TEXT,
    -- Vendor business metadata
    business_name   VARCHAR(255),
    address         TEXT,
    entity_type     VARCHAR(20),                       -- 'company' | 'individual'
    ntn_number      VARCHAR(50),
    cnic_back_url   TEXT,
    latitude        DECIMAL(10,8),
    longitude       DECIMAL(11,8),
    -- Lifecycle flags
    background_check_url TEXT,
    email_verified   BOOLEAN NOT NULL DEFAULT false,
    two_factor_enabled BOOLEAN NOT NULL DEFAULT false,
    is_verified      BOOLEAN NOT NULL DEFAULT FALSE,    -- required for riders & vendors
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    -- KYC/KYB verification automation fields
    verification_status VARCHAR(20) NOT NULL DEFAULT 'unverified', -- unverified | pending | approved | rejected
    verification_reason TEXT,
    risk_score      INTEGER NOT NULL DEFAULT 0,
    submitted_at    TIMESTAMPTZ,
    verified_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_role        ON users(role);
CREATE INDEX idx_users_tracking_id ON users(tracking_id);

-- ────────────────────────────────────────────────────────────
-- Vendor Stores
--   Go repository queries table name `stores` (NOT vendor_stores)
--   with columns: vendor_tracking_id, store_tracking_id,
--   store_name, latitude, longitude
-- ────────────────────────────────────────────────────────────
CREATE TABLE stores (
    id                BIGSERIAL PRIMARY KEY,
    vendor_tracking_id VARCHAR(50) NOT NULL,           -- FK -> users.tracking_id
    store_tracking_id  VARCHAR(50) UNIQUE NOT NULL,    -- e.g. STOR-112233
    store_name        VARCHAR(255) NOT NULL,
    store_description TEXT,
    logo_url          TEXT,
    latitude          DECIMAL(10,8),
    longitude         DECIMAL(11,8),
    commission_rate   DECIMAL(5,2)  NOT NULL DEFAULT 2.00,
    banner_url        TEXT,
    is_active         BOOLEAN NOT NULL DEFAULT true,
    
    is_verified       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_stores_vendor_tracking_id ON stores(vendor_tracking_id);
CREATE INDEX idx_stores_store_tracking_id   ON stores(store_tracking_id);

-- ────────────────────────────────────────────────────────────
-- Products
--   Go repository inserts: product_tracking_id, vendor_tracking_id,
--   store_tracking_id, sku, name, description, base_price, stock,
--   is_featured, image_url, category
-- ────────────────────────────────────────────────────────────
CREATE TABLE products (
    id                  BIGSERIAL PRIMARY KEY,
    product_tracking_id VARCHAR(50)  UNIQUE NOT NULL, -- e.g. PROD-998877
    vendor_tracking_id  VARCHAR(50)  NOT NULL,         -- FK -> users.tracking_id
    store_tracking_id   VARCHAR(50)  NOT NULL,         -- FK -> stores.store_tracking_id
    sku                VARCHAR(50),
    name               VARCHAR(500) NOT NULL,
    description        TEXT,
    base_price         DECIMAL(12,2) NOT NULL,         -- Go reads as float64
    stock              INTEGER      NOT NULL DEFAULT 0,
    is_featured        BOOLEAN      NOT NULL DEFAULT FALSE,
    image_url          TEXT,
    category           TEXT,
    is_active          BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_products_product_tracking_id ON products(product_tracking_id);
CREATE INDEX idx_products_vendor_tracking_id  ON products(vendor_tracking_id);
CREATE INDEX idx_products_store_tracking_id   ON products(store_tracking_id);
CREATE INDEX idx_products_category             ON products(category, product_tracking_id);

-- ────────────────────────────────────────────────────────────
-- Orders
--   Go repository inserts: order_tracking_id, customer_tracking_id,
--   store_tracking_id, vendor_tracking_id, product_tracking_ids,
--   status, total_amount, currency
--   Nullable columns scanned via *string: rider_tracking_id,
--   delivery_type, payment_gateway, otp_code
-- ────────────────────────────────────────────────────────────
CREATE TABLE orders (
    id                  BIGSERIAL PRIMARY KEY,
    order_tracking_id   VARCHAR(50) UNIQUE NOT NULL,  -- e.g. ORDR-ABCDEF
    customer_tracking_id VARCHAR(50) NOT NULL,        -- FK -> users.tracking_id
    store_tracking_id   VARCHAR(50) NOT NULL,         -- FK -> stores.store_tracking_id
    vendor_tracking_id  VARCHAR(50),                  -- resolved from stores at insert time
    rider_tracking_id   VARCHAR(50),                  -- nullable until gig accepted
    status              VARCHAR(30) NOT NULL DEFAULT 'pending',
    delivery_type       VARCHAR(30),
    payment_gateway     VARCHAR(30),
    total_amount        DECIMAL(14,2) NOT NULL,
    currency            VARCHAR(3)   NOT NULL,
    otp_code            VARCHAR(10),
    customer_lat        DECIMAL(10,8),
    customer_lng        DECIMAL(11,8),
    admin_commission    DECIMAL(14,2) NOT NULL DEFAULT 0,
    vendor_escrow    NUMERIC(12,2) DEFAULT 0.00,
    delivery_escrow  NUMERIC(12,2) DEFAULT 0.00,
    payment_status   VARCHAR(30) DEFAULT 'pending',
    delivered_at     TIMESTAMPTZ,
    escrow_released  BOOLEAN DEFAULT FALSE,
    dispute_status   VARCHAR(20) DEFAULT 'NONE',
    handover_photo_url TEXT,
    handover_at      TIMESTAMPTZ,
    handover_notes   TEXT,
    handed_over_by_tracking_id VARCHAR(50),
    
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    device_session_nonce VARCHAR(64),                 -- idempotency nonce from checkout
    CONSTRAINT orders_idempotency_unique UNIQUE (customer_tracking_id, device_session_nonce)
);

CREATE INDEX idx_orders_order_tracking_id    ON orders(order_tracking_id);
CREATE INDEX idx_orders_customer_tracking_id ON orders(customer_tracking_id);
CREATE INDEX idx_orders_vendor_tracking_id   ON orders(vendor_tracking_id);
CREATE INDEX idx_orders_status               ON orders(status);
CREATE INDEX idx_orders_device_session_nonce ON orders(device_session_nonce);

-- ────────────────────────────────────────────────────────────
-- Order Items
-- ────────────────────────────────────────────────────────────
CREATE TABLE order_items (
    id                  BIGSERIAL PRIMARY KEY,
    order_tracking_id   VARCHAR(50) NOT NULL,
    product_tracking_id VARCHAR(50) NOT NULL,
    quantity            INTEGER NOT NULL CHECK (quantity > 0),
    price_at_checkout   DECIMAL(12,2) NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_order_items_order ON order_items(order_tracking_id);
CREATE INDEX idx_order_items_product ON order_items(product_tracking_id);

-- ────────────────────────────────────────────────────────────
-- Deliveries (Gigs)
--   Go CreateGig inserts: tracking_id, order_tracking_id, status,
--   admin_commission. Geo columns are nullable for forward compat
--   with delivery_service enrichment.
CREATE TABLE deliveries (
    id               BIGSERIAL PRIMARY KEY,
    tracking_id      VARCHAR(50) UNIQUE NOT NULL,    -- e.g. DELV-123123 / GIG-xxxx
    order_tracking_id VARCHAR(50) NOT NULL,          -- FK -> orders.order_tracking_id
    vendor_store_tracking_id VARCHAR(50),
    customer_tracking_id VARCHAR(50),
    rider_tracking_id VARCHAR(50),                  -- nullable until rider accepts
    status           VARCHAR(30) NOT NULL DEFAULT 'broadcasting',
    pickup_lat       DECIMAL(10,8),
    pickup_lng       DECIMAL(11,8),
    dropoff_lat      DECIMAL(10,8),
    dropoff_lng      DECIMAL(11,8),
    delivery_fee     DECIMAL(10,2) NOT NULL DEFAULT 0,
    admin_commission DECIMAL(10,2) NOT NULL DEFAULT 0,
    rider_earning    DECIMAL(10,2) NOT NULL DEFAULT 0,
    tips             DECIMAL(10,2) DEFAULT 0.0,
    petrol_allowance DECIMAL(10,2) DEFAULT 0.0,
    otp_code         VARCHAR(10),
    proof_of_delivery_url TEXT,
    proof_of_delivery_type VARCHAR(20),
    pickup_photo_url TEXT,
    delivery_photo_url TEXT,
    customer_dispute_photo_url TEXT,
    dispute_status   VARCHAR(30) DEFAULT 'none',
    cancel_reason    TEXT,
    is_cod           BOOLEAN DEFAULT false,
    order_total      DECIMAL(10,2) DEFAULT 0.0,
    customer_phone   VARCHAR(20) DEFAULT '',
    currency         VARCHAR(3)   NOT NULL DEFAULT 'PKR',
    current_h3_hexagon VARCHAR(50),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_deliveries_tracking_id      ON deliveries(tracking_id);
CREATE INDEX idx_deliveries_order_tracking_id ON deliveries(order_tracking_id);
CREATE INDEX idx_deliveries_rider_tracking_id ON deliveries(rider_tracking_id);
CREATE INDEX idx_deliveries_customer_tracking ON deliveries(customer_tracking_id);
CREATE INDEX idx_deliveries_status           ON deliveries(status);
CREATE INDEX idx_deliveries_dispute_status   ON deliveries(dispute_status);

-- ────────────────────────────────────────────────────────────
-- Outbox Events
-- ────────────────────────────────────────────────────────────
CREATE TABLE outbox_events (
    id            BIGSERIAL PRIMARY KEY,
    aggregate_id  VARCHAR(50) NOT NULL,
    topic         VARCHAR(100) NOT NULL,
    payload       JSONB NOT NULL,
    status        VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    retry_count   INT NOT NULL DEFAULT 0,
    error_message TEXT,
    processed_at  TIMESTAMPTZ
);

CREATE INDEX idx_outbox_events_status ON outbox_events(status);

-- ────────────────────────────────────────────────────────────
-- Pick & Drop Rides
--   Go CreateRide inserts: tracking_id, customer_tracking_id,
--   status, admin_commission, fare_amount. Geo columns nullable
--   for forward compat.
-- ────────────────────────────────────────────────────────────
CREATE TABLE rides (
    id               BIGSERIAL PRIMARY KEY,
    tracking_id      VARCHAR(50) UNIQUE NOT NULL,    -- e.g. RIDE-456456
    customer_tracking_id VARCHAR(50) NOT NULL,       -- FK -> users.tracking_id
    rider_tracking_id VARCHAR(50),                  -- nullable until rider accepts
    status           VARCHAR(30) NOT NULL DEFAULT 'requested',
    pickup_lat       DECIMAL(10,8),
    pickup_lng       DECIMAL(11,8),
    dropoff_lat      DECIMAL(10,8),
    dropoff_lng      DECIMAL(11,8),
    fare_amount      DECIMAL(10,2) NOT NULL,
    admin_commission DECIMAL(10,2) NOT NULL DEFAULT 0,
    currency         VARCHAR(3)   NOT NULL DEFAULT 'PKR',
    vehicle_type     VARCHAR(30) NOT NULL DEFAULT 'bike',
    actual_distance_meters DOUBLE PRECISION,
    actual_duration_seconds DOUBLE PRECISION,
    
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_rides_tracking_id           ON rides(tracking_id);
CREATE INDEX idx_rides_customer_tracking_id  ON rides(customer_tracking_id);
CREATE INDEX idx_rides_rider_tracking_id     ON rides(rider_tracking_id);
CREATE INDEX idx_rides_status                ON rides(status);

-- ────────────────────────────────────────────────────────────
-- Customer Favorites (Wishlist)
-- ────────────────────────────────────────────────────────────
CREATE TABLE favorites (
    id                   BIGSERIAL PRIMARY KEY,
    user_tracking_id     VARCHAR(50) NOT NULL,
    product_tracking_id  VARCHAR(50) NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_tracking_id, product_tracking_id)
);

CREATE INDEX idx_favorites_user     ON favorites(user_tracking_id);
CREATE INDEX idx_favorites_product  ON favorites(product_tracking_id);

-- ────────────────────────────────────────────────────────────
-- Product Reviews / Ratings
-- ────────────────────────────────────────────────────────────
CREATE TABLE reviews (
    id                  BIGSERIAL PRIMARY KEY,
    product_tracking_id VARCHAR(50) NOT NULL,
    user_tracking_id VARCHAR(50) NOT NULL,
    rating              INTEGER NOT NULL CHECK (rating >= 1 AND rating <= 5),
    comment             TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(product_tracking_id, user_tracking_id)
);

CREATE INDEX idx_reviews_product ON reviews(product_tracking_id);

-- ────────────────────────────────────────────────────────────
-- updated_at trigger (auto-maintain on row update)
-- ────────────────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION omnigo_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_users_updated_at      BEFORE UPDATE ON users      FOR EACH ROW EXECUTE FUNCTION omnigo_set_updated_at();
CREATE TRIGGER trg_stores_updated_at     BEFORE UPDATE ON stores     FOR EACH ROW EXECUTE FUNCTION omnigo_set_updated_at();
CREATE TRIGGER trg_products_updated_at   BEFORE UPDATE ON products   FOR EACH ROW EXECUTE FUNCTION omnigo_set_updated_at();
CREATE TRIGGER trg_orders_updated_at      BEFORE UPDATE ON orders      FOR EACH ROW EXECUTE FUNCTION omnigo_set_updated_at();
CREATE TRIGGER trg_deliveries_updated_at BEFORE UPDATE ON deliveries FOR EACH ROW EXECUTE FUNCTION omnigo_set_updated_at();
CREATE TRIGGER trg_rides_updated_at      BEFORE UPDATE ON rides      FOR EACH ROW EXECUTE FUNCTION omnigo_set_updated_at();
CREATE TRIGGER trg_reviews_updated_at    BEFORE UPDATE ON reviews    FOR EACH ROW EXECUTE FUNCTION omnigo_set_updated_at();
CREATE TRIGGER trg_refresh_tokens_updated_at BEFORE UPDATE ON user_refresh_tokens FOR EACH ROW EXECUTE FUNCTION omnigo_set_updated_at();
CREATE TRIGGER trg_rider_wallet_updated_at BEFORE UPDATE ON rider_wallet FOR EACH ROW EXECUTE FUNCTION omnigo_set_updated_at();

-- ────────────────────────────────────────────────────────────
-- Rider Location History (PostGIS)
--   Historical geospatial trace tracking. Live state lives in Redis;
--   this table is used for route analytics, geofencing, and audit.
-- ────────────────────────────────────────────────────────────
CREATE EXTENSION IF NOT EXISTS "postgis";

CREATE TABLE rider_location_history (
    id            BIGSERIAL PRIMARY KEY,
    rider_tracking_id VARCHAR(50) NOT NULL,
    latitude      DOUBLE PRECISION NOT NULL,
    longitude     DOUBLE PRECISION NOT NULL,
    speed         REAL,
    bearing       REAL,
    battery_pct   SMALLINT,
    location      GEOGRAPHY(Point, 4326),
    status        VARCHAR(20) NOT NULL DEFAULT 'online',
    vector_clock  BIGINT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_rider_location_history_rider     ON rider_location_history(rider_tracking_id);
CREATE INDEX idx_rider_location_history_created   ON rider_location_history(created_at);
CREATE INDEX idx_rider_location_history_geo       ON rider_location_history USING GIST(location);

-- ────────────────────────────────────────────────────────────
-- Refresh Tokens (Security RTR Engine)
-- ────────────────────────────────────────────────────────────
CREATE TABLE user_refresh_tokens (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_tracking_id VARCHAR(50) NOT NULL,
    token_hash       VARCHAR(255) NOT NULL UNIQUE,
    expires_at       TIMESTAMPTZ NOT NULL,
    revoked          BOOLEAN NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_refresh_tokens_user_tracking_id ON user_refresh_tokens(user_tracking_id);
CREATE INDEX idx_refresh_tokens_token_hash       ON user_refresh_tokens(token_hash);

-- ────────────────────────────────────────────────────────────
-- Rider Wallet (earnings / payable balance)
--   Tracks rider delivery earnings, admin commission deductions, and
--   lifetime earnings. All writes happen inside a DB transaction so
--   balance can never go negative.
-- ────────────────────────────────────────────────────────────
CREATE TABLE rider_wallet (
    id                  BIGSERIAL PRIMARY KEY,
    rider_tracking_id   VARCHAR(50) UNIQUE NOT NULL,    -- FK -> users.tracking_id
    balance             DECIMAL(14,2) NOT NULL DEFAULT 0 CHECK (balance >= 0),
    lifetime_earnings   DECIMAL(14,2) NOT NULL DEFAULT 0 CHECK (lifetime_earnings >= 0),
    cash_in_hand        DECIMAL(14,2) NOT NULL DEFAULT 0.00,
    is_cash_blocked     BOOLEAN NOT NULL DEFAULT FALSE,
    
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_rider_wallet_rider ON rider_wallet(rider_tracking_id);

-- ────────────────────────────────────────────────────────────
-- FCM Device Token Registry
--   Maps user_tracking_id -> Firebase Cloud Messaging device token.
--   A user can own multiple devices (phone + tablet); each row is one token.
--   The Node.js notification worker queries this table to resolve recipients.
-- ────────────────────────────────────────────────────────────
CREATE TABLE device_tokens (
    id               SERIAL PRIMARY KEY,
    user_tracking_id VARCHAR(50) NOT NULL,                 -- FK -> users.tracking_id
    fcm_token        TEXT NOT NULL,
    platform         VARCHAR(20) DEFAULT 'android',          -- android | ios | web
    is_active        BOOLEAN NOT NULL DEFAULT true,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_tracking_id, fcm_token)
);

CREATE INDEX idx_device_tokens_user   ON device_tokens(user_tracking_id);
CREATE INDEX idx_device_tokens_active ON device_tokens(user_tracking_id) WHERE is_active = true;

-- ────────────────────────────────────────────────────────────
-- PAYMENT ARCHITECTURE TABLES
-- ────────────────────────────────────────────────────────────

-- Ledger Entries (Double-Entry Accounting)
CREATE TABLE IF NOT EXISTS ledger_entries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id  UUID NOT NULL,
    account         VARCHAR(50) NOT NULL,
    amount          DECIMAL(14,2) NOT NULL,
    currency        VARCHAR(3) NOT NULL DEFAULT 'PKR',
    reference_type  VARCHAR(30) NOT NULL,
    reference_id    VARCHAR(50),
    description     TEXT,
    idempotency_key VARCHAR(128) UNIQUE,
    signature       VARCHAR(64) DEFAULT '',
    signature_version INT DEFAULT 1,
    
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ledger_transaction ON ledger_entries(transaction_id);
CREATE INDEX IF NOT EXISTS idx_ledger_account ON ledger_entries(account, created_at);
CREATE INDEX IF NOT EXISTS idx_ledger_reference ON ledger_entries(reference_type, reference_id);
CREATE INDEX IF NOT EXISTS idx_ledger_idempotency ON ledger_entries(idempotency_key);

-- Escrow Holds (Vendor funds locked after delivery)
CREATE TABLE IF NOT EXISTS escrow_holds (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_tracking_id VARCHAR(50) NOT NULL,
    vendor_tracking_id VARCHAR(50) NOT NULL,
    amount            DECIMAL(14,2) NOT NULL,
    status            VARCHAR(20) NOT NULL DEFAULT 'held',
    hold_until        TIMESTAMPTZ NOT NULL,
    released_at       TIMESTAMPTZ,
    dispute_id        UUID,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_escrow_vendor ON escrow_holds(vendor_tracking_id, status);
CREATE INDEX IF NOT EXISTS idx_escrow_hold_until ON escrow_holds(hold_until, status);
CREATE INDEX IF NOT EXISTS idx_escrow_order ON escrow_holds(order_tracking_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_escrow_order_vendor ON escrow_holds(order_tracking_id, vendor_tracking_id);

-- COD Debts (Rider collects cash, owes platform)
CREATE TABLE IF NOT EXISTS cod_debts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_tracking_id   VARCHAR(50) NOT NULL,
    rider_tracking_id   VARCHAR(50) NOT NULL,
    amount_owed         DECIMAL(14,2) NOT NULL,
    status              VARCHAR(20) NOT NULL DEFAULT 'pending',
    settled_via         VARCHAR(30),
    settled_at          TIMESTAMPTZ,
    webhook_event_id    VARCHAR(128),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_cod_debts_rider ON cod_debts(rider_tracking_id, status);
CREATE INDEX IF NOT EXISTS idx_cod_debts_order ON cod_debts(order_tracking_id);

-- Vendor Payouts (Settlement records)
CREATE TABLE IF NOT EXISTS vendor_payouts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vendor_tracking_id  VARCHAR(50) NOT NULL,
    amount              DECIMAL(14,2) NOT NULL,
    method              VARCHAR(30),
    status              VARCHAR(20) NOT NULL DEFAULT 'pending',
    batch_id            UUID,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at        TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_vendor_payouts_vendor ON vendor_payouts(vendor_tracking_id, status);
CREATE INDEX IF NOT EXISTS idx_vendor_payouts_batch ON vendor_payouts(batch_id);

-- Disputes
CREATE TABLE IF NOT EXISTS disputes (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_tracking_id   VARCHAR(50) NOT NULL,
    filed_by            VARCHAR(50) NOT NULL,
    reason              TEXT NOT NULL,
    status              VARCHAR(20) NOT NULL DEFAULT 'open',
    resolution          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at         TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_disputes_order ON disputes(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_disputes_status ON disputes(status);

-- Vendor Wallet
CREATE TABLE IF NOT EXISTS vendor_wallet (
    id                  BIGSERIAL PRIMARY KEY,
    vendor_tracking_id  VARCHAR(50) UNIQUE NOT NULL,
    balance             DECIMAL(14,2) NOT NULL DEFAULT 0 CHECK (balance >= 0),
    lifetime_earnings   DECIMAL(14,2) NOT NULL DEFAULT 0 CHECK (lifetime_earnings >= 0),
    total_payouts       DECIMAL(14,2) NOT NULL DEFAULT 0,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_vendor_wallet_vendor ON vendor_wallet(vendor_tracking_id);
-- ────────────────────────────────────────────────────────────
-- Additional Tables
-- ────────────────────────────────────────────────────────────

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

CREATE TABLE IF NOT EXISTS customer_wallet (
    id BIGSERIAL PRIMARY KEY,
    customer_tracking_id VARCHAR(50) UNIQUE NOT NULL,
    balance DECIMAL(14,2) NOT NULL DEFAULT 0.00,
    lifetime_spent DECIMAL(14,2) NOT NULL DEFAULT 0.00,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

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
