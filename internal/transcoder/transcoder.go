package transcoder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func Transcode(inputPath string, videoID string) error {
	outputDir := filepath.Join(os.Getenv("HOME"), "giltube/output", videoID)

	// create folder
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	cmd := exec.Command(
		"ffmpeg",
		"-i", inputPath,

		// video + audio codecs
		"-c:v", "libx264",
		"-c:a", "aac",

		// HLS settings
		"-f", "hls",
		"-hls_time", "6",
		"-hls_list_size", "0",

		// segment files
		"-hls_segment_filename", filepath.Join(outputDir, "segment_%03d.ts"),

		// output playlist
		filepath.Join(outputDir, "playlist.m3u8"),
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("Running HLS FFmpeg for:", inputPath)

	return cmd.Run()
}


func GenerateThumbnail(inputPath, videoID string) error {
	outputDir := filepath.Join(os.Getenv("HOME"), "giltube/output", videoID)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	thumbPath := filepath.Join(outputDir, "thumbnail.jpg")

	cmd := exec.Command(
		"ffmpeg",
		"-ss", "3",
		"-i", inputPath,
		"-vframes", "1",
		"-q:v", "2",
		thumbPath,
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("Generating thumbnail:", thumbPath)

	return cmd.Run()
}

func ProcessVideo(inputPath, videoID string) {
	err := GenerateThumbnail(inputPath, videoID)
	if err != nil {
		fmt.Println("Thumbnail error:", err)
		return
	}

	fmt.Println("Thumbnail done")

	err = Transcode(inputPath, videoID)
	if err != nil {
		fmt.Println("Transcode error:", err)
		return
	}

	fmt.Println("Transcode done")
}

