-- Migration: Add product_tracking_id to cart_items
-- The Go code uses product_tracking_id (VARCHAR) but the database has product_id (BIGINT).
-- This migration adds the missing column and a unique constraint for upsert operations.

-- Add the product_tracking_id column
ALTER TABLE cart_items ADD COLUMN IF NOT EXISTS product_tracking_id VARCHAR(100);

-- Create unique constraint for (cart_id, product_tracking_id) if it doesn't exist
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'cart_items_cart_product_tracking_unique'
    ) THEN
        ALTER TABLE cart_items
        ADD CONSTRAINT cart_items_cart_product_tracking_unique
        UNIQUE (cart_id, product_tracking_id);
    END IF;
END $$;

-- Create index for product_tracking_id lookups
CREATE INDEX IF NOT EXISTS idx_cart_items_product_tracking ON cart_items(product_tracking_id);

-- Populate product_tracking_id from products table for existing rows
UPDATE cart_items ci
SET product_tracking_id = p.product_tracking_id
FROM products p
WHERE ci.product_id = p.id
AND ci.product_tracking_id IS NULL;
