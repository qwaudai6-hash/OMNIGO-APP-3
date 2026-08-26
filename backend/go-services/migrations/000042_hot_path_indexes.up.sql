-- SP-SQL-34..38: missing indexes on hot query paths + review uniqueness.
-- Identified by Session 61 third-pass audit; these columns are filtered/joined
-- on every order lookup, COD settlement, and favorite/review read.
CREATE INDEX IF NOT EXISTS idx_deliveries_customer
    ON deliveries(customer_tracking_id);

CREATE INDEX IF NOT EXISTS idx_orders_store_tracking
    ON orders(store_tracking_id);

CREATE INDEX IF NOT EXISTS idx_cod_debts_order
    ON cod_debts(order_tracking_id);

CREATE INDEX IF NOT EXISTS idx_favorites_product
    ON favorites(product_tracking_id);

-- One review per customer per product (parity with init.sql schema).
-- NOTE: reviews PK column is user_tracking_id (not customer_tracking_id).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'uq_reviews_product_customer'
    ) THEN
        ALTER TABLE reviews
            ADD CONSTRAINT uq_reviews_product_customer
            UNIQUE (product_tracking_id, user_tracking_id);
    END IF;
END $$;
