-- M4: Add CHECK constraints to prevent negative wallet balances at DB level.
-- Application-level guards exist (WHERE balance >= $1) but this adds defense-in-depth.

ALTER TABLE rider_wallet
    ADD CONSTRAINT chk_rider_wallet_balance_non_negative
    CHECK (balance >= 0);

ALTER TABLE vendor_wallet
    ADD CONSTRAINT chk_vendor_wallet_balance_non_negative
    CHECK (balance >= 0);

ALTER TABLE customer_wallet
    ADD CONSTRAINT chk_customer_wallet_balance_non_negative
    CHECK (balance >= 0);
