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

-- 12. update ledger_entries
ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS signature VARCHAR(64) DEFAULT '';
ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS signature_version INT DEFAULT 1;

-- 13. Add 10 missing tables

CREATE TABLE IF NOT EXISTS payment_transactions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    status VARCHAR(30) NOT NULL CHECK (status IN ('pending', 'processing', 'completed', 'failed', 'refunded')),
    kind VARCHAR(30) NOT NULL,
    idempotency_key VARCHAR(128) UNIQUE,
    metadata JSONB,
    callback_processed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS customer_wallet (
    id BIGSERIAL PRIMARY KEY,
    customer_tracking_id VARCHAR(50) UNIQUE NOT NULL,
    balance DECIMAL(14,2) NOT NULL DEFAULT 0.00,
    lifetime_spent DECIMAL(14,2) NOT NULL DEFAULT 0.00,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS carts (
    id BIGSERIAL PRIMARY KEY,
    customer_tracking_id VARCHAR(50) NOT NULL,
    store_tracking_id VARCHAR(50) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS cart_items (
    id BIGSERIAL PRIMARY KEY,
    cart_id BIGINT NOT NULL,
    product_tracking_id VARCHAR(50) NOT NULL,
    quantity INT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS ride_bids (
    id BIGSERIAL PRIMARY KEY,
    tracking_id VARCHAR(50) UNIQUE NOT NULL,
    ride_tracking_id VARCHAR(50) NOT NULL,
    rider_tracking_id VARCHAR(50) NOT NULL,
    bid_amount DECIMAL(10,2) NOT NULL,
    status VARCHAR(30) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS chat_messages (
    id BIGSERIAL PRIMARY KEY,
    order_id VARCHAR(50) NOT NULL,
    sender_id VARCHAR(50) NOT NULL,
    receiver_id VARCHAR(50) NOT NULL,
    content TEXT NOT NULL,
    is_read BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS customer_saved_cards (
    id BIGSERIAL PRIMARY KEY,
    user_id VARCHAR(50) NOT NULL,
    gateway VARCHAR(30) NOT NULL,
    card_token VARCHAR(255) NOT NULL,
    masked_pan VARCHAR(20),
    card_brand VARCHAR(30),
    card_holder_name VARCHAR(255),
    expiry_month INT,
    expiry_year INT,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS password_reset_tokens (
    id BIGSERIAL PRIMARY KEY,
    user_tracking_id VARCHAR(50) NOT NULL,
    token_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS email_verification_tokens (
    id BIGSERIAL PRIMARY KEY,
    user_tracking_id VARCHAR(50) NOT NULL,
    token TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS user_2fa_secrets (
    id BIGSERIAL PRIMARY KEY,
    user_tracking_id VARCHAR(50) NOT NULL,
    secret TEXT NOT NULL,
    is_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS payment_api_keys (
    id BIGSERIAL PRIMARY KEY,
    vendor_tracking_id VARCHAR(50) NOT NULL,
    api_key VARCHAR(100) UNIQUE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    is_active BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE TABLE IF NOT EXISTS payment_api_key_audit (
    id BIGSERIAL PRIMARY KEY,
    key_id BIGINT NOT NULL,
    action VARCHAR(50) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
