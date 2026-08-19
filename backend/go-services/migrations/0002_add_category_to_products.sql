-- Migration: Add category column and composite structural index on products table
-- Applied at: 2026-07-13T05:30:00Z

-- Add category column
ALTER TABLE products ADD COLUMN IF NOT EXISTS category text;

-- Create composite index for high-speed prefix category scans at 50M records scale
CREATE INDEX IF NOT EXISTS idx_products_category ON products(category, product_tracking_id);

-- Seed defaults for mock catalog products
UPDATE products SET category = 'Shoes' WHERE product_tracking_id = 'PROD-1001';
UPDATE products SET category = 'Apparel' WHERE product_tracking_id = 'PROD-1002';
