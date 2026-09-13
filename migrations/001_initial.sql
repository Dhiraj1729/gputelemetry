CREATE TABLE gpus (
    uuid text PRIMARY KEY,
    hostname text NOT NULL,
    local_gpu_id text NOT NULL,
    device text NOT NULL,
    model_name text NOT NULL,
    first_seen timestamptz NOT NULL,
    last_seen timestamptz NOT NULL,
    last_event_id text NOT NULL
);
CREATE TABLE telemetry (
    event_id text PRIMARY KEY,
    gpu_uuid text NOT NULL REFERENCES gpus(uuid) DEFERRABLE INITIALLY DEFERRED,
    metric_name text NOT NULL,
    value double precision NOT NULL,
    processed_at timestamptz NOT NULL,
    source_timestamp timestamptz NOT NULL,
    ingested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    labels_raw text NOT NULL,
    container text NOT NULL,
    pod text NOT NULL,
    namespace text NOT NULL,
    event jsonb NOT NULL
);
CREATE INDEX telemetry_gpu_time_idx ON telemetry (gpu_uuid, processed_at, event_id);
