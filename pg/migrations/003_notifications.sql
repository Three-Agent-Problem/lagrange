-- How each person wants to hear that a task waits on them. No row means the defaults:
-- email on (when the server can send email), no Slack.
CREATE TABLE notification_settings (
    user_id       bigint PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    email         boolean NOT NULL DEFAULT true,
    slack_webhook text NOT NULL DEFAULT '',
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- One-time links in notifications: whoever opens one can answer that one task as the
-- person it was sent to, once, without logging in.
CREATE TABLE decision_links (
    token_hash text PRIMARY KEY,
    task_id    text NOT NULL,
    handle     text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);
CREATE INDEX decision_links_task ON decision_links (task_id);
