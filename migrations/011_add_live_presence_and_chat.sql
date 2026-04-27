ALTER TABLE live_streams
ADD COLUMN IF NOT EXISTS use_publisher_presence BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS live_chat_messages (
    id TEXT PRIMARY KEY,
    live_stream_id TEXT NOT NULL REFERENCES live_streams(id) ON DELETE CASCADE,
    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    message TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_live_chat_messages_stream_created_at
ON live_chat_messages(live_stream_id, created_at DESC);
