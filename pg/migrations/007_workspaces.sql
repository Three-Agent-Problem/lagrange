-- Workspaces keep teams apart. Each agent address belongs to one workspace; people are
-- members of any number of workspaces, with a role. A workspace sees the traffic of its
-- addresses: its agents and its members' own addresses.
CREATE TABLE workspaces (
    id         bigserial PRIMARY KEY,
    slug       text NOT NULL UNIQUE,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE members (
    workspace_id bigint NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         text NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX members_user ON members (user_id, created_at);

-- Everything so far was one team: it becomes the default workspace.
INSERT INTO workspaces (slug, name) VALUES ('default', 'Default');
INSERT INTO members (workspace_id, user_id, role)
    SELECT w.id, u.id, CASE WHEN u.admin THEN 'owner' ELSE 'member' END FROM workspaces w, users u WHERE w.slug = 'default';

-- People's own addresses have no workspace: they belong to whoever they are members of.
ALTER TABLE addresses ADD COLUMN workspace_id bigint REFERENCES workspaces (id) ON DELETE CASCADE;
UPDATE addresses SET workspace_id = (SELECT id FROM workspaces WHERE slug = 'default') WHERE kind = 'agent';
CREATE INDEX addresses_workspace ON addresses (workspace_id);

-- An invitation joins a workspace with a role.
ALTER TABLE auth_links ADD COLUMN workspace_id bigint REFERENCES workspaces (id) ON DELETE CASCADE;
ALTER TABLE auth_links ADD COLUMN role text NOT NULL DEFAULT '';

-- Each workspace has its own waiting rules.
ALTER TABLE wait_policy ADD COLUMN workspace_id bigint REFERENCES workspaces (id) ON DELETE CASCADE;
UPDATE wait_policy SET workspace_id = (SELECT id FROM workspaces WHERE slug = 'default');
-- Dropping id drops its primary key and its id = 1 check.
ALTER TABLE wait_policy DROP COLUMN id;
ALTER TABLE wait_policy ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE wait_policy ADD PRIMARY KEY (workspace_id);

-- Hiding a run is per workspace too.
ALTER TABLE archived_runs ADD COLUMN workspace_id bigint REFERENCES workspaces (id) ON DELETE CASCADE;
UPDATE archived_runs SET workspace_id = (SELECT id FROM workspaces WHERE slug = 'default');
ALTER TABLE archived_runs DROP CONSTRAINT archived_runs_pkey;
ALTER TABLE archived_runs ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE archived_runs ADD PRIMARY KEY (workspace_id, run_id);
