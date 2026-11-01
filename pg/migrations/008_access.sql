-- Who may start a task with an address: anyone (public), its workspace, or its workspace
-- and the workspaces it accepted a connection from. Addresses from before keep working
-- as they did: public.
ALTER TABLE addresses ADD COLUMN access text NOT NULL DEFAULT 'public' CHECK (access IN ('public', 'workspace', 'connections'));

-- A workspace asking to reach an address of another one, and the answer. Blocked
-- requests stay, so the same workspace can't ask again.
CREATE TABLE connections (
    id             bigserial PRIMARY KEY,
    from_workspace bigint NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    to_handle      text NOT NULL REFERENCES addresses (handle) ON DELETE CASCADE ON UPDATE CASCADE,
    status         text NOT NULL CHECK (status IN ('pending', 'accepted', 'declined', 'blocked')),
    note           text NOT NULL DEFAULT '',
    rate_per_min   int NOT NULL DEFAULT 60 CHECK (rate_per_min > 0),
    requested_by   bigint REFERENCES users (id) ON DELETE SET NULL,
    decided_by     bigint REFERENCES users (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    decided_at     timestamptz,
    UNIQUE (from_workspace, to_handle)
);
CREATE INDEX connections_to ON connections (to_handle);
