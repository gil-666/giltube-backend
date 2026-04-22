-- Add user_type column to users table
ALTER TABLE users ADD COLUMN user_type VARCHAR(50) DEFAULT 'user' NOT NULL;

-- Create index on user_type for faster queries
CREATE INDEX idx_users_type ON users(user_type);
