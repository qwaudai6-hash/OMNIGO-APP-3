-- Migration: 0051_return_requests.sql (rollback)
-- Description: Drops return_requests table and removes return_deadline from orders.

DROP TABLE IF EXISTS return_requests;

ALTER TABLE orders DROP COLUMN IF EXISTS return_deadline;
