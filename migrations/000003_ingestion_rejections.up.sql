CREATE TABLE ingestion_rejections (
    stream TEXT NOT NULL,
    message_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    payload TEXT,
    rejected_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (stream, message_id)
);

