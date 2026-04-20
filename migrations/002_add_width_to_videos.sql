-- Add width column to videos table for 4K resolution detection
ALTER TABLE videos ADD COLUMN IF NOT EXISTS width INT DEFAULT 0;

-- Create index on width for queries filtering by resolution
CREATE INDEX IF NOT EXISTS idx_videos_width ON videos(width);
