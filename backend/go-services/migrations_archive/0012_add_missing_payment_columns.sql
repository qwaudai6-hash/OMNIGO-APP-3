-- Migration 0012: Add missing payment_status and hold_until columns
-- These columns are referenced by Go code but were missing from the schema.

-- Add payment_status to orders table (used by reconciliation worker)
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'orders' AND column_name = 'payment_status') THEN
        ALTER TABLE orders ADD COLUMN payment_status VARCHAR(30) DEFAULT 'pending';
        CREATE INDEX IF NOT EXISTS idx_orders_payment_status ON orders(payment_status);
    END IF;
END $$;

-- Add hold_until to escrow_holds table (used by escrow releaser)
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'escrow_holds' AND column_name = 'hold_until') THEN
        ALTER TABLE escrow_holds ADD COLUMN hold_until TIMESTAMPTZ DEFAULT NOW() + INTERVAL '7 days';
        CREATE INDEX IF NOT EXISTS idx_escrow_hold_until ON escrow_holds(hold_until);
    END IF;
END $$;
