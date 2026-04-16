package main

import (
	"fmt"
	"github.com/gil/giltube/config"
	"github.com/gil/giltube/internal/queue"
	"github.com/gil/giltube/internal/transcoder"
	"github.com/gil/giltube/internal/db"
	"os"
)

func cleanup(path string) {
	err := os.Remove(path)
	if err != nil {
		fmt.Println("Failed to delete temp file:", err)
	} else {
		fmt.Println("Deleted temp file:", path)
	}
}

func main() {
	cfg := config.Load()
	q := queue.New(cfg.RedisURL)
	database := db.Connect(cfg.DatabaseURL)

	fmt.Println("Worker started...")

	for {
	job, err := q.Dequeue()
	if err != nil {
		fmt.Println("Queue error:", err)
		continue
	}

	fmt.Println("Processing:", job.VideoID)

	// 1. mark as processing
	err = db.UpdateVideoStatus(database, job.VideoID, "processing")
	if err != nil {
		fmt.Println("DB error:", err)
		continue
	}

	// 2. generate thumbnail
	err = transcoder.GenerateThumbnail(job.FilePath, job.VideoID)
	if err != nil {
		fmt.Println("Thumbnail error:", err)

		db.UpdateVideoStatus(database, job.VideoID, "failed")
		cleanup(job.FilePath)
		continue
	}

	// 3. transcode
	err = transcoder.Transcode(job.FilePath, job.VideoID)
	if err != nil {
		fmt.Println("Transcode error:", err)
		
		db.UpdateVideoStatus(database, job.VideoID, "failed")
		cleanup(job.FilePath)
		continue
	}

	// 4. mark as ready
	err = db.UpdateVideoStatus(database, job.VideoID, "ready")
	if err != nil {
		fmt.Println("DB error:", err)
		continue
	}
	
	cleanup(job.FilePath)
	fmt.Println("Done:", job.VideoID)
	

}



}
