-- Runs someone hid from the runs list (junk test runs). The ID is the root task's ID or
-- msg-<seq>, as the runs API names runs.
CREATE TABLE archived_runs (
    run_id      text PRIMARY KEY,
    archived_by text NOT NULL,
    archived_at timestamptz NOT NULL DEFAULT now()
);
