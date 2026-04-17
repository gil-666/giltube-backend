-- Create likes table
CREATE TABLE IF NOT EXISTS likes (
    id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    video_id TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    channel_id TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(video_id, channel_id)
);

-- Add index on video_id for faster lookups
CREATE INDEX IF NOT EXISTS idx_likes_video_id ON likes(video_id);

-- Add index on channel_id for faster lookups
CREATE INDEX IF NOT EXISTS idx_likes_channel_id ON likes(channel_id);

-- Add likes column to videos table if it doesn't exist
ALTER TABLE videos ADD COLUMN IF NOT EXISTS likes INT DEFAULT 0;
