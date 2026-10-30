-- Every HTTP exchange the platform carries: A2A calls coming in at /a/<handle>, the
-- relay's calls out to agents, and pushes and webhooks through /t/<handle>. Bodies are
-- capped and only an allowlist of headers is kept, so tokens are never stored.
CREATE TABLE exchanges (
    id             bigserial PRIMARY KEY,
    at             timestamptz NOT NULL,
    direction      text NOT NULL,
    task_id        text NOT NULL DEFAULT '',
    parent_task_id text NOT NULL DEFAULT '',
    context_id     text NOT NULL DEFAULT '',
    sender         text NOT NULL DEFAULT '',
    recipient      text NOT NULL DEFAULT '',
    method         text NOT NULL,
    url            text NOT NULL,
    status         int NOT NULL DEFAULT 0,
    duration_ms    bigint NOT NULL DEFAULT 0,
    headers        jsonb,
    request        text NOT NULL DEFAULT '',
    response       text NOT NULL DEFAULT '',
    truncated      boolean NOT NULL DEFAULT false,
    error          text NOT NULL DEFAULT ''
);
CREATE INDEX exchanges_task ON exchanges (task_id);
CREATE INDEX exchanges_parent ON exchanges (parent_task_id) WHERE parent_task_id <> '';
CREATE INDEX exchanges_context ON exchanges (context_id) WHERE context_id <> '';
CREATE INDEX activity_parent ON activity (parent_task_id) WHERE parent_task_id <> '';
