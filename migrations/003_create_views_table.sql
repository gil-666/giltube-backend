-- Create views table to track individual view events
CREATE TABLE IF NOT EXISTS views (
    id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    video_id TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    viewed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Add index on video_id for faster lookups and analytics queries
CREATE INDEX IF NOT EXISTS idx_views_video_id ON views(video_id);

-- Add composite index for video_id and viewed_at for analytics queries
CREATE INDEX IF NOT EXISTS idx_views_video_date ON views(video_id, viewed_at);
