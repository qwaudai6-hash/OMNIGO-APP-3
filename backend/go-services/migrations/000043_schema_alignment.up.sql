-- 000043 SCHEMA ALIGNMENT — bridges gaps between Go code expectations and the
-- authoritative embedded migration chain (000001 baseline + 000040..42).
-- Found via Session-62 full schema-vs-code mapping audit.
--
-- Idempotency note: golang-migrate runs each file once, but every statement
-- below still guards with IF EXISTS / IF NOT EXISTS where possible so mixed-
-- state environments (e.g. DBs previously migrated by the legacy top-level
-- scripts) fail loudly only on true conflicts, not cosmetic ones.

-- ── 1. cod_debts ────────────────────────────────────────────────────────
-- Code inserts UUID ids and uses amount_owed / webhook_event_id / settled_via.
ALTER TABLE cod_debts ALTER COLUMN id DROP DEFAULT;
ALTER TABLE cod_debts ALTER COLUMN id TYPE uuid
    USING (md5(id::text || clock_timestamp()::text))::uuid;
ALTER TABLE cod_debts ADD COLUMN IF NOT EXISTS amount_owed      NUMERIC(10,2);
ALTER TABLE cod_debts ADD COLUMN IF NOT EXISTS webhook_event_id VARCHAR(100);
ALTER TABLE cod_debts ADD COLUMN IF NOT EXISTS settled_via      VARCHAR(30);
-- Backfill from the legacy column name for pre-existing rows.
UPDATE cod_debts SET amount_owed = amount WHERE amount_owed IS NULL AND amount IS NOT NULL;
ALTER TABLE cod_debts ALTER COLUMN amount_owed SET DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_cod_debts_webhook_event ON cod_debts(webhook_event_id);

-- ── 2. disputes ─────────────────────────────────────────────────────────
-- Handlers generate uuid ids and stamp resolved_at on resolution.
ALTER TABLE disputes ALTER COLUMN id DROP DEFAULT;
ALTER TABLE disputes ALTER COLUMN id TYPE uuid
    USING (md5(id::text || clock_timestamp()::text))::uuid;
ALTER TABLE disputes ADD COLUMN IF NOT EXISTS resolved_at   TIMESTAMPTZ;
ALTER TABLE disputes ADD COLUMN IF NOT EXISTS resolved_by   VARCHAR(50);
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'disputes' AND column_name = 'tracking_id'
    ) THEN
        ALTER TABLE disputes ALTER COLUMN tracking_id DROP NOT NULL;
    END IF;
END $$;

-- ── 3. vendor_payouts ───────────────────────────────────────────────────
ALTER TABLE vendor_payouts ALTER COLUMN id DROP DEFAULT;
ALTER TABLE vendor_payouts ALTER COLUMN id TYPE uuid
    USING (md5(id::text || clock_timestamp()::text))::uuid;
ALTER TABLE vendor_payouts ADD COLUMN IF NOT EXISTS method       VARCHAR(30) DEFAULT 'bank_transfer';
ALTER TABLE vendor_payouts ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;
ALTER TABLE vendor_payouts ADD COLUMN IF NOT EXISTS batch_id     VARCHAR(60);
CREATE INDEX IF NOT EXISTS idx_vendor_payouts_batch ON vendor_payouts(batch_id);

-- ── 4. escrow_holds ─────────────────────────────────────────────────────
-- Repository scans ids as uuid.UUID and upserts on (order, vendor) pairs.
ALTER TABLE escrow_holds ALTER COLUMN id DROP DEFAULT;
ALTER TABLE escrow_holds ALTER COLUMN id TYPE uuid
    USING (md5(id::text || clock_timestamp()::text))::uuid;
CREATE UNIQUE INDEX IF NOT EXISTS uq_escrow_hold_order_vendor
    ON escrow_holds(order_tracking_id, vendor_tracking_id);

-- ── 5. Wallet ledger columns ────────────────────────────────────────────
ALTER TABLE vendor_wallet ADD COLUMN IF NOT EXISTS lifetime_earnings DECIMAL(14,2) NOT NULL DEFAULT 0;
ALTER TABLE vendor_wallet ADD COLUMN IF NOT EXISTS total_payouts     DECIMAL(14,2) NOT NULL DEFAULT 0;
ALTER TABLE rider_wallet ADD COLUMN IF NOT EXISTS lifetime_earnings  DECIMAL(14,2) NOT NULL DEFAULT 0;

-- ── 6. reviews ──────────────────────────────────────────────────────────
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- ── 7. orders: vendor handover evidence (RecordVendorHandover) ──────────
ALTER TABLE orders ADD COLUMN IF NOT EXISTS handover_photo_url TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS handover_at        TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS handover_notes     TEXT;

-- ── 8. deliveries: fee split math + cancel state ────────────────────────
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS delivery_fee NUMERIC(10,2) NOT NULL DEFAULT 0;

-- Allow gig cancellations: code sets status='cancelled' in CancelDeliveryForOrder,
-- but the baseline CHECK omitted it. Drop+re-add guarded.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_deliveries_status'
          AND pg_get_constraintdef(oid) NOT LIKE '%cancelled%'
    ) THEN
        ALTER TABLE deliveries DROP CONSTRAINT chk_deliveries_status;
        ALTER TABLE deliveries ADD CONSTRAINT chk_deliveries_status
            CHECK (status IN ('broadcasting','assigned','accepted','picked_up',
                              'in_transit','completed','failed','cancelled','disputed'));
    END IF;
END $$;

-- ── 9. payment_idempotency (legacy payment-service duplicate guard) ─────
CREATE TABLE IF NOT EXISTS payment_idempotency (
    key            TEXT PRIMARY KEY,
    request_hash   TEXT NOT NULL,
    transaction_id TEXT,
    expires_at     TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '24 hours'
);

-- ── 10. chat_messages ───────────────────────────────────────────────────
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'chat_messages' AND column_name = 'id' AND data_type = 'bigint'
    ) THEN
        ALTER TABLE chat_messages ALTER COLUMN id DROP DEFAULT;
        ALTER TABLE chat_messages ALTER COLUMN id TYPE VARCHAR(64);
    END IF;
END $$;
