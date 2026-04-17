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
			ch.id,
			ch.name,
			COALESCE(ch.avatar_url, '')
		FROM comments c
		JOIN channels ch ON c.channel_id = ch.id
		WHERE c.video_id = $1
		ORDER BY c.created_at DESC
	`, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var comments []map[string]interface{}
	for rows.Next() {
		var id, text, createdAt, chID, chName, chAvatarURL string
		err := rows.Scan(&id, &text, &createdAt, &chID, &chName, &chAvatarURL)
		if err != nil {
			continue
		}
		comments = append(comments, map[string]interface{}{
			"id":         id,
			"text":       text,
			"created_at": createdAt,
			"channel": map[string]interface{}{
				"id":         chID,
				"name":       chName,
				"avatar_url": chAvatarURL,
			},
		})
	}
	return comments, nil
}

func CreateComment(db *sql.DB, commentID, videoID, channelID, text string) error {
	_, err := db.Exec(
		"INSERT INTO comments (id, video_id, channel_id, text, created_at) VALUES ($1, $2, $3, $4, NOW())",
		commentID,
		videoID,
		channelID,
		text,
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

