-- Migration: add Vendor Auto-Approval fields

ALTER TABLE users
ADD COLUMN IF NOT EXISTS entity_type VARCHAR(20),
ADD COLUMN IF NOT EXISTS ntn_number VARCHAR(50),
ADD COLUMN IF NOT EXISTS cnic_back_url TEXT;
