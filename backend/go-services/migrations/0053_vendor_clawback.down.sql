-- 0053 down: Remove vendor clawback support
ALTER TABLE vendor_wallet DROP COLUMN IF EXISTS vendor_clawback_paisa;
ALTER TABLE vendor_payouts DROP COLUMN IF EXISTS clawback_order_id;
ALTER TABLE vendor_payouts DROP COLUMN IF EXISTS clawback_paisa;
DELETE FROM ledger_accounts WHERE code = 'vendor_clawback';
