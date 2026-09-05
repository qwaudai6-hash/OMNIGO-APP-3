-- Migration 0032: Full H4 Completion — paisa columns for orders, products, cart, etc.
-- Expand-Contract pattern: add new columns, backfill, dual-write, then cutover.
--
-- Tables being migrated (9 remaining after H4 partial completion):
--   1. orders           — total_amount, admin_commission
--   2. order_items      — price_at_checkout
--   3. products         — base_price
--   4. cart             — price, total_amount (handled by service layer; skip for now)
--   5. payment_transactions — amount
--   6. deliveries       — admin_commission, rider_earning
--   7. rides            — fare_amount, negotiated_fare, admin_commission
--   8. vendor_payouts   — amount
--   9. cod_debts        — amount_owed
--
-- After this migration:
--   *_paisa columns exist with NOT NULL DEFAULT 0
--   Old NUMERIC columns remain (kept for safety during cutover)
--   Dual-write triggers keep old/new columns in sync
--   API responses expose both paisa (internal) and rupees (display) variants

BEGIN;

-- ============================================================
-- STEP 1: EXPAND — Add *_paisa BIGINT columns
-- ============================================================

-- orders
ALTER TABLE orders ADD COLUMN IF NOT EXISTS total_amount_paisa BIGINT NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS admin_commission_paisa BIGINT NOT NULL DEFAULT 0;

-- order_items
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS price_at_checkout_paisa BIGINT NOT NULL DEFAULT 0;

-- products
ALTER TABLE products ADD COLUMN IF NOT EXISTS base_price_paisa BIGINT NOT NULL DEFAULT 0;

-- payment_transactions
ALTER TABLE payment_transactions ADD COLUMN IF NOT EXISTS amount_paisa BIGINT NOT NULL DEFAULT 0;

-- deliveries
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS admin_commission_paisa BIGINT NOT NULL DEFAULT 0;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS rider_earning_paisa BIGINT NOT NULL DEFAULT 0;

-- rides
ALTER TABLE rides ADD COLUMN IF NOT EXISTS fare_amount_paisa BIGINT NOT NULL DEFAULT 0;
ALTER TABLE rides ADD COLUMN IF NOT EXISTS negotiated_fare_paisa BIGINT;
ALTER TABLE rides ADD COLUMN IF NOT EXISTS admin_commission_paisa BIGINT NOT NULL DEFAULT 0;

-- vendor_payouts
ALTER TABLE vendor_payouts ADD COLUMN IF NOT EXISTS amount_paisa BIGINT NOT NULL DEFAULT 0;

-- cod_debts
ALTER TABLE cod_debts ADD COLUMN IF NOT EXISTS amount_owed_paisa BIGINT NOT NULL DEFAULT 0;

-- ============================================================
-- STEP 2: BACKFILL from old NUMERIC columns
-- ============================================================

-- orders
UPDATE orders SET
    total_amount_paisa = ROUND(total_amount * 100)::BIGINT,
    admin_commission_paisa = ROUND(admin_commission * 100)::BIGINT
WHERE total_amount_paisa = 0 AND total_amount != 0;

-- order_items
UPDATE order_items SET
    price_at_checkout_paisa = ROUND(price_at_checkout * 100)::BIGINT
WHERE price_at_checkout_paisa = 0 AND price_at_checkout IS NOT NULL AND price_at_checkout != 0;

-- products
UPDATE products SET
    base_price_paisa = ROUND(base_price * 100)::BIGINT
WHERE base_price_paisa = 0 AND base_price IS NOT NULL AND base_price != 0;

-- payment_transactions
UPDATE payment_transactions SET
    amount_paisa = ROUND(amount * 100)::BIGINT
WHERE amount_paisa = 0 AND amount IS NOT NULL AND amount != 0;

-- deliveries
UPDATE deliveries SET
    admin_commission_paisa = ROUND(admin_commission * 100)::BIGINT,
    rider_earning_paisa = ROUND(rider_earning * 100)::BIGINT
WHERE (admin_commission_paisa = 0 AND admin_commission != 0)
   OR (rider_earning_paisa = 0 AND rider_earning != 0);

-- rides
UPDATE rides SET
    fare_amount_paisa = ROUND(fare_amount * 100)::BIGINT,
    negotiated_fare_paisa = CASE WHEN negotiated_fare IS NOT NULL THEN ROUND(negotiated_fare * 100)::BIGINT ELSE NULL END,
    admin_commission_paisa = ROUND(admin_commission * 100)::BIGINT
WHERE (fare_amount_paisa = 0 AND fare_amount != 0)
   OR (negotiated_fare_paisa IS NULL AND negotiated_fare IS NOT NULL AND negotiated_fare != 0)
   OR (admin_commission_paisa = 0 AND admin_commission != 0);

-- vendor_payouts
UPDATE vendor_payouts SET
    amount_paisa = ROUND(amount * 100)::BIGINT
WHERE amount_paisa = 0 AND amount IS NOT NULL AND amount != 0;

-- cod_debts
UPDATE cod_debts SET
    amount_owed_paisa = ROUND(amount_owed * 100)::BIGINT
WHERE amount_owed_paisa = 0 AND amount_owed != 0;

-- ============================================================
-- STEP 3: DUAL-WRITE TRIGGERS
-- When old column is written, sync paisa. When paisa is written, sync old.
-- This lets either code path write the value and the other column stays correct.
-- ============================================================

-- orders: total_amount <-> total_amount_paisa
CREATE OR REPLACE FUNCTION sync_orders_paisa() RETURNS trigger AS $$
BEGIN
    -- If old column was written (NUMERIC), sync paisa
    IF NEW.total_amount_paisa = 0 AND NEW.total_amount IS NOT NULL AND NEW.total_amount != 0 THEN
        NEW.total_amount_paisa := ROUND(NEW.total_amount * 100)::BIGINT;
    END IF;
    -- If paisa was written, sync old column
    IF NEW.total_amount_paisa > 0 AND (NEW.total_amount IS NULL OR NEW.total_amount = 0) THEN
        NEW.total_amount := NEW.total_amount_paisa / 100.0;
    END IF;

    IF NEW.admin_commission_paisa = 0 AND NEW.admin_commission IS NOT NULL AND NEW.admin_commission != 0 THEN
        NEW.admin_commission_paisa := ROUND(NEW.admin_commission * 100)::BIGINT;
    END IF;
    IF NEW.admin_commission_paisa > 0 AND (NEW.admin_commission IS NULL OR NEW.admin_commission = 0) THEN
        NEW.admin_commission := NEW.admin_commission_paisa / 100.0;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_orders_paisa ON orders;
CREATE TRIGGER trg_sync_orders_paisa
    BEFORE INSERT OR UPDATE ON orders
    FOR EACH ROW
    EXECUTE FUNCTION sync_orders_paisa();

-- order_items: price_at_checkout <-> price_at_checkout_paisa
CREATE OR REPLACE FUNCTION sync_order_items_paisa() RETURNS trigger AS $$
BEGIN
    IF NEW.price_at_checkout_paisa = 0 AND NEW.price_at_checkout IS NOT NULL AND NEW.price_at_checkout != 0 THEN
        NEW.price_at_checkout_paisa := ROUND(NEW.price_at_checkout * 100)::BIGINT;
    END IF;
    IF NEW.price_at_checkout_paisa > 0 AND (NEW.price_at_checkout IS NULL OR NEW.price_at_checkout = 0) THEN
        NEW.price_at_checkout := NEW.price_at_checkout_paisa / 100.0;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_order_items_paisa ON order_items;
CREATE TRIGGER trg_sync_order_items_paisa
    BEFORE INSERT OR UPDATE ON order_items
    FOR EACH ROW
    EXECUTE FUNCTION sync_order_items_paisa();

-- products: base_price <-> base_price_paisa
CREATE OR REPLACE FUNCTION sync_products_paisa() RETURNS trigger AS $$
BEGIN
    IF NEW.base_price_paisa = 0 AND NEW.base_price IS NOT NULL AND NEW.base_price != 0 THEN
        NEW.base_price_paisa := ROUND(NEW.base_price * 100)::BIGINT;
    END IF;
    IF NEW.base_price_paisa > 0 AND (NEW.base_price IS NULL OR NEW.base_price = 0) THEN
        NEW.base_price := NEW.base_price_paisa / 100.0;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_products_paisa ON products;
CREATE TRIGGER trg_sync_products_paisa
    BEFORE INSERT OR UPDATE ON products
    FOR EACH ROW
    EXECUTE FUNCTION sync_products_paisa();

-- payment_transactions: amount <-> amount_paisa
CREATE OR REPLACE FUNCTION sync_payment_transactions_paisa() RETURNS trigger AS $$
BEGIN
    IF NEW.amount_paisa = 0 AND NEW.amount IS NOT NULL AND NEW.amount != 0 THEN
        NEW.amount_paisa := ROUND(NEW.amount * 100)::BIGINT;
    END IF;
    IF NEW.amount_paisa > 0 AND (NEW.amount IS NULL OR NEW.amount = 0) THEN
        NEW.amount := NEW.amount_paisa / 100.0;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_payment_transactions_paisa ON payment_transactions;
CREATE TRIGGER trg_sync_payment_transactions_paisa
    BEFORE INSERT OR UPDATE ON payment_transactions
    FOR EACH ROW
    EXECUTE FUNCTION sync_payment_transactions_paisa();

-- deliveries: admin_commission, rider_earning <-> *_paisa
CREATE OR REPLACE FUNCTION sync_deliveries_paisa() RETURNS trigger AS $$
BEGIN
    IF NEW.admin_commission_paisa = 0 AND NEW.admin_commission IS NOT NULL AND NEW.admin_commission != 0 THEN
        NEW.admin_commission_paisa := ROUND(NEW.admin_commission * 100)::BIGINT;
    END IF;
    IF NEW.admin_commission_paisa > 0 AND (NEW.admin_commission IS NULL OR NEW.admin_commission = 0) THEN
        NEW.admin_commission := NEW.admin_commission_paisa / 100.0;
    END IF;

    IF NEW.rider_earning_paisa = 0 AND NEW.rider_earning IS NOT NULL AND NEW.rider_earning != 0 THEN
        NEW.rider_earning_paisa := ROUND(NEW.rider_earning * 100)::BIGINT;
    END IF;
    IF NEW.rider_earning_paisa > 0 AND (NEW.rider_earning IS NULL OR NEW.rider_earning = 0) THEN
        NEW.rider_earning := NEW.rider_earning_paisa / 100.0;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_deliveries_paisa ON deliveries;
CREATE TRIGGER trg_sync_deliveries_paisa
    BEFORE INSERT OR UPDATE ON deliveries
    FOR EACH ROW
    EXECUTE FUNCTION sync_deliveries_paisa();

-- rides: fare_amount, negotiated_fare, admin_commission <-> *_paisa
CREATE OR REPLACE FUNCTION sync_rides_paisa() RETURNS trigger AS $$
BEGIN
    IF NEW.fare_amount_paisa = 0 AND NEW.fare_amount IS NOT NULL AND NEW.fare_amount != 0 THEN
        NEW.fare_amount_paisa := ROUND(NEW.fare_amount * 100)::BIGINT;
    END IF;
    IF NEW.fare_amount_paisa > 0 AND (NEW.fare_amount IS NULL OR NEW.fare_amount = 0) THEN
        NEW.fare_amount := NEW.fare_amount_paisa / 100.0;
    END IF;

    IF NEW.negotiated_fare_paisa IS NULL AND NEW.negotiated_fare IS NOT NULL AND NEW.negotiated_fare != 0 THEN
        NEW.negotiated_fare_paisa := ROUND(NEW.negotiated_fare * 100)::BIGINT;
    END IF;
    IF NEW.negotiated_fare_paisa IS NOT NULL AND NEW.negotiated_fare_paisa > 0 AND (NEW.negotiated_fare IS NULL OR NEW.negotiated_fare = 0) THEN
        NEW.negotiated_fare := NEW.negotiated_fare_paisa / 100.0;
    END IF;

    IF NEW.admin_commission_paisa = 0 AND NEW.admin_commission IS NOT NULL AND NEW.admin_commission != 0 THEN
        NEW.admin_commission_paisa := ROUND(NEW.admin_commission * 100)::BIGINT;
    END IF;
    IF NEW.admin_commission_paisa > 0 AND (NEW.admin_commission IS NULL OR NEW.admin_commission = 0) THEN
        NEW.admin_commission := NEW.admin_commission_paisa / 100.0;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_rides_paisa ON rides;
CREATE TRIGGER trg_sync_rides_paisa
    BEFORE INSERT OR UPDATE ON rides
    FOR EACH ROW
    EXECUTE FUNCTION sync_rides_paisa();

-- vendor_payouts: amount <-> amount_paisa
CREATE OR REPLACE FUNCTION sync_vendor_payouts_paisa() RETURNS trigger AS $$
BEGIN
    IF NEW.amount_paisa = 0 AND NEW.amount IS NOT NULL AND NEW.amount != 0 THEN
        NEW.amount_paisa := ROUND(NEW.amount * 100)::BIGINT;
    END IF;
    IF NEW.amount_paisa > 0 AND (NEW.amount IS NULL OR NEW.amount = 0) THEN
        NEW.amount := NEW.amount_paisa / 100.0;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_vendor_payouts_paisa ON vendor_payouts;
CREATE TRIGGER trg_sync_vendor_payouts_paisa
    BEFORE INSERT OR UPDATE ON vendor_payouts
    FOR EACH ROW
    EXECUTE FUNCTION sync_vendor_payouts_paisa();

-- cod_debts: amount_owed <-> amount_owed_paisa
CREATE OR REPLACE FUNCTION sync_cod_debts_paisa() RETURNS trigger AS $$
BEGIN
    IF NEW.amount_owed_paisa = 0 AND NEW.amount_owed IS NOT NULL AND NEW.amount_owed != 0 THEN
        NEW.amount_owed_paisa := ROUND(NEW.amount_owed * 100)::BIGINT;
    END IF;
    IF NEW.amount_owed_paisa > 0 AND (NEW.amount_owed IS NULL OR NEW.amount_owed = 0) THEN
        NEW.amount_owed := NEW.amount_owed_paisa / 100.0;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_cod_debts_paisa ON cod_debts;
CREATE TRIGGER trg_sync_cod_debts_paisa
    BEFORE INSERT OR UPDATE ON cod_debts
    FOR EACH ROW
    EXECUTE FUNCTION sync_cod_debts_paisa();

-- ============================================================
-- STEP 4: INDEXES for query performance
-- ============================================================

-- Most queries filter on *_paisa columns for new code, old indexes still work
CREATE INDEX IF NOT EXISTS idx_orders_total_amount_paisa ON orders(total_amount_paisa);
CREATE INDEX IF NOT EXISTS idx_products_base_price_paisa ON products(base_price_paisa);
CREATE INDEX IF NOT EXISTS idx_rides_fare_amount_paisa ON rides(fare_amount_paisa);

-- ============================================================
-- STEP 5: COMMENTS for documentation
-- ============================================================

COMMENT ON COLUMN orders.total_amount_paisa IS 'Order total in paisa (int64). 1 PKR = 100 paisa. Source of truth for all order amount calculations. Added in H6 (migration 0032).';
COMMENT ON COLUMN orders.admin_commission_paisa IS 'Admin commission in paisa (int64). Added in H6 (migration 0032).';
COMMENT ON COLUMN order_items.price_at_checkout_paisa IS 'Price at checkout in paisa (int64). Frozen snapshot at order time. Added in H6 (migration 0032).';
COMMENT ON COLUMN products.base_price_paisa IS 'Base price in paisa (int64). 1 PKR = 100 paisa. Added in H6 (migration 0032).';
COMMENT ON COLUMN payment_transactions.amount_paisa IS 'Payment amount in paisa (int64). Source of truth. Added in H6 (migration 0032).';
COMMENT ON COLUMN deliveries.admin_commission_paisa IS 'Admin commission on delivery in paisa (int64). Added in H6 (migration 0032).';
COMMENT ON COLUMN deliveries.rider_earning_paisa IS 'Rider earning in paisa (int64). Added in H6 (migration 0032).';
COMMENT ON COLUMN rides.fare_amount_paisa IS 'Ride fare in paisa (int64). Added in H6 (migration 0032).';
COMMENT ON COLUMN rides.negotiated_fare_paisa IS 'Negotiated fare in paisa (int64, nullable). Added in H6 (migration 0032).';
COMMENT ON COLUMN rides.admin_commission_paisa IS 'Admin commission in paisa (int64). Added in H6 (migration 0032).';
COMMENT ON COLUMN vendor_payouts.amount_paisa IS 'Payout amount in paisa (int64). Added in H6 (migration 0032).';
COMMENT ON COLUMN cod_debts.amount_owed_paisa IS 'COD amount owed in paisa (int64). Added in H6 (migration 0032).';

-- ============================================================
-- ROLLBACK PLAN (if needed):
-- DROP TRIGGER IF EXISTS trg_sync_orders_paisa ON orders;
-- DROP TRIGGER IF EXISTS trg_sync_order_items_paisa ON order_items;
-- DROP TRIGGER IF EXISTS trg_sync_products_paisa ON products;
-- DROP TRIGGER IF EXISTS trg_sync_payment_transactions_paisa ON payment_transactions;
-- DROP TRIGGER IF EXISTS trg_sync_deliveries_paisa ON deliveries;
-- DROP TRIGGER IF EXISTS trg_sync_rides_paisa ON rides;
-- DROP TRIGGER IF EXISTS trg_sync_vendor_payouts_paisa ON vendor_payouts;
-- DROP TRIGGER IF EXISTS trg_sync_cod_debts_paisa ON cod_debts;
--
-- ALTER TABLE orders DROP COLUMN IF EXISTS total_amount_paisa, DROP COLUMN IF EXISTS admin_commission_paisa;
-- ALTER TABLE order_items DROP COLUMN IF EXISTS price_at_checkout_paisa;
-- ALTER TABLE products DROP COLUMN IF EXISTS base_price_paisa;
-- ALTER TABLE payment_transactions DROP COLUMN IF EXISTS amount_paisa;
-- ALTER TABLE deliveries DROP COLUMN IF EXISTS admin_commission_paisa, DROP COLUMN IF EXISTS rider_earning_paisa;
-- ALTER TABLE rides DROP COLUMN IF EXISTS fare_amount_paisa, DROP COLUMN IF EXISTS negotiated_fare_paisa, DROP COLUMN IF EXISTS admin_commission_paisa;
-- ALTER TABLE vendor_payouts DROP COLUMN IF EXISTS amount_paisa;
-- ALTER TABLE cod_debts DROP COLUMN IF EXISTS amount_owed_paisa;
-- ============================================================

COMMIT;
