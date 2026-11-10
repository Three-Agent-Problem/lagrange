-- Telegram: a person's own bot, which tells them what waits on them and lets them
-- answer with buttons. One bot per person, paired to one private chat.
CREATE TABLE telegram_bots (
    user_id         bigint PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    token           text NOT NULL,
    bot_id          bigint NOT NULL UNIQUE,
    bot_username    text NOT NULL,
    -- The paired chat and Telegram user; NULL until pairing.
    chat_id         bigint,
    tg_user_id      bigint,
    tg_username     text NOT NULL DEFAULT '',
    pair_code_hash  text NOT NULL DEFAULT '',
    pair_expires_at timestamptz,
    pair_attempts   int NOT NULL DEFAULT 0,
    -- What to send: 'questions' (only what waits on them) or 'milestones' (also run
    -- reports and finished runs).
    level           text NOT NULL DEFAULT 'milestones' CHECK (level IN ('questions', 'milestones')),
    muted_until     timestamptz,
    update_offset   bigint NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Every message a bot sent. A message about a waiting task stays open (its buttons
-- work) until the task is answered, here or anywhere else; closing it is the one claim
-- that lets an answer through.
CREATE TABLE telegram_messages (
    id         bigserial PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    chat_id    bigint NOT NULL,
    message_id bigint NOT NULL,
    task_id    text NOT NULL DEFAULT '',
    kind       text NOT NULL,
    open       boolean NOT NULL DEFAULT false,
    body       text NOT NULL DEFAULT '',
    -- The questions of a form (lagrange/ask), and the picks so far, per question.
    questions  jsonb NOT NULL DEFAULT '[]',
    picks      jsonb NOT NULL DEFAULT '[]',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX telegram_messages_open ON telegram_messages (task_id) WHERE open;
CREATE UNIQUE INDEX telegram_messages_run ON telegram_messages (user_id, task_id) WHERE kind = 'run';

-- A button's callback data is a random token: it names a row here, never a task.
CREATE TABLE telegram_buttons (
    token      text PRIMARY KEY,
    message    bigint NOT NULL REFERENCES telegram_messages (id) ON DELETE CASCADE,
    action     text NOT NULL,
    question   int NOT NULL DEFAULT 0,
    option     int NOT NULL DEFAULT 0,
    expires_at timestamptz NOT NULL
);
CREATE INDEX telegram_buttons_message ON telegram_buttons (message);

-- How far into the activity log Telegram has looked (answered elsewhere, finished runs,
-- report notices). It starts at the end, so connecting a bot doesn't replay history.
CREATE TABLE telegram_cursor (
    id  int PRIMARY KEY CHECK (id = 1),
    seq bigint NOT NULL
);
INSERT INTO telegram_cursor (id, seq) SELECT 1, coalesce(max(seq), 0) FROM activity;
