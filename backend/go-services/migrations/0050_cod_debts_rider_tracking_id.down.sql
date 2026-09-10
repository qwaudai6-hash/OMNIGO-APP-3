DROP INDEX IF EXISTS idx_cod_debts_rider_tracking_id;
ALTER TABLE cod_debts DROP COLUMN IF EXISTS rider_tracking_id;
