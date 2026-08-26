-- Migration: 0022_add_returned_order_status.sql
-- Description: Updates the chk_orders_status constraint on the orders table
-- to include 'returned' and 'paid' states.

-- Drop existing constraint
ALTER TABLE orders DROP CONSTRAINT IF EXISTS chk_orders_status;

-- Add updated constraint with all valid state machine statuses
ALTER TABLE orders
    ADD CONSTRAINT chk_orders_status CHECK (
        status IN (
            'pending',
            'paid',
            'accepted',
            'shipped',
            'in_transit',
            'delivered',
            'completed',
            'cancelled',
            'failed',
            'refunded',
            'returned'
        )
    );
