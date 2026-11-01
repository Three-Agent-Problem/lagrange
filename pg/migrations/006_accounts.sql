-- Self-serve accounts: people sign up, verify their email, log in with a link or with
-- GitHub/Google, and reset their password. Someone who signs in only with a link or a
-- provider has no password.
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;
ALTER TABLE users ADD COLUMN email_verified_at timestamptz;
-- Users so far were created by an admin, who vouched for their email.
UPDATE users SET email_verified_at = created_at, email = lower(email);

-- Accounts at OAuth providers that log a person in.
CREATE TABLE identities (
    provider   text NOT NULL,
    subject    text NOT NULL,
    user_id    bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    email      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject)
);
CREATE INDEX identities_user ON identities (user_id);

-- One-time links sent by email: confirm the address, log in, reset the password, or
-- accept an invitation. Only the token's hash is stored.
CREATE TABLE auth_links (
    token_hash text PRIMARY KEY,
    purpose    text NOT NULL CHECK (purpose IN ('verify', 'login', 'reset', 'invite')),
    email      text NOT NULL,
    user_id    bigint REFERENCES users (id) ON DELETE CASCADE,
    invited_by bigint REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);
CREATE INDEX auth_links_email ON auth_links (email, purpose);
