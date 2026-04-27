ALTER TABLE notifications
ADD COLUMN IF NOT EXISTS minute_bucket BIGINT;

UPDATE notifications
SET minute_bucket = (EXTRACT(EPOCH FROM created_at)::bigint / 60)
WHERE minute_bucket IS NULL;

ALTER TABLE notifications
ALTER COLUMN minute_bucket SET NOT NULL;

ALTER TABLE notifications
ALTER COLUMN minute_bucket SET DEFAULT (EXTRACT(EPOCH FROM NOW())::bigint / 60);

DROP INDEX IF EXISTS idx_notifications_dedupe_window;

CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_dedupe_window
ON notifications (
    recipient_user_id,
    actor_channel_id,
    type,
    COALESCE(related_video_id, ''),
    COALESCE(related_comment_id, ''),
    minute_bucket
);
