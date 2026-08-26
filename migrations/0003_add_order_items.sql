-- Remove old array column from orders
ALTER TABLE orders DROP COLUMN IF EXISTS product_tracking_ids;

-- Create order_items table for frozen snapshots
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
