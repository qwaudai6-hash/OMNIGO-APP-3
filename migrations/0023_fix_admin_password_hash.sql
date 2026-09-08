-- Fix admin password hash: Argon2id -> bcrypt
-- The original seed used an Argon2id hash but auth service uses bcrypt.
-- Password: Omn!go@YSeoWRg5UwYB (bcrypt cost-12)

-- Upsert: create if not exists, update password if exists
INSERT INTO users (tracking_id, email, password_hash, full_name, phone, role, is_verified)
VALUES (
    'ADMN-0001',
    'admin@omnigo.pk',
    '$2a$12$YCKxJphDq4DN7dGSXh/JxeutOHt4hv4mVQZo/N6C4OJX9JhGypnly',
    'System Admin',
    '+923001234567',
    'admin',
    true
)
ON CONFLICT (tracking_id) DO UPDATE
SET password_hash = EXCLUDED.password_hash,
    email = EXCLUDED.email,
    role = 'admin',
    is_verified = true;

-- Also update by email in case tracking_id differs
UPDATE users
SET password_hash = '$2a$12$YCKxJphDq4DN7dGSXh/JxeutOHt4hv4mVQZo/N6C4OJX9JhGypnly'
WHERE email = 'admin@omnigo.pk';
