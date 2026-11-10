-- Private, durable user-initiated model checks. Billing remains in the gateway.
CREATE TABLE IF NOT EXISTS user_intelligence_tests (
  id uuid PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id),
  api_key_id bigint NOT NULL REFERENCES api_keys(id),
  group_id bigint NOT NULL REFERENCES groups(id),
  model varchar(100) NOT NULL,
  kind varchar(20) NOT NULL,
  status varchar(20) NOT NULL DEFAULT 'running',
  result jsonb NOT NULL DEFAULT '{}',
  audit jsonb NOT NULL DEFAULT '[]',
  created_at timestamptz NOT NULL DEFAULT now(),
  deadline_at timestamptz NOT NULL,
  finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_user_intelligence_tests_owner ON user_intelligence_tests(user_id, api_key_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_intelligence_tests_running ON user_intelligence_tests(deadline_at) WHERE status='running';
