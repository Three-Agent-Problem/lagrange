-- Usage reports (kind 'usage'): the latest one of an agent's session is looked up
-- for each new one, to record how much the session cost since.
CREATE INDEX activity_usage ON activity (recipient, context_id, seq) WHERE kind = 'usage';
