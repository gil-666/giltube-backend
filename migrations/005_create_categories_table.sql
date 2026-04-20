-- Create categories table
CREATE TABLE IF NOT EXISTS categories (
    id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    name VARCHAR(100) NOT NULL UNIQUE,
    slug VARCHAR(100) NOT NULL UNIQUE,
    description TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Create video_categories junction table for many-to-many relationship
CREATE TABLE IF NOT EXISTS video_categories (
    id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    video_id TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    category_id TEXT NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(video_id, category_id)
);

-- Add indexes for faster lookups
CREATE INDEX IF NOT EXISTS idx_video_categories_video_id ON video_categories(video_id);
CREATE INDEX IF NOT EXISTS idx_video_categories_category_id ON video_categories(category_id);

-- Insert default categories
INSERT INTO categories (name, slug, description) VALUES 
    ('Entertainment', 'entertainment', 'Entertainment content including talk shows, variety shows, and entertainment news'),
    ('Gaming', 'gaming', 'Video games, gameplay, streaming, reviews, and gaming content'),
    ('Music', 'music', 'Music videos, performances, covers, and audio content'),
    ('Movies', 'movies', 'Movie clips, trailers, reviews, and film content'),
    ('Education', 'education', 'Educational content, tutorials, courses, and learning materials'),
    ('Sports', 'sports', 'Sports events, highlights, analysis, and athletic content'),
    ('Technology', 'technology', 'Tech reviews, tutorials, gadgets, and tech news'),
    ('Vlogs', 'vlogs', 'Video blogs, daily vlogs, and personal content'),
    ('Comedy', 'comedy', 'Funny videos, stand-up, sketches, and comedic content'),
    ('News', 'news', 'News coverage, current events, and journalism'),
    ('Art & Design', 'art-design', 'Art tutorials, design showcases, and creative content'),
    ('Food', 'food', 'Cooking, recipes, food reviews, and culinary content'),
    ('Travel', 'travel', 'Travel vlogs, destination guides, and exploration'),
    ('Beauty', 'beauty', 'Beauty tutorials, makeup, skincare, and cosmetics'),
    ('Fitness', 'fitness', 'Workout videos, fitness tutorials, and health content'),
    ('DIY', 'diy', 'Do-it-yourself projects, crafts, and home improvement'),
    ('Other', 'other', 'Miscellaneous and uncategorized content')
ON CONFLICT (name) DO NOTHING;

-- Grant permissions to giltube user
GRANT ALL PRIVILEGES ON TABLE categories TO giltube;
GRANT ALL PRIVILEGES ON TABLE video_categories TO giltube;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO giltube;
