package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/gil/giltube/internal/queue"
)

func main() {
	flag.Parse()

	if flag.NArg() < 2 {
		fmt.Println("Usage: re-encode <video-id> <file-path>")
		fmt.Println("")
		fmt.Println("Example:")
		fmt.Println("  re-encode db180c4c-b18a-409b-bed5-751e34a16036 /path/to/video.mp4")
		fmt.Println("")
		fmt.Println("This tool queues a video for re-encoding.")
		fmt.Println("The encoder will automatically skip already-encoded qualities and resume missing ones.")
		os.Exit(1)
	}

	videoID := flag.Arg(0)
	filePath := flag.Arg(1)

	// Verify file exists
	if _, err := os.Stat(filePath); err != nil {
		fmt.Printf("Error: File not found: %s\n", filePath)
		os.Exit(1)
	}

	// Initialize Redis queue
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}

	// Enqueue job
	q := queue.New(redisURL)
	err := q.Enqueue(queue.Job{VideoID: videoID, FilePath: filePath})
	if err != nil {
		fmt.Printf("Error queuing video: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Video %s queued for re-encoding\n", videoID)
	fmt.Printf("  File: %s\n", filePath)
	fmt.Printf("\nThe encoder will:\n")
	fmt.Printf("  1. Detect which qualities are already complete\n")
	fmt.Printf("  2. Skip those qualities (resume capability)\n")
	fmt.Printf("  3. Encode only the missing/incomplete qualities\n")
	fmt.Printf("  4. Update the master playlist when done\n")
}
