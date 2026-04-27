CREATE TABLE IF NOT EXISTS live_streams (
    id TEXT PRIMARY KEY,
    channel_id TEXT NOT NULL UNIQUE REFERENCES channels(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT 'Live Stream',
    description TEXT NOT NULL DEFAULT '',
    stream_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'offline',
    use_publisher_presence BOOLEAN NOT NULL DEFAULT FALSE,
    started_at TIMESTAMPTZ NULL,
    ended_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_live_streams_status_started_at
ON live_streams(status, started_at DESC);
