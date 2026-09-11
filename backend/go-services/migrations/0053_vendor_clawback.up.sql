-- 0053: Add vendor clawback support for returns after escrow release
-- When a customer returns an order after vendor has been paid,
-- the clawback amount is tracked and deducted from next payout.

-- Add clawback column to vendor_wallet
ALTER TABLE vendor_wallet ADD COLUMN IF NOT EXISTS vendor_clawback_paisa BIGINT DEFAULT 0;

-- Add clawback tracking to vendor_payouts
ALTER TABLE vendor_payouts ADD COLUMN IF NOT EXISTS clawback_order_id VARCHAR(50);
ALTER TABLE vendor_payouts ADD COLUMN IF NOT EXISTS clawback_paisa BIGINT DEFAULT 0;

-- Seed the new ledger account
INSERT INTO ledger_accounts (code, name, account_type, is_active, created_at, updated_at)
VALUES ('vendor_clawback', 'Vendor Clawback', 'liability', true, NOW(), NOW())
ON CONFLICT (code) DO NOTHING;
