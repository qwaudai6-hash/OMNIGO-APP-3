-- Migration to add vehicle_type to rides table
ALTER TABLE rides ADD COLUMN IF NOT EXISTS vehicle_type VARCHAR(30) NOT NULL DEFAULT 'bike';
