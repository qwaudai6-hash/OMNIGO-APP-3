-- HIGH-07: persist actual ride distance and duration at completion.
-- Previously CompleteRide only stored fare_amount; rider-reported telemetry
-- (distance_meters, duration_seconds) was discarded, making ride analytics
-- and per-km earnings audits impossible.
ALTER TABLE rides ADD COLUMN IF NOT EXISTS actual_distance_meters DOUBLE PRECISION;
ALTER TABLE rides ADD COLUMN IF NOT EXISTS actual_duration_seconds DOUBLE PRECISION;
