package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/gil/giltube/config"
	"github.com/gil/giltube/internal/db"
	"github.com/gil/giltube/internal/paths"
	"github.com/gil/giltube/internal/queue"
	"github.com/gil/giltube/internal/transcoder"
	"github.com/google/uuid"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Resolution struct {
	Name string
	H    int
	W    int
}

type liveRecordingSession struct {
	streamKey  string
	outputPath string
	cancel     context.CancelFunc
	done       chan error
	cmdMu      sync.Mutex
	cmd        *exec.Cmd
	leaseCancel context.CancelFunc
	workerID    string
}

var liveRecordingSessions sync.Map

var errLiveRecordingNotOwned = errors.New("live recording is owned by another worker")

const liveRecordingLeaseTTL = 5 * time.Minute
const liveRecordingLeaseRenewInterval = 1 * time.Minute

func translatePath(inputPath string) string {
	if runtime.GOOS != "windows" {
		return inputPath
	}

	if strings.HasPrefix(inputPath, "/") {
		wslPath := strings.TrimPrefix(inputPath, "/")
		wslPath = strings.ReplaceAll(wslPath, "/", "\\")
		return "\\\\wsl.localhost\\Ubuntu\\" + wslPath
	}

	return inputPath
}

func convertWindowsPathToWSL(inputPath string) string {
	if runtime.GOOS != "linux" {
		return inputPath
	}

	if strings.HasPrefix(inputPath, "\\\\wsl.localhost\\Ubuntu\\") {
		linuxPath := strings.TrimPrefix(inputPath, "\\\\wsl.localhost\\Ubuntu")
		linuxPath = strings.ReplaceAll(linuxPath, "\\", "/")
		if !strings.HasPrefix(linuxPath, "/") {
			linuxPath = "/" + linuxPath
		}
		return linuxPath
	}

	if strings.Contains(inputPath, "\\") && len(inputPath) > 1 && inputPath[1] == ':' {
		return strings.ReplaceAll(inputPath, "\\", "/")
	}

	return inputPath
}

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
	frameStr = strings.TrimSuffix(frameStr, ",")
	frames, err := strconv.Atoi(frameStr)
	if err != nil {
		return 0, err
	}

	return frames, nil
}

func hasAudio(inputPath string) bool {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_type",
		"-of", "csv=p=0",
		inputPath,
	)

	out, err := cmd.Output()
	if err != nil {
		return false
	}

	output := strings.TrimSpace(string(out))
	return output == "audio"
}

// isQualityEncoded checks if a specific quality variant is already encoded
func isQualityEncoded(outputDir string, qualityName string) bool {
	playlistPath := filepath.Join(outputDir, qualityName, "playlist.m3u8")

	// Check if playlist exists
	if _, err := os.Stat(playlistPath); err != nil {
		return false
	}

	// Check if at least one segment exists
	segmentPattern := filepath.Join(outputDir, qualityName, "segment_*.ts")
	matches, err := filepath.Glob(segmentPattern)
	if err != nil || len(matches) == 0 {
		return false
	}

	// Check if playlist has #EXT-X-ENDLIST (indicates encoding completed)
	file, err := os.Open(playlistPath)
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if scanner.Text() == "#EXT-X-ENDLIST" {
			return true
		}
	}

	return false
}

type EncoderType struct {
	Name  string
	Codec string
	IsGPU bool
}

func detectGPUEncoder() *EncoderType {
	// Check for AMD GPU encoders
	var encoders []string

	if runtime.GOOS == "windows" {
		// Windows: try h264_amf first (better browser compatibility than hevc_amf)
		encoders = []string{"h264_amf", "hevc_amf"}
	} else {
		// Linux: try ROCM encoders
		encoders = []string{"hevc_rocm", "h264_rocm", "hevc_amf", "h264_amf"}
	}

	for _, encoder := range encoders {
		cmd := exec.Command("ffmpeg", "-encoders", "-hide_banner")
		out, err := cmd.CombinedOutput()
		if err == nil && strings.Contains(string(out), encoder) {
			fmt.Printf("GPU encoder detected: %s (OS: %s)\n", encoder, runtime.GOOS)
			return &EncoderType{Name: encoder, Codec: encoder, IsGPU: true}
		}
	}

	fmt.Printf("GPU encoder not found, using CPU (libx264)\n")
	return &EncoderType{Name: "libx264", Codec: "libx264", IsGPU: false}
}

// Global encoder instance (detected once at startup)
var selectedEncoder *EncoderType

func init() {
	selectedEncoder = detectGPUEncoder()
}

// getEncoderArgs returns FFmpeg codec args for video encoding
// For GPU encoders, uses bitrate; for CPU, uses crf
func getEncoderArgs(encoder *EncoderType, bitrate, maxrate, bufsize string) []string {
	if encoder.IsGPU {
		// AMD h264_amf settings with proper parameters
		args := []string{
			"-c:v", encoder.Codec,
			"-b:v", bitrate,
			"-maxrate:v", maxrate,
			"-bufsize:v", bufsize,
		}

		return args
	} else {
		// CPU encoding (libx264)
		return []string{
			"-c:v", encoder.Codec,
			"-preset", "veryfast",
			"-crf", "20",
			"-b:v", bitrate,
			"-maxrate:v", maxrate,
			"-bufsize:v", bufsize,
		}
	}
}

// tryEncodeWithFallback runs ffmpeg and falls back to CPU if GPU fails
func tryEncodeWithFallback(args []string, isGPUFirstAttempt bool) error {
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()

	// If GPU encoding failed and this was a GPU attempt, retry with CPU
	if err != nil && isGPUFirstAttempt && selectedEncoder.IsGPU {
		fmt.Println("GPU encoding failed, falling back to CPU encoding (libx264)...")

		// Replace GPU codec with CPU codec in arguments
		newArgs := make([]string, len(args))
		copy(newArgs, args)

		for i, arg := range newArgs {
			if arg == selectedEncoder.Codec {
				newArgs[i] = "libx264"
				// Also update encoder flags for CPU
				if i+1 < len(newArgs) && newArgs[i+1] == "libx264" {
					// Remove GPU-specific flags
					for j := i + 2; j < len(newArgs); j++ {
						if newArgs[j] == "-rc" || newArgs[j] == "vbr" {
							// Remove these GPU-specific args
							newArgs = append(newArgs[:j], newArgs[j+2:]...)
							break
						}
					}
					// Add CPU-specific flags
					newArgs = append(newArgs[i+1:i+1], "-preset", "veryfast", "-crf", "20")
				}
				break
			}
		}

		// Retry with CPU
		cmd = exec.Command("ffmpeg", newArgs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err = cmd.Run()

		if err == nil {
			fmt.Println("CPU encoding succeeded as fallback")
		}
	}

	return err
}

func cleanup(path string) {
	err := os.Remove(path)
	if err != nil {
		fmt.Println("Failed to delete temp file:", err)
	} else {
		fmt.Println("Deleted temp file:", path)
	}
}

func liveRecordingsDir() string {
	if dir := strings.TrimSpace(os.Getenv("GILTUBE_LIVE_RECORDINGS_DIR")); dir != "" {
		return dir
	}
	return filepath.Join(paths.OutputDir(), "live-recordings")
}

func liveThumbnailDir(streamKey string) string {
	return filepath.Join(paths.OutputDir(), "live-thumbnails", streamKey)
}

func liveThumbnailOutputPath(streamKey string) string {
	return filepath.Join(liveThumbnailDir(streamKey), "thumbnail.jpg")
}

func publicOutputPath(absPath string) (string, error) {
	relPath, err := filepath.Rel(paths.OutputDir(), absPath)
	if err != nil {
		return "", err
	}
	relPath = filepath.ToSlash(relPath)
	if strings.HasPrefix(relPath, "../") || relPath == ".." {
		return "", fmt.Errorf("path %q is outside output dir", absPath)
	}
	return "/videos/" + relPath, nil
}

func copyFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}

	info, err := src.Stat()
	if err == nil {
		_ = os.Chmod(dstPath, info.Mode())
	}

	return dst.Close()
}

func liveLocalIngestURL(cfg *config.Config) string {
	base := strings.TrimSpace(cfg.MediaMTXLocalRTMPURL)
	if base == "" {
		base = cfg.MediaMTXRTMPURL
	}
	return strings.TrimRight(base, "/") + "/live"
}

func liveRecordingOutputPath(streamKey string) string {
	return filepath.Join(liveRecordingsDir(), streamKey+".mp4")
}

func waitForRecordingReady(recordingPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		info, err := os.Stat(recordingPath)
		if err == nil && info.Size() > 0 {
			probe := exec.Command(
				"ffprobe",
				"-v", "error",
				"-show_entries", "format=duration",
				"-of", "default=noprint_wrappers=1:nokey=1",
				recordingPath,
			)
			if probeErr := probe.Run(); probeErr == nil {
				return nil
			}
		}

		if time.Now().After(deadline) {
			if err == nil {
				return fmt.Errorf("recording is not ready")
			}
			return err
		}

		time.Sleep(500 * time.Millisecond)
	}
}

func findLatestLiveRecording(streamKey string) (string, error) {
	recordingsDir := liveRecordingsDir()
	if recordingsDir == "" {
		return "", fmt.Errorf("live recordings dir is not configured")
	}

	var bestPath string
	var bestModTime time.Time

	err := filepath.Walk(recordingsDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.IsDir() {
			return nil
		}

		if !strings.Contains(path, streamKey) {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".mp4", ".m4v", ".webm", ".mov", ".mkv", ".flv":
		default:
			return nil
		}

		modTime := info.ModTime().UTC()
		if bestPath == "" || modTime.After(bestModTime) {
			bestPath = path
			bestModTime = modTime
		}

		return nil
	})
	if err != nil {
		return "", err
	}
	if bestPath == "" {
		return "", fmt.Errorf("no recording found for stream key %s", streamKey)
	}

	return bestPath, nil
}

func runLiveRecordingLoop(ctx context.Context, cfg *config.Config, q *queue.Queue, workerID string, session *liveRecordingSession) {
	defer close(session.done)
	defer func() {
		if _, err := q.ReleaseLiveRecordingLease(session.streamKey, workerID); err != nil {
			fmt.Printf("failed to release live recording lease for %s: %v\n", session.streamKey, err)
		}
	}()

	inputURL := liveLocalIngestURL(cfg) + "/" + session.streamKey

	for {
		if ctx.Err() != nil {
			session.done <- ctx.Err()
			return
		}

		cmd := exec.Command(
			"ffmpeg",
			"-y",
			"-hide_banner",
			"-loglevel", "error",
			"-i", inputURL,
			"-c", "copy",
			"-movflags", "+faststart",
			session.outputPath,
		)

		session.cmdMu.Lock()
		session.cmd = cmd
		session.cmdMu.Unlock()

		err := cmd.Run()

		session.cmdMu.Lock()
		session.cmd = nil
		session.cmdMu.Unlock()

		if ctx.Err() != nil {
			session.done <- ctx.Err()
			return
		}

		if err == nil {
			if info, statErr := os.Stat(session.outputPath); statErr == nil && info.Size() > 0 {
				session.done <- nil
				return
			}
		}

		select {
		case <-ctx.Done():
			session.done <- ctx.Err()
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func waitForMediaProbeReady(inputPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		probe := exec.Command(
			"ffprobe",
			"-v", "error",
			"-show_entries", "format=duration",
			"-of", "default=noprint_wrappers=1:nokey=1",
			inputPath,
		)
		if probe.Run() == nil {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("media source is not ready")
		}

		time.Sleep(500 * time.Millisecond)
	}
}

func livePlaybackURL(cfg *config.Config, streamKey string) string {
	return strings.TrimRight(cfg.MediaMTXHLSBaseURL, "/") + "/live/" + streamKey + "/index.m3u8"
}

func updateLiveStreamThumbnail(database *sql.DB, streamKey, thumbnailURL string) error {
	_, err := database.Exec(
		`UPDATE live_streams SET thumbnail_url = $1, updated_at = NOW() WHERE stream_key = $2`,
		thumbnailURL,
		streamKey,
	)
	return err
}

func generateLiveThumbnail(cfg *config.Config, database *sql.DB, streamKey string) error {
	streamKey = strings.TrimSpace(streamKey)
	if streamKey == "" {
		return fmt.Errorf("stream key is required")
	}

	inputURL := livePlaybackURL(cfg, streamKey)
	if err := waitForMediaProbeReady(inputURL, 30*time.Second); err != nil {
		return err
	}

	outputDir := liveThumbnailDir(streamKey)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	if err := transcoder.GenerateThumbnail(inputURL, streamKey, outputDir); err != nil {
		return err
	}

	thumbPath := liveThumbnailOutputPath(streamKey)
	publicPath, err := publicOutputPath(thumbPath)
	if err != nil {
		return err
	}

	return updateLiveStreamThumbnail(database, streamKey, publicPath)
}

func runLiveThumbnailLoop(ctx context.Context, cfg *config.Config, database *sql.DB, streamKey string) {
	// Try immediately, then keep refreshing every 2 minutes while live.
	for {
		if ctx.Err() != nil {
			return
		}

		if err := generateLiveThumbnail(cfg, database, streamKey); err != nil {
			fmt.Printf("live thumbnail generation failed for %s: %v\n", streamKey, err)
		} else {
			fmt.Printf("live thumbnail updated for %s\n", streamKey)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Minute):
		}
	}
}

func startLiveRecordingLeaseRenewal(ctx context.Context, q *queue.Queue, streamKey, workerID string, stop func()) {
	ticker := time.NewTicker(liveRecordingLeaseRenewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok, err := q.RefreshLiveRecordingLease(streamKey, workerID, liveRecordingLeaseTTL)
			if err != nil {
				fmt.Printf("live recording lease refresh failed for %s: %v\n", streamKey, err)
				continue
			}
			if !ok {
				fmt.Printf("live recording lease lost for %s on worker %s\n", streamKey, workerID)
				if stop != nil {
					stop()
				}
				return
			}
		}
	}
}

func getLiveRecordingLeaseOwner(q *queue.Queue, streamKey string) (string, error) {
	return q.LiveRecordingLeaseOwner(streamKey)
}

func startLiveRecording(cfg *config.Config, database *sql.DB, q *queue.Queue, workerID, streamKey string) error {
	streamKey = strings.TrimSpace(streamKey)
	if streamKey == "" {
		return fmt.Errorf("stream key is required")
	}

	if err := os.MkdirAll(liveRecordingsDir(), 0755); err != nil {
		return err
	}

	if _, exists := liveRecordingSessions.Load(streamKey); exists {
		return nil
	}

	owned, err := q.AcquireLiveRecordingLease(streamKey, workerID, liveRecordingLeaseTTL)
	if err != nil {
		return err
	}
	if !owned {
		return errLiveRecordingNotOwned
	}

	ctx, cancel := context.WithCancel(context.Background())
	session := &liveRecordingSession{
		streamKey:  streamKey,
		outputPath: liveRecordingOutputPath(streamKey),
		cancel:     cancel,
		done:       make(chan error, 1),
		leaseCancel: cancel,
		workerID:    workerID,
	}
	liveRecordingSessions.Store(streamKey, session)
	go runLiveRecordingLoop(ctx, cfg, q, workerID, session)
	go runLiveThumbnailLoop(ctx, cfg, database, streamKey)
	go startLiveRecordingLeaseRenewal(ctx, q, streamKey, workerID, cancel)
	return nil
}

func stopLiveRecording(q *queue.Queue, workerID, streamKey string) (string, error) {
	streamKey = strings.TrimSpace(streamKey)
	if streamKey == "" {
		return "", fmt.Errorf("stream key is required")
	}

	owner, err := getLiveRecordingLeaseOwner(q, streamKey)
	if err != nil {
		return "", err
	}
	if owner != "" && owner != workerID {
		return "", errLiveRecordingNotOwned
	}

	value, exists := liveRecordingSessions.LoadAndDelete(streamKey)
	if exists {
		session := value.(*liveRecordingSession)
		session.cancel()
		session.cmdMu.Lock()
		if session.cmd != nil && session.cmd.Process != nil {
			_ = session.cmd.Process.Signal(syscall.SIGINT)
		}
		session.cmdMu.Unlock()

		select {
		case <-session.done:
		case <-time.After(10 * time.Second):
			return session.outputPath, fmt.Errorf("timed out while stopping live recorder")
		}
	}

	if err := waitForRecordingReady(liveRecordingOutputPath(streamKey), 5*time.Second); err != nil {
		return liveRecordingOutputPath(streamKey), err
	}

	recordingPath := liveRecordingOutputPath(streamKey)
	info, err := os.Stat(recordingPath)
	if err != nil {
		return recordingPath, err
	}
	if info.Size() == 0 {
		return recordingPath, fmt.Errorf("live recording is empty")
	}

	if _, err := q.ReleaseLiveRecordingLease(streamKey, workerID); err != nil {
		fmt.Printf("failed to release live recording lease for %s: %v\n", streamKey, err)
	}

	return recordingPath, nil
}

func archiveCompletedLiveStreamRecording(database *sql.DB, recordingPath string, job *queue.LiveRecordingJob) error {
	var existingVODVideoID sql.NullString
	err := database.QueryRow(
		"SELECT vod_video_id FROM live_streams WHERE id = $1",
		job.LiveStreamID,
	).Scan(&existingVODVideoID)
	if err != nil {
		return err
	}
	if existingVODVideoID.Valid && strings.TrimSpace(existingVODVideoID.String) != "" {
		cleanup(recordingPath)
		return nil
	}

	videoID := uuid.New().String()
	videoDir := paths.VideoDir(videoID)
	destDir := filepath.Join(videoDir, "source")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	ext := strings.ToLower(filepath.Ext(recordingPath))
	if ext == "" {
		ext = ".mp4"
	}
	destPath := filepath.Join(destDir, "original"+ext)
	if err := copyFile(recordingPath, destPath); err != nil {
		return err
	}
	cleanup(recordingPath)

	publicPath, err := publicOutputPath(destPath)
	if err != nil {
		return err
	}

	title := strings.TrimSpace(job.Title)
	if title == "" {
		title = "Live Stream"
	}

	thumbnailURL := ""
	if err := transcoder.GenerateThumbnail(destPath, videoID, videoDir); err != nil {
		fmt.Printf("thumbnail generation failed for live VOD %s: %v\n", videoID, err)
	} else {
		thumbnailURL = fmt.Sprintf("/videos/%s/thumbnail.jpg", videoID)
	}

	_, err = database.Exec(`
		INSERT INTO videos (
			id, title, description, status, created_at, channel_id, hls_path, thumbnail_url, has_custom_thumbnail
		) VALUES ($1, $2, $3, 'ready', NOW(), $4, $5, $6, false)
	`, videoID, title, job.Description, job.ChannelID, publicPath, thumbnailURL)
	if err != nil {
		return err
	}

	_, err = database.Exec(`
		UPDATE live_streams
		SET vod_video_id = $1, updated_at = NOW()
		WHERE id = $2
	`, videoID, job.LiveStreamID)
	return err
}

func processLiveRecordingJob(cfg *config.Config, database *sql.DB, q *queue.Queue, workerID string, job *queue.LiveRecordingJob) {
	switch strings.TrimSpace(job.Action) {
	case "start":
		if err := startLiveRecording(cfg, database, q, workerID, job.StreamKey); err != nil {
			if errors.Is(err, errLiveRecordingNotOwned) {
				fmt.Printf("live recording start skipped for %s on worker %s: owned by another worker\n", job.StreamKey, workerID)
				return
			}
			fmt.Printf("live recording start failed for %s: %v\n", job.StreamKey, err)
		}
	case "cancel":
		recordingPath, err := stopLiveRecording(q, workerID, job.StreamKey)
		if err != nil && !os.IsNotExist(err) {
			if errors.Is(err, errLiveRecordingNotOwned) {
				fmt.Printf("live recording cancel requeued for %s: owned by another worker\n", job.StreamKey)
				_ = q.EnqueueLiveRecording(*job)
				time.Sleep(500 * time.Millisecond)
				return
			}
			fmt.Printf("live recording cancel failed for %s: %v\n", job.StreamKey, err)
		}
		if recordingPath != "" {
			cleanup(recordingPath)
		}
	case "stop":
		var existingVODVideoID sql.NullString
		if err := database.QueryRow(
			"SELECT vod_video_id FROM live_streams WHERE stream_key = $1 OR channel_id = $2 OR id = $3",
			job.StreamKey,
			job.ChannelID,
			job.LiveStreamID,
		).Scan(&existingVODVideoID); err == nil {
			if existingVODVideoID.Valid && strings.TrimSpace(existingVODVideoID.String) != "" {
				fmt.Printf("live recording stop skipped for channel %s: VOD already exists\n", job.ChannelID)
				return
			}
		}
		recordingPath, err := stopLiveRecording(q, workerID, job.StreamKey)
		if err != nil {
			if errors.Is(err, errLiveRecordingNotOwned) {
				fmt.Printf("live recording stop requeued for %s: owned by another worker\n", job.StreamKey)
				_ = q.EnqueueLiveRecording(*job)
				time.Sleep(500 * time.Millisecond)
				return
			}
			if !os.IsNotExist(err) {
				fmt.Printf("live recording stop failed for %s: %v\n", job.StreamKey, err)
				return
			}

			fallbackPath, fallbackErr := findLatestLiveRecording(job.StreamKey)
			if fallbackErr != nil {
				fmt.Printf("live recording stop failed for %s: %v\n", job.StreamKey, err)
				return
			}

			fmt.Printf("live recording stop fallback for %s: using %s\n", job.StreamKey, fallbackPath)
			recordingPath = fallbackPath
		}
		if err := archiveCompletedLiveStreamRecording(database, recordingPath, job); err != nil {
			fmt.Printf("live stream archive failed for channel %s: %v\n", job.ChannelID, err)
		}
	default:
		fmt.Printf("unknown live recording action: %s\n", job.Action)
	}
}

func transcodeHighestQualityOnly(inputPath, videoID string, database *sql.DB, selected []Resolution, totalFrames int, multiplier float64, outputDir string, hasCaptions bool, bitDepth int, isHDR bool) error {
	// Encode only the highest quality variant (first in selected array)
	if len(selected) == 0 {
		return fmt.Errorf("no resolutions selected")
	}

	highestQuality := selected[0]
	fmt.Printf("Encoding highest quality first: %s (bit depth: %d)\n", highestQuality.Name, bitDepth)

	// Check if highest quality is already encoded (for resume capability)
	if isQualityEncoded(outputDir, highestQuality.Name) {
		fmt.Printf("Highest quality %s already encoded, skipping to remaining qualities\n", highestQuality.Name)
		return nil
	}

	// Check if video has audio
	videoHasAudio := hasAudio(inputPath)
	fmt.Printf("Video has audio: %v\n", videoHasAudio)

	bitrateMap := map[string]string{
		"4320p": "25000k",
		"2160p": "10000k",
		"1080p": "5000k",
		"720p":  "2500k",
		"480p":  "1200k",
		"360p":  "800k",
		"240p":  "400k",
		"144p":  "200k",
	}

	maxrateMap := map[string]string{
		"4320p": "26750k",
		"2160p": "10700k",
		"1080p": "5350k",
		"720p":  "2675k",
		"480p":  "1280k",
		"360p":  "856k",
		"240p":  "428k",
		"144p":  "214k",
	}

	bufsizeMap := map[string]string{
		"4320p": "37500k",
		"2160p": "15000k",
		"1080p": "7500k",
		"720p":  "3750k",
		"480p":  "1800k",
		"360p":  "1200k",
		"240p":  "600k",
		"144p":  "300k",
	}

	// Build filter - scale to target resolution
	// H.264 requires yuv420p, so force that format
	filter := fmt.Sprintf("[0:v]format=yuv420p,scale=-2:%d[vout]", highestQuality.H)

	args := []string{
		"-thread_queue_size", "16", // Reduce thread queue size to avoid excessive buffering
		"-i", inputPath,
		"-filter_complex", filter,
		"-map", "[vout]",
	}

	// Only map audio if it exists
	if videoHasAudio {
		args = append(args, "-map", "0:a:0")
	}

	// Get encoder-specific arguments (GPU or CPU)
	encoderArgs := getEncoderArgs(selectedEncoder,
		transcoder.ApplyMultiplierToBitrate(bitrateMap[highestQuality.Name], multiplier),
		transcoder.ApplyMultiplierToBitrate(maxrateMap[highestQuality.Name], multiplier),
		transcoder.ApplyMultiplierToBitrate(bufsizeMap[highestQuality.Name], multiplier),
	)

	args = append(args, encoderArgs...)

	// Only add scene detection for CPU encoding
	if !selectedEncoder.IsGPU {
		args = append(args,
			"-g", "48",
			"-keyint_min", "48",
			"-sc_threshold", "0",
		)
	}

	// Only add audio encoding settings if audio exists
	if videoHasAudio {
		args = append(args,
			"-c:a", "aac",
			"-ac", "2",
			"-b:a", "256k",
		)
	}

	args = append(args,
		"-f", "hls",
		"-hls_flags", "independent_segments",
		"-hls_time", "6",
		"-hls_list_size", "0",
		"-progress", "pipe:1",
	)

	// Generate master playlist with just the highest quality
	vsm := fmt.Sprintf("v:0")
	if videoHasAudio {
		vsm += fmt.Sprintf(",a:0")
	}
	vsm += fmt.Sprintf(",name:%s", highestQuality.Name)

	args = append(args,
		"-var_stream_map", vsm,
		"-hls_segment_filename", filepath.Join(outputDir, "%v/segment_%03d.ts"),
		filepath.Join(outputDir, "%v/playlist.m3u8"),
	)

	// Generate master playlist manually after encoding (not using FFmpeg's -master_pl_name)
	// This allows us to include captions track

	fmt.Printf("Using encoder: %s (GPU: %v)\n", selectedEncoder.Name, selectedEncoder.IsGPU)

	cmd := exec.Command("ffmpeg", args...)
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr

	err := cmd.Start()
	if err != nil {
		return err
	}

	// Track progress
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

			if time.Since(lastUpdate) > 2*time.Second {
				percentageDone := float64(currentFrame) / float64(totalFrames) * 100.0
				progress := 10 + int(percentageDone*0.8)
				if progress > 90 {
					progress = 90
				}

				db.UpdateVideoProgress(database, videoID, progress)
				lastUpdate = time.Now().UTC()
				fmt.Printf("Highest quality progress: %d%% (frame %d/%d)\n", progress, currentFrame, totalFrames)
			}
		}
	}

	err = cmd.Wait()

	// If GPU encoding failed, retry with CPU
	if err != nil && selectedEncoder.IsGPU {
		fmt.Println("GPU encoding failed, retrying with CPU encoder (libx264)...")

		// Rebuild args with CPU encoder
		args := []string{
			"-thread_queue_size", "16", // Reduce thread queue size
			"-i", inputPath,
			"-filter_complex", filter,
			"-map", "[vout]",
		}

		if videoHasAudio {
			args = append(args, "-map", "0:a:0")
		}

		args = append(args,
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-crf", "20",
			"-b:v", transcoder.ApplyMultiplierToBitrate(bitrateMap[highestQuality.Name], multiplier),
			"-maxrate:v", transcoder.ApplyMultiplierToBitrate(maxrateMap[highestQuality.Name], multiplier),
			"-bufsize:v", transcoder.ApplyMultiplierToBitrate(bufsizeMap[highestQuality.Name], multiplier),
			"-g", "48",
			"-keyint_min", "48",
			"-sc_threshold", "0",
		)

		if videoHasAudio {
			args = append(args,
				"-c:a", "aac",
				"-ac", "2",
				"-b:a", "256k",
			)
		}

		args = append(args,
			"-f", "hls",
			"-hls_flags", "independent_segments",
			"-hls_time", "6",
			"-hls_list_size", "0",
			"-progress", "pipe:1",
			"-var_stream_map", vsm,
			"-hls_segment_filename", filepath.Join(outputDir, "%v/segment_%03d.ts"),
			filepath.Join(outputDir, "%v/playlist.m3u8"),
		)

		cmd = exec.Command("ffmpeg", args...)
		stdout, _ = cmd.StdoutPipe()
		cmd.Stderr = os.Stderr
		err = cmd.Start()
		if err != nil {
			return err
		}

		// Track progress for fallback
		lastUpdate = time.Now().UTC()
		currentFrame = 0
		scanner = bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()

			if strings.HasPrefix(line, "frame=") {
				frameStr := strings.TrimSpace(strings.TrimPrefix(line, "frame="))
				if frame, err := strconv.Atoi(frameStr); err == nil {
					currentFrame = frame
				}

				if time.Since(lastUpdate) > 2*time.Second {
					percentageDone := float64(currentFrame) / float64(totalFrames) * 100.0
					progress := 10 + int(percentageDone*0.8)
					if progress > 90 {
						progress = 90
					}

					db.UpdateVideoProgress(database, videoID, progress)
					lastUpdate = time.Now().UTC()
					fmt.Printf("CPU fallback progress: %d%% (frame %d/%d)\n", progress, currentFrame, totalFrames)
				}
			}
		}

		err = cmd.Wait()
	}

	if err != nil {
		return err
	}

	// Generate master playlist with ONLY the highest quality (since that's all that exists)
	// Remaining qualities will update this when they're done
	err = generateMasterPlaylist(outputDir, []Resolution{highestQuality}, hasCaptions)
	if err != nil {
		return err
	}

	return nil
}

// extractCaptions extracts subtitle/caption streams from video and converts to WebVTT format
func extractCaptions(inputPath, videoID, outputDir string) (bool, error) {
	// First, check if video has subtitle streams
	ffprobeCmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "s",
		"-show_entries", "stream=index,codec_type",
		"-of", "csv=p=0",
		inputPath,
	)

	output, err := ffprobeCmd.Output()
	if err != nil || len(output) == 0 {
		fmt.Println("No subtitles found in video:", err)
		return false, nil
	}

	fmt.Println("Found subtitles, extracting...")

	// Extract all subtitle streams to WebVTT format
	// Use output pattern: captions.vtt for the main caption track
	captionsPath := filepath.Join(outputDir, "captions.vtt")

	ffmpegCmd := exec.Command("ffmpeg",
		"-y",
		"-i", inputPath,
		"-map", "0:s:0", // Select first subtitle stream
		captionsPath,
	)

	if err := ffmpegCmd.Run(); err != nil {
		fmt.Println("Warning: Failed to extract captions:", err)
		return false, nil
	}

	// Verify file was created and has content
	if fileInfo, err := os.Stat(captionsPath); err != nil || fileInfo.Size() == 0 {
		fmt.Println("Caption file empty or not created")
		os.Remove(captionsPath)
		return false, nil
	}

	fmt.Println("Successfully extracted captions to:", captionsPath)
	return true, nil
}

func generateMasterPlaylist(outputDir string, selected []Resolution, hasCaptions bool) error {
	// Generate master.m3u8 with all variants
	masterContent := "#EXTM3U\n#EXT-X-VERSION:3\n"

	// Add caption track if available
	if hasCaptions {
		masterContent += `#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="cc",LANGUAGE="en",NAME="English",DEFAULT=YES,INSTREAM-ID="CC1"
`
	}

	// Bandwidths for each quality (approximate)
	bandwidths := map[string]string{
		"4320p": "25000000",
		"2160p": "10000000",
		"1080p": "5000000",
		"720p":  "2500000",
		"480p":  "1200000",
		"360p":  "800000",
		"240p":  "400000",
		"144p":  "200000",
	}

	for _, res := range selected {
		bandwidth := bandwidths[res.Name]
		ccAttr := ""
		if hasCaptions {
			ccAttr = ",CLOSED-CAPTIONS=\"cc\""
		}
		masterContent += fmt.Sprintf(`#EXT-X-STREAM-INF:BANDWIDTH=%s,RESOLUTION=%dx%d%s
%s/playlist.m3u8
`, bandwidth, res.W, res.H, ccAttr, res.Name)
	}

	// Add caption file URI if available
	if hasCaptions {
		masterContent += `#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",DEFAULT=YES,FORCED=NO,LANGUAGE="en",URI="captions.vtt"
`
	}

	masterPath := filepath.Join(outputDir, "master.m3u8")
	err := os.WriteFile(masterPath, []byte(masterContent), 0644)
	if err != nil {
		return fmt.Errorf("failed to write master.m3u8: %w", err)
	}

	fmt.Println("Generated master playlist with", len(selected), "variants")
	if hasCaptions {
		fmt.Println("Master playlist includes captions track")
	}
	return nil
}

func transcodeRemainingQualities(inputPath, videoID string, database *sql.DB, selected []Resolution, totalFrames int, multiplier float64, outputDir string, hasCaptions bool, bitDepth int, isHDR bool) error {
	// Encode remaining lower quality variants
	if len(selected) <= 1 {
		fmt.Println("No remaining qualities to encode")
		return nil
	}

	remainingQualities := selected[1:]

	// Filter out already-encoded qualities (for resume capability)
	var qualitiesToEncode []Resolution
	var alreadyEncoded []string

	for _, q := range remainingQualities {
		if isQualityEncoded(outputDir, q.Name) {
			alreadyEncoded = append(alreadyEncoded, q.Name)
		} else {
			qualitiesToEncode = append(qualitiesToEncode, q)
		}
	}

	if len(alreadyEncoded) > 0 {
		fmt.Printf("Resuming interrupted encode - skipping already-encoded qualities: %v\n", alreadyEncoded)
	}

	if len(qualitiesToEncode) == 0 {
		fmt.Println("All quality variants already encoded, skipping remaining qualities stage")
		return generateMasterPlaylist(outputDir, selected, hasCaptions)
	}

	fmt.Printf("Encoding %d remaining quality variants in parallel with libx264 (bit depth: %d)\n", len(qualitiesToEncode), bitDepth)

	bitrateMap := map[string]string{
		"4320p": "25000k",
		"2160p": "10000k",
		"1080p": "5000k",
		"720p":  "2500k",
		"480p":  "1200k",
		"360p":  "800k",
		"240p":  "400k",
		"144p":  "200k",
	}

	maxrateMap := map[string]string{
		"4320p": "26750k",
		"2160p": "10700k",
		"1080p": "5350k",
		"720p":  "2675k",
		"480p":  "1280k",
		"360p":  "856k",
		"240p":  "428k",
		"144p":  "214k",
	}

	bufsizeMap := map[string]string{
		"4320p": "37500k",
		"2160p": "15000k",
		"1080p": "7500k",
		"720p":  "3750k",
		"480p":  "1800k",
		"360p":  "1200k",
		"240p":  "600k",
		"144p":  "300k",
	}

	// Use goroutines to encode multiple qualities in parallel (max 3 concurrent)
	semaphore := make(chan struct{}, 3)
	var wg sync.WaitGroup
	errors := make([]error, len(qualitiesToEncode))

	for idx, resolution := range qualitiesToEncode {
		wg.Add(1)
		go func(index int, res Resolution) {
			defer wg.Done()
			semaphore <- struct{}{}        // Acquire semaphore
			defer func() { <-semaphore }() // Release semaphore

			fmt.Printf("Encoding quality: %s (libx264 CPU)\n", res.Name)

			qualityDir := filepath.Join(outputDir, res.Name)
			if err := os.MkdirAll(qualityDir, 0755); err != nil {
				fmt.Printf("Failed to create quality dir %s: %v\n", qualityDir, err)
				errors[index] = err
				return
			}

			// Build filter for single quality
			var filter string
			if isHDR {
				// For HDR, skip colorspace conversion
				filter = fmt.Sprintf("[0:v]scale=-2:%d[scaled]", res.H)
			} else {
				filter = fmt.Sprintf("[0:v]format=yuv420p,scale=-2:%d[scaled]", res.H)
			}

			args := []string{
				"-i", inputPath,
				"-filter_complex", filter,
				"-map", "[scaled]",
			}

			// Map audio
			if hasAudio(inputPath) {
				args = append(args, "-map", "0:a:0")
			}

			// Use libx264 for all background qualities
			args = append(args,
				"-c:v", "libx264",
				"-preset", "fast",
				"-crf", "20",
				"-b:v", transcoder.ApplyMultiplierToBitrate(bitrateMap[res.Name], multiplier),
				"-maxrate", transcoder.ApplyMultiplierToBitrate(maxrateMap[res.Name], multiplier),
				"-bufsize", transcoder.ApplyMultiplierToBitrate(bufsizeMap[res.Name], multiplier),
				"-g", "48",
				"-keyint_min", "48",
				"-sc_threshold", "0",
			)

			// Audio codec
			if hasAudio(inputPath) {
				args = append(args,
					"-c:a", "aac",
					"-ac", "2",
					"-b:a", "256k",
				)
			}

			// HLS output
			args = append(args,
				"-f", "hls",
				"-hls_flags", "independent_segments",
				"-hls_time", "6",
				"-hls_list_size", "0",
				"-hls_segment_filename", filepath.Join(qualityDir, "segment_%03d.ts"),
				filepath.Join(qualityDir, "playlist.m3u8"),
			)

			cmd := exec.Command("ffmpeg", args...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr

			err := cmd.Run()
			if err != nil {
				fmt.Printf("Failed to encode %s: %v\n", res.Name, err)
				errors[index] = err
				return
			}

			fmt.Printf("Successfully encoded: %s\n", res.Name)
		}(idx, resolution)
	}

	wg.Wait()

	// Check for any encoding errors
	for i, err := range errors {
		if err != nil {
			fmt.Printf("Quality encoding failed: %s\n", qualitiesToEncode[i].Name)
		}
	}

	// After all sequential encoding, regenerate master playlist
	return generateMasterPlaylist(outputDir, selected, hasCaptions)
}

func transcodeWithProgress(inputPath, videoID string, database *sql.DB) (int, error) {
	// Get total frames first
	totalFrames, err := getTotalFrames(inputPath)
	if err != nil {
		fmt.Println("Failed to get total frames:", err)
		totalFrames = 1 // fallback
	}
	fmt.Printf("Total frames: %d\n", totalFrames)

	outputDir := paths.VideoDir(videoID)

	width, height, err := transcoder.GetVideoResolution(inputPath)
	if err != nil {
		return 0, err
	}

	fps, _ := transcoder.GetVideoFrameRate(inputPath)
	multiplier := transcoder.ApplyFrameRateMultiplier(fps)

	// Detect bit depth (8, 10, 12, etc.)
	bitDepth := transcoder.GetVideoBitDepth(inputPath)

	// Detect if video is HDR (smpte2084, arib-std-b67, etc.)
	isHDR := transcoder.IsHDRVideo(inputPath)

	fmt.Println("Source resolution:", width, "x", height)
	fmt.Printf("Frame rate multiplier: %.2fx\n", multiplier)
	fmt.Printf("Bit depth: %d-bit\n", bitDepth)
	if isHDR {
		fmt.Println("Video type: HDR (colorspace filter will be skipped)")
	}

	all := []Resolution{
		{"4320p", 4320, 7680},
		{"2160p", 2160, 3840},
		{"1080p", 1080, 1920},
		{"720p", 720, 1280},
		{"480p", 480, 854},
		{"360p", 360, 640},
		{"240p", 240, 426},
		{"144p", 144, 256},
	}

	var selected []Resolution
	for _, r := range all {
		// Select if either height or width meets the threshold
		// This handles both standard and ultra-wide aspect ratios
		if height >= r.H || width >= r.W {
			selected = append(selected, r)
		}
	}

	if len(selected) == 0 {
		selected = []Resolution{{"144p", height, width}}
	}

	// Extract captions from original video (before encoding)
	hasCaptions, err := extractCaptions(inputPath, videoID, outputDir)
	if err != nil {
		fmt.Printf("Warning: Failed to process captions for %s: %v\n", videoID, err)
		// Continue even if caption extraction fails
	}

	// First pass: encode highest quality only
	err = transcodeHighestQualityOnly(inputPath, videoID, database, selected, totalFrames, multiplier, outputDir, hasCaptions, bitDepth, isHDR)
	if err != nil {
		return 0, err
	}

	// Second pass: encode remaining qualities in background (don't wait for completion)
	go func() {
		err := transcodeRemainingQualities(inputPath, videoID, database, selected, totalFrames, multiplier, outputDir, hasCaptions, bitDepth, isHDR)
		if err != nil {
			fmt.Printf("Background encoding error for %s: %v\n", videoID, err)
		}
	}()

	return width, nil
}

func processDownloadJob(job *queue.DownloadJob) {
	videoDir := paths.VideoQualityDir(job.VideoID, job.Quality)
	playlistPath := filepath.Join(videoDir, "playlist.m3u8")

	// Check if playlist exists
	if _, err := os.Stat(playlistPath); os.IsNotExist(err) {
		fmt.Println("Playlist not found:", playlistPath)
		return
	}

	// Prepare output file
	outputDir := paths.DownloadsDir()
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

	fmt.Println("Download ready:", outputFile)
}

func main() {
	cfg := config.Load()
	q := queue.New(cfg.RedisURL)
	database := db.Connect(cfg.DatabaseURL)
	workerID := uuid.NewString()

	fmt.Printf("Worker started... id=%s\n", workerID)

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

	go func() {
		for {
			job, err := q.DequeueLiveRecording()
			if err != nil {
				fmt.Println("Live recording queue error:", err)
				continue
			}
			fmt.Println("Processing live recording job:", job.Action, job.StreamKey)
			processLiveRecordingJob(cfg, database, q, workerID, job)
		}
	}()

	// Main transcoding job processor
	for {
		job, err := q.Dequeue()
		if err != nil {
			fmt.Println("Queue error:", err)
			continue
		}

		// Convert Windows UNC paths to WSL Linux paths if worker is running in WSL
		job.FilePath = convertWindowsPathToWSL(job.FilePath)

		// Translate WSL paths to Windows interop format if running on Windows
		job.FilePath = translatePath(job.FilePath)

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

		// Check if custom thumbnail already exists
		var hasCustomThumbnail bool
		var existingThumbnailURL string
		err = database.QueryRow(
			"SELECT has_custom_thumbnail, thumbnail_url FROM videos WHERE id = $1",
			job.VideoID,
		).Scan(&hasCustomThumbnail, &existingThumbnailURL)
		if err != nil {
			fmt.Println("Error checking custom thumbnail:", err)
		}

		var thumbURL string
		// Only generate thumbnail if no custom thumbnail exists
		if !hasCustomThumbnail {
			thumbURL = "/videos/" + job.VideoID + "/thumbnail.jpg"

			// 2. generate thumbnail (10% progress)
			outputPath := paths.VideoDir(job.VideoID)
			err = transcoder.GenerateThumbnail(job.FilePath, job.VideoID, outputPath)
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
		} else {
			thumbURL = existingThumbnailURL
			fmt.Println("Using custom thumbnail for", job.VideoID)
		}

		err = db.UpdateVideoProgress(database, job.VideoID, 10)
		if err != nil {
			fmt.Println("Progress update error (10%):", err)
		}

		// 3. transcode highest quality (10-90% progress) with real-time progress tracking
		// Lower quality variants will encode in the background
		width, err := transcodeWithProgress(job.FilePath, job.VideoID, database)
		if err != nil {
			fmt.Println("Transcode error:", err)
			db.UpdateVideoStatus(database, job.VideoID, "failed")
			db.UpdateVideoProgress(database, job.VideoID, 0)
			cleanup(job.FilePath)
			continue
		}

		// 4. mark as ready after highest quality is done (100% progress)
		// Lower quality variants continue encoding in background
		err = db.UpdateVideoStatus(database, job.VideoID, "ready")
		if err != nil {
			fmt.Println("DB error marking as ready:", err)
			continue
		}

		hlsPath := "/videos/" + job.VideoID + "/master.m3u8"

		_, err = database.Exec(
			`UPDATE videos 
			SET status=$1, hls_path=$2, thumbnail_url=$3, progress=$4, width=$5
			WHERE id=$6`,
			"ready",
			hlsPath,
			thumbURL,
			100,
			width,
			job.VideoID,
		)

		if err != nil {
			fmt.Println("DB update error:", err)
		}

		// Don't delete the file yet - it's still being used by background encoding
		// cleanup(job.FilePath)

		fmt.Println("Video ready to watch (background encoding in progress):", job.VideoID)
	}

}
