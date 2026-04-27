CREATE TABLE IF NOT EXISTS comment_likes (
    id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    comment_id TEXT NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
    channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(comment_id, channel_id)
);

CREATE INDEX IF NOT EXISTS idx_comment_likes_comment_id
ON comment_likes(comment_id);

CREATE INDEX IF NOT EXISTS idx_comment_likes_channel_id
ON comment_likes(channel_id);
