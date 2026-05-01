CREATE TABLE IF NOT EXISTS notifications (
    id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    recipient_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    actor_channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    type TEXT NOT NULL CHECK (type IN ('comment_video', 'reply_comment', 'like_video', 'like_comment', 'live_started')),
    related_video_id TEXT NULL REFERENCES videos(id) ON DELETE CASCADE,
    related_comment_id TEXT NULL REFERENCES comments(id) ON DELETE CASCADE,
    minute_bucket BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::bigint / 60),
    is_read BOOLEAN NOT NULL DEFAULT FALSE,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_notifications_recipient_created_at
ON notifications(recipient_user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_notifications_recipient_is_read
ON notifications(recipient_user_id, is_read);

CREATE INDEX IF NOT EXISTS idx_notifications_related_video
ON notifications(related_video_id);

CREATE INDEX IF NOT EXISTS idx_notifications_related_comment
ON notifications(related_comment_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_dedupe_window
ON notifications (
    recipient_user_id,
    actor_channel_id,
    type,
    COALESCE(related_video_id, ''),
    COALESCE(related_comment_id, ''),
    minute_bucket
);
