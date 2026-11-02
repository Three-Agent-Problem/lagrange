-- Outbound webhooks: a workspace's endpoints and the events each wants.
CREATE TABLE webhooks (
    id           bigserial PRIMARY KEY,
    workspace_id bigint NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    url          text NOT NULL,
    description  text NOT NULL DEFAULT '',
    events       text[] NOT NULL,
    secret       text NOT NULL,
    active       boolean NOT NULL DEFAULT true,
    created_by   bigint REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhooks_workspace ON webhooks (workspace_id);

-- One row per event per webhook: the outbox the sender works through, and the log
-- Settings shows. The payload is fixed when the event happens, so retries send the same
-- bytes.
CREATE TABLE webhook_deliveries (
    id           bigserial PRIMARY KEY,
    webhook_id   bigint NOT NULL REFERENCES webhooks (id) ON DELETE CASCADE,
    event_id     text NOT NULL,
    event_type   text NOT NULL,
    payload      jsonb NOT NULL,
    status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'failed')),
    attempts     int NOT NULL DEFAULT 0,
    next_at      timestamptz NOT NULL DEFAULT now(),
    last_status  int,
    last_error   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    UNIQUE (webhook_id, event_id)
);
CREATE INDEX webhook_deliveries_due ON webhook_deliveries (next_at) WHERE status = 'pending';
CREATE INDEX webhook_deliveries_hook ON webhook_deliveries (webhook_id, id DESC);

-- How far into the activity log events have been fanned out. It starts at the end, so
-- turning webhooks on doesn't replay history.
CREATE TABLE webhook_cursor (
    id  int PRIMARY KEY CHECK (id = 1),
    seq bigint NOT NULL
);
INSERT INTO webhook_cursor (id, seq) SELECT 1, coalesce(max(seq), 0) FROM activity;

-- Whether each agent was last seen online, so address.online/offline fire on changes only.
CREATE TABLE webhook_presence (
    handle text PRIMARY KEY,
    online boolean NOT NULL
);
