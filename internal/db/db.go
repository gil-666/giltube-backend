package db

import (
	"database/sql"
	"log"

	_ "github.com/lib/pq"
)

func Connect(databaseURL string) *sql.DB {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		log.Fatal(err)
	}

	if err := db.Ping(); err != nil {
		log.Fatal(err)
	}

	log.Println("Connected to database")
	return db
}

func UpdateVideoStatus(db *sql.DB, videoID string, status string) error {
	_, err := db.Exec(
		"UPDATE videos SET status = $1 WHERE id = $2",
		status,
		videoID,
	)
	return err
}

func UpdateVideoProgress(db *sql.DB, videoID string, progress int) error {
	_, err := db.Exec(
		"UPDATE videos SET progress = $1 WHERE id = $2",
		progress,
		videoID,
	)
	return err
}

func IncrementVideoViews(db *sql.DB, videoID string) error {
	_, err := db.Exec(
		"UPDATE videos SET views = views + 1 WHERE id = $1",
		videoID,
	)
	return err
}

func GetComments(db *sql.DB, videoID string) ([]map[string]interface{}, error) {
	rows, err := db.Query(`
		SELECT 
			c.id,
			c.text,
			c.created_at,
			c.parent_comment_id,
			ch.id,
			ch.name,
			COALESCE(ch.avatar_url, ''),
			COALESCE(ch.verified, false)
		FROM comments c
		JOIN channels ch ON c.channel_id = ch.id
		WHERE c.video_id = $1
		ORDER BY c.created_at ASC
	`, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type commentRow struct {
		ID              string
		Text            string
		CreatedAt       string
		ParentCommentID sql.NullString
		ChannelID       string
		ChannelName     string
		ChannelAvatar   string
		ChannelVerified bool
	}

	var rowsData []commentRow
	for rows.Next() {
		var row commentRow
		err := rows.Scan(
			&row.ID,
			&row.Text,
			&row.CreatedAt,
			&row.ParentCommentID,
			&row.ChannelID,
			&row.ChannelName,
			&row.ChannelAvatar,
			&row.ChannelVerified,
		)
		if err != nil {
			continue
		}
		rowsData = append(rowsData, row)
	}

	nodes := make(map[string]map[string]interface{}, len(rowsData))
	roots := make([]map[string]interface{}, 0)

	for _, row := range rowsData {
		var parent interface{}
		if row.ParentCommentID.Valid {
			parent = row.ParentCommentID.String
		}

		nodes[row.ID] = map[string]interface{}{
			"id":                row.ID,
			"text":              row.Text,
			"created_at":        row.CreatedAt,
			"parent_comment_id": parent,
			"channel": map[string]interface{}{
				"id":         row.ChannelID,
				"name":       row.ChannelName,
				"avatar_url": row.ChannelAvatar,
				"verified":   row.ChannelVerified,
			},
			"replies": []map[string]interface{}{},
		}
	}

	for _, row := range rowsData {
		node := nodes[row.ID]
		if row.ParentCommentID.Valid {
			if parent, ok := nodes[row.ParentCommentID.String]; ok {
				replies := parent["replies"].([]map[string]interface{})
				parent["replies"] = append(replies, node)
				continue
			}
		}
		roots = append(roots, node)
	}

	return roots, nil
}

func CreateComment(db *sql.DB, commentID, videoID, channelID, text string, parentCommentID *string) error {
	_, err := db.Exec(
		"INSERT INTO comments (id, video_id, channel_id, text, parent_comment_id, created_at) VALUES ($1, $2, $3, $4, $5, NOW())",
		commentID,
		videoID,
		channelID,
		text,
		parentCommentID,
	)
	return err
}

func DeleteComment(db *sql.DB, commentID string) error {
	_, err := db.Exec(
		"DELETE FROM comments WHERE id = $1",
		commentID,
	)
	return err
}

func CreateLike(db *sql.DB, likeID, videoID, channelID string) error {
	_, err := db.Exec(
		"INSERT INTO likes (id, video_id, channel_id, created_at) VALUES ($1, $2, $3, NOW())",
		likeID,
		videoID,
		channelID,
	)
	return err
}

func DeleteLike(db *sql.DB, videoID, channelID string) error {
	_, err := db.Exec(
		"DELETE FROM likes WHERE video_id = $1 AND channel_id = $2",
		videoID,
		channelID,
	)
	return err
}

func CheckIfLiked(db *sql.DB, videoID, channelID string) (bool, error) {
	var count int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM likes WHERE video_id = $1 AND channel_id = $2",
		videoID,
		channelID,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func GetLikesCount(db *sql.DB, videoID string) (int, error) {
	var count int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM likes WHERE video_id = $1",
		videoID,
	).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetChannelAnalytics returns aggregate views and likes data for all videos in a channel
func GetChannelAnalytics(db *sql.DB, channelID string) (map[string]interface{}, error) {
	// Get total views and likes
	var totalViews int
	var totalLikes int

	err := db.QueryRow(`
		SELECT 
			COALESCE(SUM(views), 0),
			(SELECT COUNT(*) FROM likes l JOIN videos v ON l.video_id = v.id WHERE v.channel_id = $1)
		FROM videos
		WHERE channel_id = $1
	`, channelID).Scan(&totalViews, &totalLikes)

	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"channel_id": channelID,
		"total_views": totalViews,
		"total_likes": totalLikes,
	}, nil
}

// GetVideoCategories retrieves all categories for a video
func GetVideoCategories(db *sql.DB, videoID string) ([]map[string]interface{}, error) {
	rows, err := db.Query(`
		SELECT c.id, c.name, c.slug, c.description, c.created_at
		FROM categories c
		JOIN video_categories vc ON c.id = vc.category_id
		WHERE vc.video_id = $1
		ORDER BY c.name
	`, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []map[string]interface{}
	for rows.Next() {
		var id, name, slug, description string
		var createdAt string
		if err := rows.Scan(&id, &name, &slug, &description, &createdAt); err != nil {
			continue
		}
		categories = append(categories, map[string]interface{}{
			"id":          id,
			"name":        name,
			"slug":        slug,
			"description": description,
			"created_at":  createdAt,
		})
	}
	return categories, nil
}

// GetAllCategories retrieves all available categories (for dropdowns/uploads)
func GetAllCategories(db *sql.DB) ([]map[string]interface{}, error) {
	rows, err := db.Query(`
		SELECT id, name, slug, description, created_at
		FROM categories
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []map[string]interface{}
	for rows.Next() {
		var id, name, slug, description string
		var createdAt string
		if err := rows.Scan(&id, &name, &slug, &description, &createdAt); err != nil {
			continue
		}
		categories = append(categories, map[string]interface{}{
			"id":          id,
			"name":        name,
			"slug":        slug,
			"description": description,
			"created_at":  createdAt,
		})
	}
	return categories, nil
}

// GetCategoriesWithVideos retrieves only categories that have videos (for sidebar filtering)
func GetCategoriesWithVideos(db *sql.DB) ([]map[string]interface{}, error) {
	rows, err := db.Query(`
		SELECT DISTINCT c.id, c.name, c.slug, c.description, c.created_at, COUNT(v.id) as video_count
		FROM categories c
		LEFT JOIN video_categories vc ON c.id = vc.category_id
		LEFT JOIN videos v ON vc.video_id = v.id AND v.status = 'ready'
		GROUP BY c.id, c.name, c.slug, c.description, c.created_at
		HAVING COUNT(v.id) > 0
		ORDER BY c.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []map[string]interface{}
	for rows.Next() {
		var id, name, slug, description string
		var createdAt string
		var videoCount int
		if err := rows.Scan(&id, &name, &slug, &description, &createdAt, &videoCount); err != nil {
			continue
		}
		categories = append(categories, map[string]interface{}{
			"id":          id,
			"name":        name,
			"slug":        slug,
			"description": description,
			"created_at":  createdAt,
			"video_count": videoCount,
		})
	}
	return categories, nil
}

// AssignCategoriesToVideo assigns categories to a video
func AssignCategoriesToVideo(db *sql.DB, videoID string, categoryIDs []string) error {
	// First, delete existing categories for this video
	_, err := db.Exec("DELETE FROM video_categories WHERE video_id = $1", videoID)
	if err != nil {
		return err
	}

	// Insert new categories
	for _, categoryID := range categoryIDs {
		_, err := db.Exec(
			"INSERT INTO video_categories (id, video_id, category_id, created_at) VALUES (gen_random_uuid()::text, $1, $2, NOW())",
			videoID,
			categoryID,
		)
		if err != nil {
			return err
		}
	}
	return nil
}
