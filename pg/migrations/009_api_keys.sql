-- Personal API keys: a key acts as its person in one workspace, with at most role.
CREATE TABLE api_keys (
    id           bigserial PRIMARY KEY,
    user_id      bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id bigint NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text NOT NULL,
    prefix       text NOT NULL,
    key_hash     text NOT NULL UNIQUE,
    role         text NOT NULL CHECK (role IN ('viewer', 'member', 'admin', 'owner')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz
);
CREATE INDEX api_keys_user ON api_keys (user_id);

-- One-time codes from `lagrange login`: the browser approves, the CLI trades the code
-- (with its PKCE verifier) for a key.
CREATE TABLE cli_logins (
    code_hash    text PRIMARY KEY,
    challenge    text NOT NULL,
    user_id      bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id bigint NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    role         text NOT NULL,
    name         text NOT NULL,
    expires_at   timestamptz NOT NULL
);
