-- ============================================================
-- OMNIGO Payment Architecture — Migration 001
-- Double-entry ledger, escrow, COD debts, disputes, vendor payouts
-- ============================================================

-- ────────────────────────────────────────────────────────────
-- Ledger Entries (Double-Entry Accounting)
-- Every money movement = 2 rows sharing the same transaction_id:
-- one DEBIT (negative amount) and one CREDIT (positive amount).
-- ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS ledger_entries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id  UUID NOT NULL,          -- groups debit + credit atomically
    account         VARCHAR(50) NOT NULL,    -- admin_revenue, vendor_escrow, rider_wallet, etc.
    amount          DECIMAL(14,2) NOT NULL,  -- negative = debit, positive = credit
    currency        VARCHAR(3) NOT NULL DEFAULT 'PKR',
    reference_type  VARCHAR(30) NOT NULL,    -- 'order_payment', 'delivery_credit', 'cod_debt', 'payout'
    reference_id    VARCHAR(50),             -- order_tracking_id or delivery_tracking_id
    description     TEXT,
    idempotency_key VARCHAR(128) UNIQUE,    -- prevent duplicate entries
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ledger_transaction ON ledger_entries(transaction_id);
CREATE INDEX IF NOT EXISTS idx_ledger_account ON ledger_entries(account, created_at);
CREATE INDEX IF NOT EXISTS idx_ledger_reference ON ledger_entries(reference_type, reference_id);
CREATE INDEX IF NOT EXISTS idx_ledger_idempotency ON ledger_entries(idempotency_key);

-- ────────────────────────────────────────────────────────────
-- Escrow Holds (Vendor funds locked after delivery completion)
-- Released after 48h if no disputes are filed.
-- ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS escrow_holds (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_tracking_id VARCHAR(50) NOT NULL,
    vendor_tracking_id VARCHAR(50) NOT NULL,
    amount            DECIMAL(14,2) NOT NULL,
    status            VARCHAR(20) NOT NULL DEFAULT 'held',  -- held, released, disputed, refunded
    hold_until        TIMESTAMPTZ NOT NULL,                  -- created_at + 48 hours
    released_at       TIMESTAMPTZ,
    dispute_id        UUID,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_escrow_vendor ON escrow_holds(vendor_tracking_id, status);
CREATE INDEX IF NOT EXISTS idx_escrow_hold_until ON escrow_holds(hold_until, status);
CREATE INDEX IF NOT EXISTS idx_escrow_order ON escrow_holds(order_tracking_id);

-- ────────────────────────────────────────────────────────────
-- COD Debts (Rider collects cash, owes platform)
-- Rider pays via JazzCash/EasyPaisa deep-link → webhook settles debt.
-- ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS cod_debts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_tracking_id   VARCHAR(50) NOT NULL,
    rider_tracking_id   VARCHAR(50) NOT NULL,
    amount_owed         DECIMAL(14,2) NOT NULL,
    status              VARCHAR(20) NOT NULL DEFAULT 'pending',  -- pending, settling, settled, disputed
    settled_via         VARCHAR(30),                              -- jazzcash, easypaisa, bank_transfer
    settled_at          TIMESTAMPTZ,
    webhook_event_id    VARCHAR(128),                             -- idempotency on settlement callback
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_cod_debts_rider ON cod_debts(rider_tracking_id, status);
CREATE INDEX IF NOT EXISTS idx_cod_debts_order ON cod_debts(order_tracking_id);

-- ────────────────────────────────────────────────────────────
-- Vendor Payouts (Settlement records)
-- Created by Payout Releaser Worker after escrow hold expires.
-- ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS vendor_payouts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vendor_tracking_id  VARCHAR(50) NOT NULL,
    amount              DECIMAL(14,2) NOT NULL,
    method              VARCHAR(30),           -- jazzcash, easypaisa, bank_transfer
    status              VARCHAR(20) NOT NULL DEFAULT 'pending',  -- pending, processing, completed, failed
    batch_id            UUID,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at        TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_vendor_payouts_vendor ON vendor_payouts(vendor_tracking_id, status);
CREATE INDEX IF NOT EXISTS idx_vendor_payouts_batch ON vendor_payouts(batch_id);

-- ────────────────────────────────────────────────────────────
-- Disputes (Hold escrow before release)
-- Filed by customer/vendor → escrow frozen → investigated → resolved.
-- ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS disputes (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_tracking_id   VARCHAR(50) NOT NULL,
    filed_by            VARCHAR(50) NOT NULL,   -- user_tracking_id
    reason              TEXT NOT NULL,
    status              VARCHAR(20) NOT NULL DEFAULT 'open',  -- open, investigating, resolved, rejected
    resolution          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at         TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_disputes_order ON disputes(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_disputes_status ON disputes(status);

-- ────────────────────────────────────────────────────────────
-- Vendor Wallet (earnings / payable balance)
-- Mirrors rider_wallet structure for vendor-side accounting.
-- ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS vendor_wallet (
    id                  BIGSERIAL PRIMARY KEY,
    vendor_tracking_id  VARCHAR(50) UNIQUE NOT NULL,
    balance             DECIMAL(14,2) NOT NULL DEFAULT 0 CHECK (balance >= 0),
    lifetime_earnings   DECIMAL(14,2) NOT NULL DEFAULT 0 CHECK (lifetime_earnings >= 0),
    total_payouts       DECIMAL(14,2) NOT NULL DEFAULT 0,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_vendor_wallet_vendor ON vendor_wallet(vendor_tracking_id);

-- ────────────────────────────────────────────────────────────
-- updated_at trigger for new tables
-- ────────────────────────────────────────────────────────────
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'trg_cod_debts_updated_at') THEN
        CREATE TRIGGER trg_cod_debts_updated_at BEFORE UPDATE ON cod_debts FOR EACH ROW EXECUTE FUNCTION omnigo_set_updated_at();
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'trg_vendor_payouts_updated_at') THEN
        CREATE TRIGGER trg_vendor_payouts_updated_at BEFORE UPDATE ON vendor_payouts FOR EACH ROW EXECUTE FUNCTION omnigo_set_updated_at();
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'trg_disputes_updated_at') THEN
        CREATE TRIGGER trg_disputes_updated_at BEFORE UPDATE ON disputes FOR EACH ROW EXECUTE FUNCTION omnigo_set_updated_at();
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'trg_vendor_wallet_updated_at') THEN
        CREATE TRIGGER trg_vendor_wallet_updated_at BEFORE UPDATE ON vendor_wallet FOR EACH ROW EXECUTE FUNCTION omnigo_set_updated_at();
    END IF;
END $$;
