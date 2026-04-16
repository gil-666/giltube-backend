package transcoder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func GetVideoResolution(inputPath string) (int, int, error) {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "csv=s=x:p=0",
		inputPath,
	)

	out, err := cmd.Output()
	if err != nil {
		return 0, 0, err
	}

	var w, h int
	fmt.Sscanf(string(out), "%dx%d", &w, &h)

	return w, h, nil
}


func Transcode(inputPath, videoID string) error {
	outputDir := filepath.Join(os.Getenv("HOME"), "giltube/output", videoID)

	width, height, err := GetVideoResolution(inputPath)
	if err != nil {
		return err
	}

	fmt.Println("Source resolution:", width, "x", height)

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

	// Select only valid renditions
	var selected []Res
	for _, r := range all {
		if height >= r.H {
			selected = append(selected, r)
		}
	}

	// fallback (very small videos)
	if len(selected) == 0 {
		selected = []Res{{"480p", height}}
	}

	// -------------------------
	// Build filter_complex
	// -------------------------
	filter := "[0:v]split=" + fmt.Sprint(len(selected))
	for i := range selected {
		filter += fmt.Sprintf("[v%d]", i)
	}
	filter += ";"

	for i, r := range selected {
		// IMPORTANT: preserve aspect ratio, no padding
		filter += fmt.Sprintf(
			"[v%d]scale=-2:%d[v%dout];",
			i, r.H, i,
		)
	}

	// -------------------------
	// Build ffmpeg args
	// -------------------------
	args := []string{
		"-i", inputPath,
		"-filter_complex", filter,
	}

	// map streams
	for i := range selected {
		args = append(args,
			"-map", fmt.Sprintf("[v%dout]", i),
			"-map", "0:a?",
		)
	}

	args = append(args,
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "23",
		"-c:a", "aac",
		"-b:a", "128k",

		"-f", "hls",
		"-hls_time", "6",
		"-hls_list_size", "0",
	)

	// var_stream_map
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
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("Generating variants:", selected)

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
	outputDir := filepath.Join(os.Getenv("HOME"), "giltube/output", videoID)

	err := os.MkdirAll(outputDir, 0755)
	if err != nil {
		fmt.Println("Failed to create output dir:", err)
		return
	}

	// Generate thumbnail
	err = GenerateThumbnail(inputPath, videoID)
	if err != nil {
		fmt.Println("Thumbnail error:", err)
	} else {
		fmt.Println("Thumbnail generated for", videoID)
	}

	// Generate HLS multi-quality
	err = Transcode(inputPath, videoID)
	if err != nil {
		fmt.Println("Transcode error:", err)
	} else {
		fmt.Println("Transcode success for", videoID)
	}
}

