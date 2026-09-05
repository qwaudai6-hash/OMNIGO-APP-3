-- Migration 0031: Ledger float64 → int64 paisa refactor
-- Expand-Contract pattern: add new columns, backfill, dual-write, then cutover
-- DO NOT run this migration manually — apply via Railway or psql with the service stopped

BEGIN;

-- ============================================================
-- STEP 1: EXPAND — Add amount_paisa BIGINT columns
-- ============================================================

-- ledger_entries: add amount_paisa (paisa = rupees * 100)
ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS amount_paisa BIGINT;

-- escrow_holds: add amount_paisa
ALTER TABLE escrow_holds ADD COLUMN IF NOT EXISTS amount_paisa BIGINT;

-- rider_wallet: add balance_paisa, cash_in_hand_paisa, lifetime_earnings_paisa
ALTER TABLE rider_wallet ADD COLUMN IF NOT EXISTS balance_paisa BIGINT DEFAULT 0;
ALTER TABLE rider_wallet ADD COLUMN IF NOT EXISTS cash_in_hand_paisa BIGINT DEFAULT 0;
ALTER TABLE rider_wallet ADD COLUMN IF NOT EXISTS lifetime_earnings_paisa BIGINT DEFAULT 0;

-- vendor_wallet: add balance_paisa, lifetime_earnings_paisa, total_payouts_paisa
-- NOTE: vendor_wallet does NOT have pending_payout column in current schema
ALTER TABLE vendor_wallet ADD COLUMN IF NOT EXISTS balance_paisa BIGINT DEFAULT 0;
ALTER TABLE vendor_wallet ADD COLUMN IF NOT EXISTS lifetime_earnings_paisa BIGINT DEFAULT 0;
ALTER TABLE vendor_wallet ADD COLUMN IF NOT EXISTS total_payouts_paisa BIGINT DEFAULT 0;

-- customer_wallet: add balance_paisa, lifetime_spent_paisa
ALTER TABLE customer_wallet ADD COLUMN IF NOT EXISTS balance_paisa BIGINT DEFAULT 0;
ALTER TABLE customer_wallet ADD COLUMN IF NOT EXISTS lifetime_spent_paisa BIGINT DEFAULT 0;

-- ============================================================
-- STEP 2: BACKFILL — Convert existing NUMERIC to BIGINT
-- Using batch update with COMMIT to avoid long locks
-- Run this in batches of 5000 rows for production safety
-- ============================================================

-- Backfill ledger_entries (run in batches if > 100k rows)
UPDATE ledger_entries SET amount_paisa = ROUND(amount * 100)::BIGINT WHERE amount_paisa IS NULL;

-- Backfill escrow_holds
UPDATE escrow_holds SET amount_paisa = ROUND(amount * 100)::BIGINT WHERE amount_paisa IS NULL;

-- Backfill rider_wallet
UPDATE rider_wallet SET
    balance_paisa = ROUND(balance * 100)::BIGINT,
    cash_in_hand_paisa = ROUND(cash_in_hand * 100)::BIGINT,
    lifetime_earnings_paisa = ROUND(lifetime_earnings * 100)::BIGINT
WHERE balance_paisa = 0 AND balance != 0;

-- Backfill vendor_wallet
UPDATE vendor_wallet SET
    balance_paisa = ROUND(balance * 100)::BIGINT,
    lifetime_earnings_paisa = ROUND(lifetime_earnings * 100)::BIGINT,
    total_payouts_paisa = ROUND(total_payouts * 100)::BIGINT
WHERE balance_paisa = 0 AND balance != 0;

-- Backfill customer_wallet
UPDATE customer_wallet SET
    balance_paisa = ROUND(balance * 100)::BIGINT,
    lifetime_spent_paisa = ROUND(lifetime_spent * 100)::BIGINT
WHERE balance_paisa = 0 AND balance != 0;

-- ============================================================
-- STEP 3: DUAL-WRITE TRIGGERS — Keep old and new columns in sync
-- ============================================================

-- Trigger: ledger_entries — sync amount ↔ amount_paisa on INSERT/UPDATE
CREATE OR REPLACE FUNCTION sync_ledger_amount_paisa() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' OR TG_OP = 'UPDATE' THEN
        IF NEW.amount_paisa IS NULL THEN
            NEW.amount_paisa := ROUND(NEW.amount * 100)::BIGINT;
        END IF;
        IF NEW.amount IS NULL OR NEW.amount = 0 THEN
            NEW.amount := NEW.amount_paisa::NUMERIC / 100.0;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_ledger_amount ON ledger_entries;
CREATE TRIGGER trg_sync_ledger_amount
    BEFORE INSERT OR UPDATE ON ledger_entries
    FOR EACH ROW
    EXECUTE FUNCTION sync_ledger_amount_paisa();

-- Trigger: escrow_holds — sync amount ↔ amount_paisa
CREATE OR REPLACE FUNCTION sync_escrow_amount_paisa() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' OR TG_OP = 'UPDATE' THEN
        IF NEW.amount_paisa IS NULL THEN
            NEW.amount_paisa := ROUND(NEW.amount * 100)::BIGINT;
        END IF;
        IF NEW.amount IS NULL OR NEW.amount = 0 THEN
            NEW.amount := NEW.amount_paisa::NUMERIC / 100.0;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_escrow_amount ON escrow_holds;
CREATE TRIGGER trg_sync_escrow_amount
    BEFORE INSERT OR UPDATE ON escrow_holds
    FOR EACH ROW
    EXECUTE FUNCTION sync_escrow_amount_paisa();

-- Trigger: rider_wallet — sync balance fields
CREATE OR REPLACE FUNCTION sync_rider_wallet_paisa() RETURNS trigger AS $$
BEGIN
    IF NEW.balance_paisa = 0 AND NEW.balance != 0 THEN
        NEW.balance_paisa := ROUND(NEW.balance * 100)::BIGINT;
    END IF;
    IF NEW.cash_in_hand_paisa = 0 AND NEW.cash_in_hand != 0 THEN
        NEW.cash_in_hand_paisa := ROUND(NEW.cash_in_hand * 100)::BIGINT;
    END IF;
    IF NEW.lifetime_earnings_paisa = 0 AND NEW.lifetime_earnings != 0 THEN
        NEW.lifetime_earnings_paisa := ROUND(NEW.lifetime_earnings * 100)::BIGINT;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_rider_wallet ON rider_wallet;
CREATE TRIGGER trg_sync_rider_wallet
    BEFORE INSERT OR UPDATE ON rider_wallet
    FOR EACH ROW
    EXECUTE FUNCTION sync_rider_wallet_paisa();

-- Trigger: vendor_wallet — sync balance fields
CREATE OR REPLACE FUNCTION sync_vendor_wallet_paisa() RETURNS trigger AS $$
BEGIN
    IF NEW.balance_paisa = 0 AND NEW.balance != 0 THEN
        NEW.balance_paisa := ROUND(NEW.balance * 100)::BIGINT;
    END IF;
    IF NEW.lifetime_earnings_paisa = 0 AND NEW.lifetime_earnings != 0 THEN
        NEW.lifetime_earnings_paisa := ROUND(NEW.lifetime_earnings * 100)::BIGINT;
    END IF;
    IF NEW.total_payouts_paisa = 0 AND NEW.total_payouts != 0 THEN
        NEW.total_payouts_paisa := ROUND(NEW.total_payouts * 100)::BIGINT;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_vendor_wallet ON vendor_wallet;
CREATE TRIGGER trg_sync_vendor_wallet
    BEFORE INSERT OR UPDATE ON vendor_wallet
    FOR EACH ROW
    EXECUTE FUNCTION sync_vendor_wallet_paisa();

-- Trigger: customer_wallet — sync balance fields
CREATE OR REPLACE FUNCTION sync_customer_wallet_paisa() RETURNS trigger AS $$
BEGIN
    IF NEW.balance_paisa = 0 AND NEW.balance != 0 THEN
        NEW.balance_paisa := ROUND(NEW.balance * 100)::BIGINT;
    END IF;
    IF NEW.lifetime_spent_paisa = 0 AND NEW.lifetime_spent != 0 THEN
        NEW.lifetime_spent_paisa := ROUND(NEW.lifetime_spent * 100)::BIGINT;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_customer_wallet ON customer_wallet;
CREATE TRIGGER trg_sync_customer_wallet
    BEFORE INSERT OR UPDATE ON customer_wallet
    FOR EACH ROW
    EXECUTE FUNCTION sync_customer_wallet_paisa();

-- ============================================================
-- STEP 4: INDEXES for new columns (for query performance)
-- ============================================================

-- No new indexes needed — amount_paisa is used for aggregation, not lookups
-- Existing account/reference indexes cover the read patterns

-- ============================================================
-- STEP 5: CONSTRAINTS — Ensure data integrity
-- ============================================================

-- Add NOT NULL constraints after backfill is confirmed
-- (Run these AFTER verifying all rows are backfilled)
-- ALTER TABLE ledger_entries ALTER COLUMN amount_paisa SET NOT NULL;
-- ALTER TABLE escrow_holds ALTER COLUMN amount_paisa SET NOT NULL;

COMMIT;

-- ============================================================
-- ROLLBACK PLAN (if needed):
-- DROP TRIGGER IF EXISTS trg_sync_ledger_amount ON ledger_entries;
-- DROP TRIGGER IF EXISTS trg_sync_escrow_amount ON escrow_holds;
-- DROP TRIGGER IF EXISTS trg_sync_rider_wallet ON rider_wallet;
-- DROP TRIGGER IF EXISTS trg_sync_vendor_wallet ON vendor_wallet;
-- DROP TRIGGER IF EXISTS trg_sync_customer_wallet ON customer_wallet;
-- ALTER TABLE ledger_entries DROP COLUMN IF EXISTS amount_paisa;
-- ALTER TABLE escrow_holds DROP COLUMN IF EXISTS amount_paisa;
-- ALTER TABLE rider_wallet DROP COLUMN IF EXISTS balance_paisa;
-- ALTER TABLE rider_wallet DROP COLUMN IF EXISTS cash_in_hand_paisa;
-- ALTER TABLE rider_wallet DROP COLUMN IF EXISTS lifetime_earnings_paisa;
-- ALTER TABLE vendor_wallet DROP COLUMN IF EXISTS balance_paisa;
-- ALTER TABLE vendor_wallet DROP COLUMN IF EXISTS lifetime_earnings_paisa;
-- ALTER TABLE vendor_wallet DROP COLUMN IF EXISTS total_payouts_paisa;
-- ALTER TABLE customer_wallet DROP COLUMN IF EXISTS balance_paisa;
-- ALTER TABLE customer_wallet DROP COLUMN IF EXISTS lifetime_spent_paisa;
-- ============================================================
