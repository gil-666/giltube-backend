package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"github.com/gil/giltube/config"
	"github.com/gil/giltube/internal/queue"
	"github.com/gil/giltube/internal/transcoder"
	"github.com/gil/giltube/internal/db"
)

func getTotalFrames(inputPath string) (int, error) {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-count_packets",
		"-show_entries", "stream=nb_read_packets",
		"-of", "csv=p=0",
		inputPath,
	)

	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	frameStr := strings.TrimSpace(string(out))
	frames, err := strconv.Atoi(frameStr)
	if err != nil {
		return 0, err
	}

	return frames, nil
}

func cleanup(path string) {
	err := os.Remove(path)
	if err != nil {
		fmt.Println("Failed to delete temp file:", err)
	} else {
		fmt.Println("Deleted temp file:", path)
	}
}

func transcodeWithProgress(inputPath, videoID string, database *sql.DB) error {
	// Get total frames first
	totalFrames, err := getTotalFrames(inputPath)
	if err != nil {
		fmt.Println("Failed to get total frames:", err)
		totalFrames = 1 // fallback
	}
	fmt.Printf("Total frames: %d\n", totalFrames)

	outputDir := filepath.Join(os.Getenv("HOME"), "giltube/output", videoID)

	width, height, err := transcoder.GetVideoResolution(inputPath)
	if err != nil {
		return err
	}

	fps, _ := transcoder.GetVideoFrameRate(inputPath)
	multiplier := transcoder.ApplyFrameRateMultiplier(fps)

	fmt.Println("Source resolution:", width, "x", height)
	fmt.Printf("Frame rate multiplier: %.2fx\n", multiplier)

	type Res struct {
		Name string
		H    int
	}

	all := []Res{
		{"1080p", 1080},
		{"720p", 720},
		{"480p", 480},
		{"360p", 360},
		{"240p", 240},
		{"144p", 144},
	}

	var selected []Res
	for _, r := range all {
		if height >= r.H {
			selected = append(selected, r)
		}
	}

	if len(selected) == 0 {
		selected = []Res{{"144p", height}}
	}

	bitrateMap := map[string]string{
		"1080p": "5000k",
		"720p":  "2500k",
		"480p":  "1200k",
		"360p":  "800k",
		"240p":  "400k",
		"144p":  "200k",
	}

	maxrateMap := map[string]string{
		"1080p": "5350k",
		"720p":  "2675k",
		"480p":  "1280k",
		"360p":  "856k",
		"240p":  "428k",
		"144p":  "214k",
	}

	bufsizeMap := map[string]string{
		"1080p": "7500k",
		"720p":  "3750k",
		"480p":  "1800k",
		"360p":  "1200k",
		"240p":  "600k",
		"144p":  "300k",
	}

	filter := "[0:v]split=" + fmt.Sprint(len(selected))
	for i := range selected {
		filter += fmt.Sprintf("[v%d]", i)
	}
	filter += ";"

	for i, r := range selected {
		filter += fmt.Sprintf("[v%d]scale=-2:%d[v%dout];", i, r.H, i)
	}

	args := []string{
		"-i", inputPath,
		"-filter_complex", filter,
	}

	for i := range selected {
		args = append(args,
			"-map", fmt.Sprintf("[v%dout]", i),
			"-map", "0:a?",
		)
	}

	for i, r := range selected {
		args = append(args,
			"-c:v:"+fmt.Sprint(i), "libx264",
			"-preset", "veryfast",
			"-crf", "20",
			"-b:v:"+fmt.Sprint(i), transcoder.ApplyMultiplierToBitrate(bitrateMap[r.Name], multiplier),
			"-maxrate:v:"+fmt.Sprint(i), transcoder.ApplyMultiplierToBitrate(maxrateMap[r.Name], multiplier),
			"-bufsize:v:"+fmt.Sprint(i), transcoder.ApplyMultiplierToBitrate(bufsizeMap[r.Name], multiplier),
			"-g", "48",
			"-keyint_min", "48",
			"-sc_threshold", "0",
		)
	}

	args = append(args,
		"-c:a", "aac",
		"-b:a", "128k",
		"-f", "hls",
		"-hls_time", "6",
		"-hls_list_size", "0",
		"-progress", "pipe:1",
	)

	var vsm string
	for i, r := range selected {
		vsm += fmt.Sprintf("v:%d,a:%d,name:%s ", i, i, r.Name)
	}

	args = append(args,
		"-var_stream_map", vsm,
		"-master_pl_name", "master.m3u8",
		"-hls_segment_filename",
		filepath.Join(outputDir, "%v/segment_%03d.ts"),
		filepath.Join(outputDir, "%v/playlist.m3u8"),
	)

	cmd := exec.Command("ffmpeg", args...)
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr

	err = cmd.Start()
	if err != nil {
		return err
	}

	// Track progress while ffmpeg runs
	lastUpdate := time.Now().UTC()
	currentFrame := 0

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "frame=") {
			frameStr := strings.TrimSpace(strings.TrimPrefix(line, "frame="))
			if frame, err := strconv.Atoi(frameStr); err == nil {
				currentFrame = frame
			}

			// Update progress every 2 seconds
			if time.Since(lastUpdate) > 2*time.Second {
				// Calculate percentage based on total frames
				percentageDone := float64(currentFrame) / float64(totalFrames) * 100.0
				// Map to 10-90% range
				progress := 10 + int(percentageDone*0.8)
				if progress > 90 {
					progress = 90
				}
				
				db.UpdateVideoProgress(database, videoID, progress)
				lastUpdate = time.Now().UTC()
				fmt.Printf("Transcode progress: %d%% (frame %d/%d)\n", progress, currentFrame, totalFrames)
			}
		}
	}

	err = cmd.Wait()
	return err
}

func processDownloadJob(job *queue.DownloadJob) {
	homeDir := os.Getenv("HOME")
	videoDir := filepath.Join(homeDir, "giltube/output", job.VideoID, job.Quality)
	playlistPath := filepath.Join(videoDir, "playlist.m3u8")
	
	// Check if playlist exists
	if _, err := os.Stat(playlistPath); os.IsNotExist(err) {
		fmt.Println("Playlist not found:", playlistPath)
		return
	}

	// Prepare output file
	outputDir := filepath.Join(homeDir, "giltube/downloads")
	os.MkdirAll(outputDir, 0755)
	outputFile := filepath.Join(outputDir, fmt.Sprintf("%s_%s.mp4", job.VideoID, job.Quality))

	// Use ffmpeg to convert HLS to MP4
	cmd := exec.Command(
		"ffmpeg",
		"-i", playlistPath,
		"-c", "copy",
		"-bsf:a", "aac_adtstoasc",
		"-y",
		outputFile,
	)

	fmt.Println("Converting HLS to MP4:", outputFile)
	
	err := cmd.Run()
	if err != nil {
		fmt.Println("FFmpeg error:", err)
		return
	}

	// Verify file was created
	fileInfo, err := os.Stat(outputFile)
	if err != nil || fileInfo.Size() == 0 {
		fmt.Println("Failed to create download file:", outputFile)
		return
	}

	// Open and sync file to ensure it's written to disk
	f, err := os.Open(outputFile)
	if err != nil {
		fmt.Println("Failed to open download file for sync:", err)
		return
	}
	defer f.Close()
	
	// Sync file to disk
	if err := f.Sync(); err != nil {
		fmt.Println("Failed to sync download file:", err)
		return
	}

	fmt.Println("Download ready:", outputFile)
}

func main() {
	cfg := config.Load()
	q := queue.New(cfg.RedisURL)
	database := db.Connect(cfg.DatabaseURL)

	fmt.Println("Worker started...")

	// Start download job processor in a separate goroutine
	go func() {
		for {
			job, err := q.DequeueDownload()
			if err != nil {
				fmt.Println("Download queue error:", err)
				continue
			}
			fmt.Println("Processing download job:", job.VideoID, job.Quality)
			processDownloadJob(job)
		}
	}()

	// Main transcoding job processor
	for {
		job, err := q.Dequeue()
		if err != nil {
			fmt.Println("Queue error:", err)
			continue
		}

		fmt.Println("Processing:", job.VideoID)

		// 1. mark as processing with 0% progress
		err = db.UpdateVideoStatus(database, job.VideoID, "processing")
		if err != nil {
			fmt.Println("DB error:", err)
			continue
		}
		err = db.UpdateVideoProgress(database, job.VideoID, 0)
		if err != nil {
			fmt.Println("Progress update error (0%):", err)
		}

		thumbURL := "/videos/" + job.VideoID + "/thumbnail.jpg"
		
		// 2. generate thumbnail (10% progress)
		err = transcoder.GenerateThumbnail(job.FilePath, job.VideoID)
		if err != nil {
			fmt.Println("Thumbnail error:", err)
			db.UpdateVideoStatus(database, job.VideoID, "failed")
			db.UpdateVideoProgress(database, job.VideoID, 0)
			_, err = database.Exec(
				`UPDATE videos 
				SET thumbnail_url=$1 
				WHERE id=$2`,
				thumbURL,
				job.VideoID,
			)
			cleanup(job.FilePath)
			continue
		}
		err = db.UpdateVideoProgress(database, job.VideoID, 10)
		if err != nil {
			fmt.Println("Progress update error (10%):", err)
		}

		// 3. transcode (10-90% progress) with real-time progress tracking
		err = transcodeWithProgress(job.FilePath, job.VideoID, database)
		if err != nil {
			fmt.Println("Transcode error:", err)
			db.UpdateVideoStatus(database, job.VideoID, "failed")
			db.UpdateVideoProgress(database, job.VideoID, 0)
			cleanup(job.FilePath)
			continue
		}
		err = db.UpdateVideoProgress(database, job.VideoID, 90)
		if err != nil {
			fmt.Println("Progress update error (90%):", err)
		}

		// 4. mark as ready (100% progress)
		err = db.UpdateVideoStatus(database, job.VideoID, "ready")
		if err != nil {
			fmt.Println("DB error:", err)
			continue
		}
		err = db.UpdateVideoProgress(database, job.VideoID, 100)
		if err != nil {
			fmt.Println("Progress update error (100%):", err)
		}
		
		hlsPath := "/videos/" + job.VideoID + "/master.m3u8"

		_, err = database.Exec(
			`UPDATE videos 
			SET status=$1, hls_path=$2, thumbnail_url=$3, progress=$4
			WHERE id=$5`,
			"ready",
			hlsPath,
			thumbURL,
			100,
			job.VideoID,
		)

		if err != nil {
			fmt.Println("DB update error:", err)
		}

		cleanup(job.FilePath)
		fmt.Println("Done:", job.VideoID)
	}



}
