-- Add status column to users table
ALTER TABLE users ADD COLUMN status VARCHAR(50) DEFAULT 'active' NOT NULL;
CREATE INDEX idx_users_status ON users(status);

-- Add status column to channels table
ALTER TABLE channels ADD COLUMN status VARCHAR(50) DEFAULT 'active' NOT NULL;
CREATE INDEX idx_channels_status ON channels(status);

-- Add hidden column to videos table
ALTER TABLE videos ADD COLUMN hidden BOOLEAN DEFAULT false NOT NULL;
CREATE INDEX idx_videos_hidden ON videos(hidden);
