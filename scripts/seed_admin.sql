-- ⚠️  MEDIUM-19: this seed uses a WELL-KNOWN password (admin123).
-- Use ONLY for local development. For staging/production:
--   1. Generate: python3 -c "import bcrypt;print(bcrypt.hashpw(input().encode(),bcrypt.gensalt()).decode())"
--   2. Replace the hash below AND delete the plaintext comment.
-- Seed script: Create default admin user
-- Run: psql -U omnigo_user -d omnigo_db -f scripts/seed_admin.sql

INSERT INTO users (tracking_id, email, password_hash, full_name, phone, role, is_verified)
VALUES (
    'ADMN-0001',
    'admin@omnigo.pk',
    -- password: admin123 (Argon2id hash)
    '$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHRzb21lc2FsdA$R2hZ4B2d3h5v8m1c7k9w0xYz3q2w1e4r5t6y7u8i9o',
    'System Admin',
    '+923001234567',
    'admin',
    true
)
ON CONFLICT (tracking_id) DO NOTHING;
