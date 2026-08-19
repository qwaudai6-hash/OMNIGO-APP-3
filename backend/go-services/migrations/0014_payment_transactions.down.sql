-- Migration 0014_down: remove payment_transactions structures

DROP TRIGGER IF EXISTS trg_payment_transactions_update_order ON payment_transactions;
DROP FUNCTION IF EXISTS update_order_payment_status();
DROP FUNCTION IF EXISTS cleanup_expired_payment_idempotency();

DROP TABLE IF EXISTS payment_idempotency;
DROP TABLE IF EXISTS payment_transactions;
