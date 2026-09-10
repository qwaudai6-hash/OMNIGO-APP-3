-- Add rider_tracking_id to cod_debts for PayFast card payment settlement tracking
ALTER TABLE cod_debts 
ADD COLUMN IF NOT EXISTS rider_tracking_id VARCHAR(100);

CREATE INDEX IF NOT EXISTS idx_cod_debts_rider_tracking_id 
ON cod_debts(rider_tracking_id);

COMMENT ON COLUMN cod_debts.rider_tracking_id IS 'Rider who collected COD cash and is responsible for settlement';
