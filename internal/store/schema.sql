CREATE TABLE IF NOT EXISTS telemetry (
  source_stream text NOT NULL,
  event_id text NOT NULL,
  device_id text NOT NULL,
  source text NOT NULL DEFAULT 'unknown',
  observed_at timestamptz NOT NULL,
  cpu_percent double precision NOT NULL CHECK (cpu_percent BETWEEN 0 AND 100),
  memory_percent double precision NOT NULL CHECK (memory_percent BETWEEN 0 AND 100),
  packet_loss_pct double precision NOT NULL CHECK (packet_loss_pct BETWEEN 0 AND 100),
  ingested_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (source_stream, event_id)
);
CREATE INDEX IF NOT EXISTS telemetry_device_time ON telemetry (device_id, observed_at DESC);
CREATE TABLE IF NOT EXISTS incidents (
  id text PRIMARY KEY,
  device_id text NOT NULL,
  observed_at timestamptz NOT NULL,
  payload jsonb NOT NULL,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
  analysis_status text NOT NULL CHECK (analysis_status IN ('disabled','pending','running','complete','error')),
  analysis jsonb,
  attempts integer NOT NULL DEFAULT 0,
  next_attempt timestamptz NOT NULL DEFAULT now(),
  locked_until timestamptz,
  analysis_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS incidents_recent ON incidents (observed_at DESC);
CREATE INDEX IF NOT EXISTS incidents_analysis_queue ON incidents (analysis_status, next_attempt);
