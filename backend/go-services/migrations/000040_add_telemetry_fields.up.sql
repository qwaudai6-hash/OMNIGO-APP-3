ALTER TABLE rider_location_history ADD COLUMN IF NOT EXISTS speed REAL;
ALTER TABLE rider_location_history ADD COLUMN IF NOT EXISTS bearing REAL;
ALTER TABLE rider_location_history ADD COLUMN IF NOT EXISTS battery_pct SMALLINT;
