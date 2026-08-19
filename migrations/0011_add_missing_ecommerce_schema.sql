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
