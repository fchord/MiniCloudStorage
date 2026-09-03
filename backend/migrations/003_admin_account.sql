-- Single-row administrator account. Password is stored as bcrypt.
-- TOTP secret is reversible (DB access is the trust boundary); never commit it.
CREATE TABLE IF NOT EXISTS admin_account (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    password_hash TEXT NOT NULL,
    totp_secret TEXT NOT NULL DEFAULT '',
    totp_pending TEXT NOT NULL DEFAULT '',
    totp_enrolled BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
