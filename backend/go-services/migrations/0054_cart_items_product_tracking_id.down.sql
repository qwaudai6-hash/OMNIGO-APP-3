-- Rollback: Remove product_tracking_id from cart_items
DROP INDEX IF EXISTS idx_cart_items_product_tracking;
ALTER TABLE cart_items DROP CONSTRAINT IF EXISTS cart_items_cart_product_tracking_unique;
ALTER TABLE cart_items DROP COLUMN IF EXISTS product_tracking_id;
