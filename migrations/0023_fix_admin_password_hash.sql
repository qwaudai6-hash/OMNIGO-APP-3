-- Fix admin password hash: Argon2id -> bcrypt
-- The original seed used an Argon2id hash but auth service uses bcrypt.
-- This migration updates the admin password to a valid bcrypt hash.
-- Password: Omn!go@YSeoWRg5UwYB (bcrypt cost-12)

UPDATE users
SET password_hash = '$2a$12$YCKxJphDq4DN7dGSXh/JxeutOHt4hv4mVQZo/N6C4OJX9JhGypnly'
WHERE email = 'admin@omnigo.pk' AND role = 'admin';
