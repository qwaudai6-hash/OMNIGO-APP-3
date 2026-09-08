-- Add is_active column to users table if missing.
-- Login query uses COALESCE(is_active, true) which fails when the column
-- does not exist at all (not just NULL).
ALTER TABLE users ADD COLUMN IF NOT EXISTS is_active BOOLEAN DEFAULT true;
