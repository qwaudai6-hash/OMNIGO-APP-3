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
    phone           VARCHAR(20) UNIQUE,
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
    is_verified     BOOLEAN NOT NULL DEFAULT FALSE,    -- required for riders & vendors
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
-- ────────────────────────────────────────────────────────────
CREATE TABLE deliveries (
    id               BIGSERIAL PRIMARY KEY,
    tracking_id      VARCHAR(50) UNIQUE NOT NULL,    -- e.g. DELV-123123 / GIG-xxxx
    order_tracking_id VARCHAR(50) NOT NULL,          -- FK -> orders.order_tracking_id
    rider_tracking_id VARCHAR(50),                  -- nullable until rider accepts
    status           VARCHAR(30) NOT NULL DEFAULT 'broadcasting',
    pickup_lat       DECIMAL(10,8),
    pickup_lng       DECIMAL(11,8),
    dropoff_lat      DECIMAL(10,8),
    dropoff_lng      DECIMAL(11,8),
    delivery_fee     DECIMAL(10,2),
    admin_commission DECIMAL(10,2) NOT NULL DEFAULT 0,
    rider_earning    DECIMAL(10,2) NOT NULL DEFAULT 0,
    currency         VARCHAR(3)   NOT NULL DEFAULT 'PKR',
    current_h3_hexagon VARCHAR(50),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_deliveries_tracking_id      ON deliveries(tracking_id);
CREATE INDEX idx_deliveries_order_tracking_id ON deliveries(order_tracking_id);
CREATE INDEX idx_deliveries_rider_tracking_id ON deliveries(rider_tracking_id);
CREATE INDEX idx_deliveries_status           ON deliveries(status);

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
    customer_tracking_id VARCHAR(50) NOT NULL,
    product_tracking_id  VARCHAR(50) NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(customer_tracking_id, product_tracking_id)
);

CREATE INDEX idx_favorites_customer ON favorites(customer_tracking_id);
CREATE INDEX idx_favorites_product  ON favorites(product_tracking_id);

-- ────────────────────────────────────────────────────────────
-- Product Reviews / Ratings
-- ────────────────────────────────────────────────────────────
CREATE TABLE reviews (
    id                  BIGSERIAL PRIMARY KEY,
    product_tracking_id VARCHAR(50) NOT NULL,
    customer_tracking_id VARCHAR(50) NOT NULL,
    rating              INTEGER NOT NULL CHECK (rating >= 1 AND rating <= 5),
    comment             TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(product_tracking_id, customer_tracking_id)
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
    location      GEOGRAPHY(Point, 4326) NOT NULL,
    status        VARCHAR(20) NOT NULL DEFAULT 'online',
    vector_clock  BIGINT NOT NULL,
    recorded_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_rider_location_history_rider     ON rider_location_history(rider_tracking_id);
CREATE INDEX idx_rider_location_history_recorded  ON rider_location_history(recorded_at);
CREATE INDEX idx_rider_location_history_geo       ON rider_location_history USING GIST(location);

-- ────────────────────────────────────────────────────────────
-- Refresh Tokens (Security RTR Engine)
-- ────────────────────────────────────────────────────────────
CREATE TABLE user_refresh_tokens (
    id               UUID PRIMARY KEY,
    user_tracking_id VARCHAR(50) NOT NULL,
    token_hash       VARCHAR(255) NOT NULL UNIQUE,
    expires_at       TIMESTAMPTZ NOT NULL,
    revoked          BOOLEAN NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
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
    completed_at        TIMESTAMPTZ
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
    resolved_at         TIMESTAMPTZ
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