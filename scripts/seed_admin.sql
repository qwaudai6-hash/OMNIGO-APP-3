-- Seed script: Create default admin user
-- Run: psql -U omnigo_user -d omnigo_db -f scripts/seed_admin.sql

INSERT INTO users (tracking_id, email, password_hash, full_name, phone, role, is_verified)
VALUES (
    'ADMN-0001',
    'admin@omnigo.pk',
    -- password: Omn!go@YSeoWRg5UwYB (bcrypt cost-12)
    '$2a$12$YCKxJphDq4DN7dGSXh/JxeutOHt4hv4mVQZo/N6C4OJX9JhGypnly',
    'System Admin',
    '+923001234567',
    'admin',
    true
)
ON CONFLICT (tracking_id) DO NOTHING;

-- Update existing admin password in case it was seeded with wrong hash
UPDATE users
SET password_hash = '$2a$12$YCKxJphDq4DN7dGSXh/JxeutOHt4hv4mVQZo/N6C4OJX9JhGypnly'
WHERE email = 'admin@omnigo.pk' AND role = 'admin';
