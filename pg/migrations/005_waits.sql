-- The team's rules for tasks waiting on someone (one row, id 1). A NULL delay is off.
CREATE TABLE wait_policy (
    id               int PRIMARY KEY CHECK (id = 1),
    remind_after_s   bigint,
    escalate_after_s bigint,
    escalate_to      text NOT NULL DEFAULT '',
    cancel_after_s   bigint,
    updated_by       text NOT NULL DEFAULT '',
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- Each task waiting on someone: since when, and what the policy's timers did since then.
-- A task that asks again starts over.
CREATE TABLE waits (
    task_id      text PRIMARY KEY,
    who          text NOT NULL,
    asker        text NOT NULL,
    since        timestamptz NOT NULL,
    reminders    int NOT NULL DEFAULT 0,
    reminded_at  timestamptz,
    escalated_to text NOT NULL DEFAULT '',
    escalated_at timestamptz
);
