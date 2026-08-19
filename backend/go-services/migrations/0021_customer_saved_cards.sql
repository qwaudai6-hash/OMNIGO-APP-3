-- ════════════════════════════════════════════════════════════════
--  OMNIGO Platform — Migration 0021
--  Creates customer_saved_cards table for PCI-compliant 1-click tokenized checkout.
--  Stores ZERO Primary Account Numbers (PAN) and ZERO CVVs.
-- ════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS customer_saved_cards (
    id                   BIGSERIAL PRIMARY KEY,
    card_id              VARCHAR(100) UNIQUE NOT NULL, -- e.g. card_uuid
    customer_tracking_id VARCHAR(50) NOT NULL,
    gateway              VARCHAR(30) NOT NULL DEFAULT 'payfast',
    instrument_token     VARCHAR(255) NOT NULL,
    card_brand           VARCHAR(30) NOT NULL,         -- visa, mastercard, paypak, unionpay
    last_four            VARCHAR(4) NOT NULL,          -- e.g. '4242'
    expiry_month         VARCHAR(2) NOT NULL,
    expiry_year          VARCHAR(4) NOT NULL,
    cardholder_name      VARCHAR(100),
    is_default           BOOLEAN NOT NULL DEFAULT false,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_saved_cards_customer ON customer_saved_cards(customer_tracking_id);
CREATE INDEX IF NOT EXISTS idx_saved_cards_token ON customer_saved_cards(instrument_token);
