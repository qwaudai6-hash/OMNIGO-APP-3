-- 0033: H4 Uber-Style Checkout + H3 Routing Audit Trail
-- Customer pays: base_product_amount + delivery_fee_amount = total_billed_amount
-- No fee_payer logic — customer always pays everything (Uber/Careem model)

-- H4: Add explicit billing columns to orders
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS base_product_amount BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS delivery_fee_amount BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS total_billed_amount BIGINT NOT NULL DEFAULT 0;

-- H3: Track whether delivery fee was calculated dynamically or via fallback
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS routing_status VARCHAR(30) NOT NULL DEFAULT 'DYNAMIC_CALCULATED';
-- Valid values: 'DYNAMIC_CALCULATED', 'FALLBACK_HAVERSINE', 'FAILED_CALCULATION'

-- Indexes for billing queries and routing audit
CREATE INDEX IF NOT EXISTS idx_orders_customer_billing ON orders(total_billed_amount);
CREATE INDEX IF NOT EXISTS idx_orders_routing ON orders(routing_status);
