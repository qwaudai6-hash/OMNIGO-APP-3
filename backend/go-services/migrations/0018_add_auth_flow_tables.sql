-- ════════════════════════════════════════════════════════════════
--  OMNIGO Platform — Migration 0018
--  Adds three new auth-flow tables:
--    1. password_reset_tokens — for forgot-password flow
--    2. email_verification_tokens — for signup verification
--    3. user_2fa_secrets — for TOTP-based two-factor auth
--  All three tables are keyed by user_tracking_id and have TTLs so
--  expired tokens are auto-cleaned by a periodic job.
-- ════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS password_reset_tokens (
    id              BIGSERIAL PRIMARY KEY,
    user_tracking_id  VARCHAR(50) NOT NULL,
    token_hash        VARCHAR(128) NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    used_at           TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (token_hash)
);
CREATE INDEX IF NOT EXISTS idx_pwd_reset_user ON password_reset_tokens(user_tracking_id);
CREATE INDEX IF NOT EXISTS idx_pwd_reset_expires ON password_reset_tokens(expires_at);

CREATE TABLE IF NOT EXISTS email_verification_tokens (
    id              BIGSERIAL PRIMARY KEY,
    user_tracking_id  VARCHAR(50) NOT NULL,
    email             VARCHAR(255) NOT NULL,
    token_hash        VARCHAR(128) NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    verified_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (token_hash)
);
CREATE INDEX IF NOT EXISTS idx_email_verify_user ON email_verification_tokens(user_tracking_id);
CREATE INDEX IF NOT EXISTS idx_email_verify_expires ON email_verification_tokens(expires_at);

CREATE TABLE IF NOT EXISTS user_2fa_secrets (
    user_tracking_id  VARCHAR(50) PRIMARY KEY,
    secret_encrypted  TEXT NOT NULL,
    enabled           BOOLEAN NOT NULL DEFAULT false,
    enrolled_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at       TIMESTAMPTZ
);

-- Add email_verified column to users (default false; flips to true once
-- the verification token is consumed).
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS email_verified BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS two_factor_enabled BOOLEAN NOT NULL DEFAULT false;

-- Note: existing users are grandfathered as email_verified=true so the
-- new constraint doesn't break old login flow until they reset their
-- password (which forces a fresh verification email).
UPDATE users SET email_verified = true WHERE email_verified = false AND created_at < NOW() - INTERVAL '7 days';
