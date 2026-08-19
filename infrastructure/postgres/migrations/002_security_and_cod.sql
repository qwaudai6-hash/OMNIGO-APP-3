-- ============================================================
-- OMNIGO Migration 002 — Security Hardening + COD Ledger Fixes
-- Date: 2026-07-20
-- Session 54, Week 1, Day 1.
-- Resolves bugs: B1, B6, B7, B10, B13 from session 53 audit.
-- ============================================================

-- ────────────────────────────────────────────────────────────
-- 1. Ledger accounts table (BUG B6: cash_receivable for COD)
--
-- Previously the COD Confirm flow debited `central_escrow` which
-- holds zero funds for COD orders. The new account `cash_receivable`
-- is an asset that represents money the rider owes the platform.
-- ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS ledger_accounts (
    id              VARCHAR(50) PRIMARY KEY,  -- 'central_escrow', 'cash_receivable', etc.
    display_name    VARCHAR(100) NOT NULL,
    account_type    VARCHAR(20)  NOT NULL,    -- 'asset' | 'liability' | 'revenue' | 'escrow'
    currency        VARCHAR(3)   NOT NULL DEFAULT 'PKR',
    is_active       BOOLEAN      NOT NULL DEFAULT true,
    metadata        JSONB,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

INSERT INTO ledger_accounts (id, display_name, account_type) VALUES
    ('central_escrow',        'Central Escrow (online payments)',  'liability'),
    ('cash_receivable',       'Cash Receivable (COD in transit)',  'asset'),
    ('admin_revenue',         'Admin Revenue',                     'revenue'),
    ('vendor_locked_escrow',  'Vendor Locked Escrow',              'escrow'),
    ('vendor_released',       'Vendor Released Earnings',          'revenue'),
    ('rider_wallet',          'Rider Wallet',                      'liability'),
    ('rider_cod_debt',        'Rider COD Debt (negative asset)',   'asset'),
    ('customer_refund',       'Customer Refund Liability',         'liability')
ON CONFLICT (id) DO NOTHING;

-- ────────────────────────────────────────────────────────────
-- 2. Composite unique on order idempotency (BUG B6 partial)
-- Fallback when Redis is unavailable. The Redis SETNX still runs
-- first; this index is the Fail-Closed safety net.
-- ────────────────────────────────────────────────────────────
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_idempotency
    ON orders (customer_tracking_id, device_session_nonce)
    WHERE device_session_nonce IS NOT NULL;

-- ────────────────────────────────────────────────────────────
-- 3. Index for admin lineage + finance queries
-- ────────────────────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_orders_created_at
    ON orders (created_at DESC);

CREATE INDEX IF NOT EXISTS idx_orders_vendor_created
    ON orders (vendor_tracking_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_ledger_entries_signature_idx
    ON ledger_entries (idempotency_key, transaction_id);

-- ────────────────────────────────────────────────────────────
-- 4. Internal API audit log (for the new InternalOnly middleware)
-- Every signed internal request gets recorded here for forensic
-- analysis if an endpoint is ever called with a forged signature.
-- ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS internal_api_audit (
    id              BIGSERIAL PRIMARY KEY,
    request_id      UUID NOT NULL DEFAULT gen_random_uuid(),
    caller_service  VARCHAR(50) NOT NULL,        -- 'order-service' etc.
    method          VARCHAR(10) NOT NULL,
    path            VARCHAR(255) NOT NULL,
    remote_addr     INET,
    user_agent      TEXT,
    response_status INT,
    response_time_ms INT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_internal_audit_caller
    ON internal_api_audit (caller_service, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_internal_audit_path
    ON internal_api_audit (path, created_at DESC);

-- ────────────────────────────────────────────────────────────
-- 5. Trigger: enforce idempotency_key UNIQUE in ledger_entries
-- even across partial-failure retries (defensive guard).
-- ────────────────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION enforce_ledger_idempotency()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.idempotency_key IS NULL THEN
        RAISE EXCEPTION 'ledger_entries.idempotency_key is required';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_enforce_ledger_idempotency ON ledger_entries;
CREATE TRIGGER trg_enforce_ledger_idempotency
    BEFORE INSERT ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION enforce_ledger_idempotency();
