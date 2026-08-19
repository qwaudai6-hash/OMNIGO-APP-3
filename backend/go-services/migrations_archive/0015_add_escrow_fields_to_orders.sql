ALTER TABLE orders 
ADD COLUMN IF NOT EXISTS escrow_released BOOLEAN DEFAULT FALSE,
ADD COLUMN IF NOT EXISTS dispute_status VARCHAR(20) DEFAULT 'NONE',
ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMP;

-- Index to optimize the background cron job scanning for 48-hour holds
CREATE INDEX IF NOT EXISTS idx_orders_escrow ON orders(status, escrow_released, dispute_status, delivered_at);
