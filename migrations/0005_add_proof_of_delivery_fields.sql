-- Add proof of delivery and dispute resolution fields to deliveries table
ALTER TABLE deliveries 
ADD COLUMN IF NOT EXISTS otp_code VARCHAR(4),
ADD COLUMN IF NOT EXISTS pickup_photo_url TEXT,
ADD COLUMN IF NOT EXISTS delivery_photo_url TEXT,
ADD COLUMN IF NOT EXISTS customer_dispute_photo_url TEXT,
ADD COLUMN IF NOT EXISTS dispute_status VARCHAR(30) DEFAULT 'none';

-- Add indexes for performance
CREATE INDEX IF NOT EXISTS idx_deliveries_dispute_status ON deliveries(dispute_status);
