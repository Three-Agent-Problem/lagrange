-- People who log in to the web app. Every user also owns a human address (handle).
CREATE TABLE users (
    id            bigserial PRIMARY KEY,
    email         text NOT NULL UNIQUE,
    name          text NOT NULL,
    handle        text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    admin         boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    token_hash text PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Every agent and every person has an address. Agents are reached through a tunnel
-- (endpoint = '') or directly at a public endpoint.
CREATE TABLE addresses (
    handle       text PRIMARY KEY,
    kind         text NOT NULL CHECK (kind IN ('agent', 'human')),
    owner_id     bigint REFERENCES users (id) ON DELETE SET NULL,
    display_name text NOT NULL DEFAULT '',
    description  text NOT NULL DEFAULT '',
    endpoint     text NOT NULL DEFAULT '',
    card         jsonb,
    lint_grade   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Bearer tokens that let an agent connect a tunnel and send as its address.
CREATE TABLE tokens (
    token_hash   text PRIMARY KEY,
    handle       text NOT NULL REFERENCES addresses (handle) ON DELETE CASCADE,
    label        text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz
);

-- a2a-go taskstore.Store. version is the optimistic concurrency token.
CREATE TABLE tasks (
    id         text PRIMARY KEY,
    tenant     text NOT NULL DEFAULT '',
    owner      text NOT NULL DEFAULT '',
    context_id text NOT NULL,
    state      text NOT NULL,
    version    bigint NOT NULL,
    task       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tasks_owner_updated ON tasks (owner, updated_at DESC, id DESC);
CREATE INDEX tasks_tenant_state ON tasks (tenant, state);

-- Events written with each task update; a2a-go's pull event queue reads them so that
-- subscribers on any replica see the task progress.
CREATE TABLE task_events (
    id         bigserial PRIMARY KEY,
    task_id    text NOT NULL,
    version    bigint NOT NULL,
    message    jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX task_events_task ON task_events (task_id, id);

CREATE TABLE push_configs (
    task_id text NOT NULL,
    id      text NOT NULL,
    config  jsonb NOT NULL,
    PRIMARY KEY (task_id, id)
);

-- a2a-go work queue (cluster mode). A worker holds an item while lease_until is in the
-- future and extends it with heartbeats; an expired lease makes the item claimable again.
CREATE TABLE work_items (
    id          bigserial PRIMARY KEY,
    task_id     text NOT NULL,
    kind        text NOT NULL,
    state       text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'working', 'done', 'failed')),
    attempts    int NOT NULL DEFAULT 0,
    worker      text NOT NULL DEFAULT '',
    payload     jsonb NOT NULL,
    cause       text NOT NULL DEFAULT '',
    lease_until timestamptz NOT NULL DEFAULT now(),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX work_items_ready ON work_items (lease_until) WHERE state IN ('pending', 'working');
CREATE UNIQUE INDEX work_items_one_execution ON work_items (task_id)
    WHERE kind = 'execute' AND state IN ('pending', 'working');

-- What the relay knows about a platform task: who sent it, who it is for, the task it
-- delegated from, and the agent's own task and context IDs behind the tunnel.
CREATE TABLE task_links (
    task_id           text PRIMARY KEY,
    sender            text NOT NULL DEFAULT '',
    recipient         text NOT NULL DEFAULT '',
    parent_task_id    text NOT NULL DEFAULT '',
    skill             text NOT NULL DEFAULT '',
    remote_task_id    text NOT NULL DEFAULT '',
    remote_context_id text NOT NULL DEFAULT ''
);

-- The platform's activity log: what the web app's graph, inbox and replay are built from.
CREATE TABLE activity (
    seq            bigserial PRIMARY KEY,
    at             timestamptz NOT NULL DEFAULT now(),
    kind           text NOT NULL,
    task_id        text NOT NULL DEFAULT '',
    parent_task_id text NOT NULL DEFAULT '',
    context_id     text NOT NULL DEFAULT '',
    sender         text NOT NULL DEFAULT '',
    recipient      text NOT NULL DEFAULT '',
    state          text NOT NULL DEFAULT '',
    summary        text NOT NULL DEFAULT '',
    data           jsonb
);
CREATE INDEX activity_task ON activity (task_id, seq);
