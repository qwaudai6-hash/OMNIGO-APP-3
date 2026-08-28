-- Composite indexes for background workers that filter by status + time range.
-- These support the OrderTimeoutWorker and GigTimeoutWorker queries with
-- FOR UPDATE SKIP LOCKED patterns.

-- OrderTimeoutWorker: WHERE status = 'pending' AND created_at < NOW() - INTERVAL '30 minutes'
CREATE INDEX IF NOT EXISTS idx_orders_status_created
    ON orders (status, created_at)
    WHERE status = 'pending';

-- GigTimeoutWorker: WHERE status = 'broadcasting' AND created_at < NOW() - INTERVAL '5 minutes'
CREATE INDEX IF NOT EXISTS idx_deliveries_status_created
    ON deliveries (status, created_at)
    WHERE status = 'broadcasting';
