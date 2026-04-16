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
