package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gil/giltube/internal/paths"
	"github.com/gil/giltube/internal/queue"
	"github.com/gil/giltube/internal/transcoder"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type liveManageRequest struct {
	ChannelID   string `json:"channel_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Enabled     *bool  `json:"enabled"`
}

type postLiveChatRequest struct {
	ChannelID string `json:"channel_id"`
	Message   string `json:"message"`
}

type liveRecordingSession struct {
	streamKey    string
	liveStreamID string
	channelID    string
	title        string
	description  string
	outputPath   string
	cancel       context.CancelFunc
	done         chan error
	cmdMu        sync.Mutex
	cmd          *exec.Cmd
	stopMu       sync.Mutex
	stopping     bool
}

func (s *liveRecordingSession) markStopping() {
	s.stopMu.Lock()
	s.stopping = true
	s.stopMu.Unlock()
}

func (s *liveRecordingSession) isStopping() bool {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	return s.stopping
}

func (s *Server) ensureLiveStreamsTable() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS live_streams (
			id TEXT PRIMARY KEY,
			channel_id TEXT NOT NULL UNIQUE REFERENCES channels(id) ON DELETE CASCADE,
			title TEXT NOT NULL DEFAULT 'Live Stream',
			description TEXT NOT NULL DEFAULT '',
			stream_key TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL DEFAULT 'offline',
			use_publisher_presence BOOLEAN NOT NULL DEFAULT FALSE,
			thumbnail_url TEXT NULL,
			started_at TIMESTAMPTZ NULL,
			ended_at TIMESTAMPTZ NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		ALTER TABLE live_streams
		ADD COLUMN IF NOT EXISTS use_publisher_presence BOOLEAN NOT NULL DEFAULT FALSE;

		ALTER TABLE live_streams
		ADD COLUMN IF NOT EXISTS vod_video_id TEXT NULL;

		ALTER TABLE live_streams
		ADD COLUMN IF NOT EXISTS thumbnail_url TEXT NULL;

		CREATE TABLE IF NOT EXISTS live_chat_messages (
			id TEXT PRIMARY KEY,
			live_stream_id TEXT NOT NULL REFERENCES live_streams(id) ON DELETE CASCADE,
			channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
			message TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS idx_live_streams_status_started_at
		ON live_streams(status, started_at DESC);

		CREATE INDEX IF NOT EXISTS idx_live_chat_messages_stream_created_at
		ON live_chat_messages(live_stream_id, created_at DESC);
	`)
	return err
}

func generateStreamKey() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *Server) requireUserID(c *gin.Context) (string, bool) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return "", false
	}
	return userID, true
}

func (s *Server) channelBelongsToUser(channelID, userID string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM channels WHERE id = $1 AND user_id = $2)",
		channelID,
		userID,
	).Scan(&exists)
	return exists, err
}

func (s *Server) ensureLiveStreamForChannel(channelID string) error {
	streamKey, err := generateStreamKey()
	if err != nil {
		return err
	}

	_, err = s.db.Exec(
		"INSERT INTO live_streams (id, channel_id, stream_key) VALUES ($1, $2, $3) ON CONFLICT (channel_id) DO NOTHING",
		uuid.New().String(),
		channelID,
		streamKey,
	)
	return err
}

func parseNullTime(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time
	return &t
}

func normalizeAvatarURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http") {
		return raw
	}
	if strings.HasPrefix(raw, "/avatars/") {
		return raw
	}
	return "/avatars/" + raw
}

func (s *Server) liveIngestURL() string {
	return strings.TrimRight(s.cfg.MediaMTXRTMPURL, "/") + "/live"
}

func (s *Server) liveLocalIngestURL() string {
	base := strings.TrimSpace(s.cfg.MediaMTXLocalRTMPURL)
	if base == "" {
		base = s.cfg.MediaMTXRTMPURL
	}
	return strings.TrimRight(base, "/") + "/live"
}

func (s *Server) liveLanIngestURL() string {
	base := strings.TrimSpace(s.cfg.MediaMTXLanRTMPURL)
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/live"
}

func (s *Server) livePlaybackURL(streamKey string) string {
	return strings.TrimRight(s.cfg.MediaMTXHLSBaseURL, "/") + "/live/" + streamKey + "/index.m3u8"
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

func findLatestLiveRecording(streamKey string, startedAt sql.NullTime) (string, error) {
	recordingsDir := liveRecordingsDir()
	if recordingsDir == "" {
		return "", fmt.Errorf("GILTUBE_LIVE_RECORDINGS_DIR is not configured")
	}

	var startedAtUTC time.Time
	if startedAt.Valid {
		startedAtUTC = startedAt.Time.UTC().Add(-5 * time.Minute)
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
		if !startedAtUTC.IsZero() && modTime.Before(startedAtUTC) {
			return nil
		}

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

func (s *Server) archiveCompletedLiveStreamRecording(recordingPath, liveStreamID, channelID, title, description string) (string, error) {
	videoID := uuid.New().String()
	videoDir := paths.VideoDir(videoID)
	destDir := filepath.Join(videoDir, "source")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}

	ext := strings.ToLower(filepath.Ext(recordingPath))
	if ext == "" {
		ext = ".mp4"
	}
	destPath := filepath.Join(destDir, "original"+ext)
	if err := copyFile(recordingPath, destPath); err != nil {
		return "", err
	}
	defer func() {
		if err := os.Remove(recordingPath); err != nil && !os.IsNotExist(err) {
			fmt.Printf("failed to remove temporary live recording %s: %v\n", recordingPath, err)
		}
	}()

	publicPath, err := publicOutputPath(destPath)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(title) == "" {
		title = "Live Stream"
	}

	thumbnailURL := ""
	if err := transcoder.GenerateThumbnail(destPath, videoID, videoDir); err != nil {
		fmt.Printf("thumbnail generation failed for live VOD %s: %v\n", videoID, err)
	} else {
		thumbnailURL = fmt.Sprintf("/videos/%s/thumbnail.jpg", videoID)
	}

	_, err = s.db.Exec(`
		INSERT INTO videos (
			id, title, description, status, created_at, channel_id, hls_path, thumbnail_url, has_custom_thumbnail
		) VALUES ($1, $2, $3, 'ready', NOW(), $4, $5, $6, false)
	`, videoID, title, description, channelID, publicPath, thumbnailURL)
	if err != nil {
		return "", err
	}

	_, err = s.db.Exec(`
		UPDATE live_streams
		SET vod_video_id = $1, updated_at = NOW()
		WHERE id = $2
	`, videoID, liveStreamID)
	if err != nil {
		return "", err
	}

	return videoID, nil
}

func (s *Server) archiveLiveRecordingSession(session *liveRecordingSession, recordingPath string) (string, error) {
	return s.archiveCompletedLiveStreamRecording(recordingPath, session.liveStreamID, session.channelID, session.title, session.description)
}

func (s *Server) archiveCompletedLiveStream(liveStreamID, channelID, title, description, streamKey string, startedAt sql.NullTime) (string, error) {
	recordingPath, err := findLatestLiveRecording(streamKey, startedAt)
	if err != nil {
		return "", err
	}
	return s.archiveCompletedLiveStreamRecording(recordingPath, liveStreamID, channelID, title, description)
}

func (s *Server) liveRecordingOutputPath(streamKey string) string {
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
			if probe.Run() == nil {
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

func (s *Server) generateLiveThumbnail(streamKey string) error {
	streamKey = strings.TrimSpace(streamKey)
	if streamKey == "" {
		return fmt.Errorf("stream key is required")
	}

	if err := waitForMediaProbeReady(s.livePlaybackURL(streamKey), 30*time.Second); err != nil {
		return err
	}

	outputDir := liveThumbnailDir(streamKey)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	if err := transcoder.GenerateThumbnail(s.livePlaybackURL(streamKey), streamKey, outputDir); err != nil {
		return err
	}

	thumbPath := liveThumbnailOutputPath(streamKey)
	publicPath, err := publicOutputPath(thumbPath)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(
		`UPDATE live_streams SET thumbnail_url = $1, updated_at = NOW() WHERE stream_key = $2`,
		publicPath,
		streamKey,
	)
	return err
}

func (s *Server) runLiveThumbnailLoop(ctx context.Context, streamKey string) {
	for {
		if ctx.Err() != nil {
			return
		}

		if err := s.generateLiveThumbnail(streamKey); err != nil {
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

func liveRecordingReadyTimeout() time.Duration {
	return 20 * time.Second
}

func liveRecordingStopTimeout() time.Duration {
	return 20 * time.Second
}

func (s *Server) enqueueLiveRecordingJob(action, liveStreamID, channelID, streamKey, title, description string) {
	if s.queue == nil {
		fmt.Printf("live recording queue unavailable for action %s on channel %s\n", action, channelID)
		return
	}

	job := queue.LiveRecordingJob{
		Action:       strings.TrimSpace(action),
		LiveStreamID: strings.TrimSpace(liveStreamID),
		ChannelID:    strings.TrimSpace(channelID),
		StreamKey:    strings.TrimSpace(streamKey),
		Title:        strings.TrimSpace(title),
		Description:  strings.TrimSpace(description),
	}

	if err := s.queue.EnqueueLiveRecording(job); err != nil {
		fmt.Printf("failed to enqueue live recording job (%s) for channel %s: %v\n", action, channelID, err)
	}
}

func (s *Server) hasLiveRecordingSession(streamKey string) bool {
	streamKey = strings.TrimSpace(streamKey)
	if streamKey == "" {
		return false
	}

	s.liveRecordingsMu.Lock()
	defer s.liveRecordingsMu.Unlock()
	_, exists := s.liveRecordings[streamKey]
	return exists
}

func (s *Server) monitorPublisherPresence() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		if err := s.syncPublisherPresenceOnce(); err != nil {
			fmt.Printf("publisher presence sync failed: %v\n", err)
		}
		<-ticker.C
	}
}

func (s *Server) monitorLiveThumbnails() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	if err := s.refreshLiveThumbnailsOnce(); err != nil {
		fmt.Printf("live thumbnail sweep failed: %v\n", err)
	}

	for {
		<-ticker.C
		if err := s.refreshLiveThumbnailsOnce(); err != nil {
			fmt.Printf("live thumbnail sweep failed: %v\n", err)
		}
	}
}

func (s *Server) refreshLiveThumbnailsOnce() error {
	rows, err := s.db.Query(`
		SELECT id, channel_id, status, COALESCE(use_publisher_presence, false), COALESCE(stream_key, '')
		FROM live_streams
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var liveStreamID, channelID, status, streamKey string
		var usePublisherPresence bool
		if err := rows.Scan(&liveStreamID, &channelID, &status, &usePublisherPresence, &streamKey); err != nil {
			continue
		}

		isLive, _ := s.resolveLiveState(status, usePublisherPresence, streamKey)
		if !isLive || strings.TrimSpace(streamKey) == "" {
			continue
		}

		if err := s.generateLiveThumbnail(streamKey); err != nil {
			fmt.Printf("failed to refresh live thumbnail for channel %s: %v\n", channelID, err)
		}
	}

	return rows.Err()
}

func (s *Server) syncPublisherPresenceOnce() error {
	rows, err := s.db.Query(`
		SELECT id, channel_id, title, description, stream_key, status, COALESCE(vod_video_id, '')
		FROM live_streams
		WHERE COALESCE(use_publisher_presence, false) = true
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var liveStreamID, channelID, title, description, streamKey, status, vodVideoID string
		if err := rows.Scan(&liveStreamID, &channelID, &title, &description, &streamKey, &status, &vodVideoID); err != nil {
			continue
		}

		actualLive, _ := s.resolveLiveState(status, true, streamKey)

		if actualLive {
			if status != "live" {
				_, _ = s.db.Exec(`
					UPDATE live_streams
					SET status = 'live', started_at = NOW(), ended_at = NULL, vod_video_id = NULL, updated_at = NOW()
					WHERE id = $1
				`, liveStreamID)
				s.enqueueLiveRecordingJob("start", liveStreamID, channelID, streamKey, title, description)
				var actorUserID string
				_ = s.db.QueryRow("SELECT user_id FROM channels WHERE id = $1", channelID).Scan(&actorUserID)
				if err := s.notifyLiveStarted(channelID, actorUserID, title, description); err != nil {
					fmt.Printf("live start notification broadcast failed for channel %s: %v\n", channelID, err)
				}
			}
			continue
		}

		if status == "live" {
			_, _ = s.db.Exec(`
				UPDATE live_streams
				SET status = 'offline', ended_at = NOW(), updated_at = NOW()
				WHERE id = $1
			`, liveStreamID)
			if strings.TrimSpace(vodVideoID) == "" {
				s.enqueueLiveRecordingJob("stop", liveStreamID, channelID, streamKey, title, description)
			}
		}
	}

	return rows.Err()
}

func (s *Server) runLiveRecordingLoop(ctx context.Context, session *liveRecordingSession) {
	defer close(session.done)
	defer func() {
		s.liveRecordingsMu.Lock()
		delete(s.liveRecordings, session.streamKey)
		s.liveRecordingsMu.Unlock()
	}()

	inputURL := s.liveLocalIngestURL() + "/" + session.streamKey

	for {
		if ctx.Err() != nil {
			session.done <- ctx.Err()
			return
		}

		_ = os.Remove(session.outputPath)
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

		if session.isStopping() {
			session.done <- nil
			return
		}

		if ctx.Err() != nil {
			session.done <- ctx.Err()
			return
		}

		if err == nil {
			if info, statErr := os.Stat(session.outputPath); statErr == nil && info.Size() > 0 {
				if readyErr := waitForRecordingReady(session.outputPath, liveRecordingReadyTimeout()); readyErr != nil {
					session.done <- readyErr
					return
				}
				if _, archiveErr := s.archiveLiveRecordingSession(session, session.outputPath); archiveErr != nil {
					session.done <- archiveErr
				} else {
					session.done <- nil
				}
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

func (s *Server) startLiveRecording(streamKey, liveStreamID, channelID, title, description string) error {
	streamKey = strings.TrimSpace(streamKey)
	if streamKey == "" {
		return fmt.Errorf("stream key is required")
	}

	if err := os.MkdirAll(liveRecordingsDir(), 0755); err != nil {
		return err
	}

	s.liveRecordingsMu.Lock()
	defer s.liveRecordingsMu.Unlock()

	if _, exists := s.liveRecordings[streamKey]; exists {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	session := &liveRecordingSession{
		streamKey:  streamKey,
		outputPath: s.liveRecordingOutputPath(streamKey),
		cancel:     cancel,
		done:       make(chan error, 1),
	}
	s.liveRecordings[streamKey] = session
	session.liveStreamID = strings.TrimSpace(liveStreamID)
	session.channelID = strings.TrimSpace(channelID)
	session.title = strings.TrimSpace(title)
	session.description = strings.TrimSpace(description)
	go s.runLiveRecordingLoop(ctx, session)
	go s.runLiveThumbnailLoop(ctx, streamKey)

	return nil
}

func (s *Server) stopLiveRecording(streamKey string) (string, error) {
	streamKey = strings.TrimSpace(streamKey)
	if streamKey == "" {
		return "", fmt.Errorf("stream key is required")
	}

	s.liveRecordingsMu.Lock()
	session, exists := s.liveRecordings[streamKey]
	s.liveRecordingsMu.Unlock()

	if exists {
		session.markStopping()
		session.cmdMu.Lock()
		if session.cmd != nil && session.cmd.Process != nil {
			_ = session.cmd.Process.Signal(syscall.SIGINT)
		}
		session.cmdMu.Unlock()

		select {
		case <-session.done:
		case <-time.After(liveRecordingStopTimeout()):
			session.cancel()
			return session.outputPath, fmt.Errorf("timed out while stopping live recorder")
		}
	}

	recordingPath := s.liveRecordingOutputPath(streamKey)
	if err := waitForRecordingReady(recordingPath, liveRecordingReadyTimeout()); err != nil {
		return recordingPath, err
	}

	info, err := os.Stat(recordingPath)
	if err != nil {
		return recordingPath, err
	}
	if info.Size() == 0 {
		return recordingPath, fmt.Errorf("live recording is empty")
	}

	return recordingPath, nil
}

func (s *Server) mediamtxPathIsLive(streamPath string) (bool, error) {
	apiBase := strings.TrimRight(s.cfg.MediaMTXAPIURL, "/")
	if apiBase == "" {
		return false, nil
	}

	endpoint := apiBase + "/v3/paths/get/" + url.PathEscape(streamPath)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}

	var payload struct {
		Ready       bool `json:"ready"`
		SourceReady bool `json:"sourceReady"`
		ReaderCount int  `json:"readerCount"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, err
	}

	return payload.Ready || payload.SourceReady || payload.ReaderCount > 0, nil
}

func (s *Server) hlsPlaybackPathIsLive(streamKey string) (bool, error) {
	streamKey = strings.TrimSpace(streamKey)
	if streamKey == "" {
		return false, nil
	}

	playbackURL := s.livePlaybackURL(streamKey)
	req, err := http.NewRequest(http.MethodGet, playbackURL, nil)
	if err != nil {
		return false, err
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, nil
	}

	return true, nil
}

func (s *Server) resolveLiveState(manualStatus string, usePublisherPresence bool, streamKey string) (bool, bool) {
	manualLive := manualStatus == "live"
	if !usePublisherPresence || strings.TrimSpace(streamKey) == "" {
		return manualLive, false
	}

	publisherLive, err := s.mediamtxPathIsLive("live/" + streamKey)
	if err != nil {
		publisherLive, err = s.hlsPlaybackPathIsLive(streamKey)
		if err != nil {
			// Fallback to manual status if presence checks fail.
			return manualLive, false
		}
	}
	// In publisher-presence mode, publisher state is authoritative when checks succeed.
	return publisherLive, publisherLive
}

func persistedLiveState(status string, usePublisherPresence bool) (bool, bool, string) {
	isLive := status == "live"
	publisherDetectedLive := usePublisherPresence && isLive
	resolvedStatus := "offline"
	if isLive {
		resolvedStatus = "live"
	}
	return isLive, publisherDetectedLive, resolvedStatus
}

func (s *Server) getMyLiveStream(c *gin.Context) {
	userID, ok := s.requireUserID(c)
	if !ok {
		return
	}

	channelID := strings.TrimSpace(c.Query("channel_id"))
	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	isOwner, err := s.channelBelongsToUser(channelID, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify channel ownership"})
		return
	}
	if !isOwner {
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not own this channel"})
		return
	}

	if err := s.ensureLiveStreamForChannel(channelID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initialize live stream"})
		return
	}

	var id, title, description, streamKey, status string
	var usePublisherPresence bool
	var startedAt, endedAt sql.NullTime
	var createdAt, updatedAt time.Time
	var thumbnailURL sql.NullString

	err = s.db.QueryRow(`
		SELECT id, channel_id, title, description, stream_key, status, use_publisher_presence, thumbnail_url, started_at, ended_at, created_at, updated_at
		FROM live_streams
		WHERE channel_id = $1
	`, channelID).Scan(
		&id,
		&channelID,
		&title,
		&description,
		&streamKey,
		&status,
		&usePublisherPresence,
		&thumbnailURL,
		&startedAt,
		&endedAt,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load live stream"})
		return
	}

	_, publisherDetectedLive, resolvedStatus := persistedLiveState(status, usePublisherPresence)

	c.JSON(http.StatusOK, gin.H{
		"id":                      id,
		"channel_id":              channelID,
		"title":                   title,
		"description":             description,
		"stream_key":              streamKey,
		"status":                  resolvedStatus,
		"manual_status":           status,
		"use_publisher_presence":  usePublisherPresence,
		"publisher_detected_live": publisherDetectedLive,
		"thumbnail_url":          func() string { if thumbnailURL.Valid { return thumbnailURL.String }; return "" }(),
		"started_at":              parseNullTime(startedAt),
		"ended_at":                parseNullTime(endedAt),
		"created_at":              createdAt,
		"updated_at":              updatedAt,
		"ingest_url":              s.liveIngestURL(),
		"ingest_url_local":        s.liveLocalIngestURL(),
		"ingest_url_lan":          s.liveLanIngestURL(),
		"stream_name":             streamKey,
		"playback_url":            s.livePlaybackURL(streamKey),
	})
}

func (s *Server) setMyPublisherPresence(c *gin.Context) {
	userID, ok := s.requireUserID(c)
	if !ok {
		return
	}

	var body liveManageRequest
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	body.ChannelID = strings.TrimSpace(body.ChannelID)
	if body.ChannelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}
	if body.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled is required"})
		return
	}

	isOwner, err := s.channelBelongsToUser(body.ChannelID, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify channel ownership"})
		return
	}
	if !isOwner {
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not own this channel"})
		return
	}

	if err := s.ensureLiveStreamForChannel(body.ChannelID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initialize live stream"})
		return
	}

	_, err = s.db.Exec(`
		UPDATE live_streams
		SET use_publisher_presence = $1, updated_at = NOW()
		WHERE channel_id = $2
	`, *body.Enabled, body.ChannelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update publisher presence setting"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":                "publisher presence setting updated",
		"use_publisher_presence": *body.Enabled,
	})
}

func (s *Server) updateMyLiveStreamSettings(c *gin.Context) {
	userID, ok := s.requireUserID(c)
	if !ok {
		return
	}

	var body liveManageRequest
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	body.ChannelID = strings.TrimSpace(body.ChannelID)
	if body.ChannelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	isOwner, err := s.channelBelongsToUser(body.ChannelID, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify channel ownership"})
		return
	}
	if !isOwner {
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not own this channel"})
		return
	}

	if err := s.ensureLiveStreamForChannel(body.ChannelID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initialize live stream"})
		return
	}

	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = "Live Stream"
	}
	description := strings.TrimSpace(body.Description)

	_, err = s.db.Exec(`
		UPDATE live_streams
		SET title = $1, description = $2, updated_at = NOW()
		WHERE channel_id = $3
	`, title, description, body.ChannelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save live stream settings"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":     "live stream settings saved",
		"title":       title,
		"description": description,
	})
}

func (s *Server) rotateMyLiveStreamKey(c *gin.Context) {
	userID, ok := s.requireUserID(c)
	if !ok {
		return
	}

	var body liveManageRequest
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	body.ChannelID = strings.TrimSpace(body.ChannelID)
	if body.ChannelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	isOwner, err := s.channelBelongsToUser(body.ChannelID, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify channel ownership"})
		return
	}
	if !isOwner {
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not own this channel"})
		return
	}

	if err := s.ensureLiveStreamForChannel(body.ChannelID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initialize live stream"})
		return
	}

	var currentStreamKey string
	err = s.db.QueryRow("SELECT stream_key FROM live_streams WHERE channel_id = $1", body.ChannelID).Scan(&currentStreamKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load current stream key"})
		return
	}

	s.enqueueLiveRecordingJob("cancel", "", body.ChannelID, currentStreamKey, "", "")

	newKey, err := generateStreamKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate stream key"})
		return
	}

	_, err = s.db.Exec(`
		UPDATE live_streams
		SET stream_key = $1, status = 'offline', started_at = NULL, ended_at = NOW(), vod_video_id = NULL, updated_at = NOW()
		WHERE channel_id = $2
	`, newKey, body.ChannelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rotate stream key"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":          "stream key rotated",
		"stream_key":       newKey,
		"ingest_url":       s.liveIngestURL(),
		"ingest_url_local": s.liveLocalIngestURL(),
		"ingest_url_lan":   s.liveLanIngestURL(),
		"stream_name":      newKey,
		"playback_url":     s.livePlaybackURL(newKey),
	})
}

func (s *Server) startMyLiveStream(c *gin.Context) {
	userID, ok := s.requireUserID(c)
	if !ok {
		return
	}

	var body liveManageRequest
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	body.ChannelID = strings.TrimSpace(body.ChannelID)
	if body.ChannelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	isOwner, err := s.channelBelongsToUser(body.ChannelID, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify channel ownership"})
		return
	}
	if !isOwner {
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not own this channel"})
		return
	}

	if err := s.ensureLiveStreamForChannel(body.ChannelID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initialize live stream"})
		return
	}

	var liveStreamID, title, description, streamKey, currentStatus string
	var usePublisherPresence bool
	err = s.db.QueryRow("SELECT id, title, description, stream_key, status, COALESCE(use_publisher_presence, false) FROM live_streams WHERE channel_id = $1", body.ChannelID).Scan(&liveStreamID, &title, &description, &streamKey, &currentStatus, &usePublisherPresence)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load stream key"})
		return
	}
	title = strings.TrimSpace(body.Title)
	if title == "" {
		title = "Live Stream"
	}
	description = strings.TrimSpace(body.Description)

	_, err = s.db.Exec(`
		UPDATE live_streams
		SET title = $1, description = $2, status = 'live', started_at = NOW(), ended_at = NULL, vod_video_id = NULL, thumbnail_url = NULL, updated_at = NOW()
		WHERE channel_id = $3
	`, title, description, body.ChannelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start stream"})
		return
	}

	s.enqueueLiveRecordingJob("start", liveStreamID, body.ChannelID, streamKey, title, description)
	if currentStatus != "live" {
		if err := s.notifyLiveStarted(body.ChannelID, userID, title, description); err != nil {
			fmt.Printf("live start notification broadcast failed for channel %s: %v\n", body.ChannelID, err)
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "stream marked live"})
}

func (s *Server) stopMyLiveStream(c *gin.Context) {
	userID, ok := s.requireUserID(c)
	if !ok {
		return
	}

	var body liveManageRequest
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	body.ChannelID = strings.TrimSpace(body.ChannelID)
	if body.ChannelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	isOwner, err := s.channelBelongsToUser(body.ChannelID, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify channel ownership"})
		return
	}
	if !isOwner {
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not own this channel"})
		return
	}

	var usePublisherPresence bool
	var liveStreamID, title, description, streamKey string
	var startedAt sql.NullTime
	var existingVODVideoID sql.NullString
	err = s.db.QueryRow(`
		SELECT id, title, description, stream_key, started_at, vod_video_id, COALESCE(use_publisher_presence, false)
		FROM live_streams
		WHERE channel_id = $1
	`, body.ChannelID).Scan(&liveStreamID, &title, &description, &streamKey, &startedAt, &existingVODVideoID, &usePublisherPresence)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load stream before stopping"})
		return
	}
	_, err = s.db.Exec(`
		UPDATE live_streams
		SET status = 'offline', ended_at = NOW(), updated_at = NOW()
		WHERE channel_id = $1
	`, body.ChannelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to stop stream"})
		return
	}

	if existingVODVideoID.Valid && strings.TrimSpace(existingVODVideoID.String) != "" {
		c.JSON(http.StatusOK, gin.H{"message": "stream stopped", "vod_created": false, "video_id": existingVODVideoID.String})
		return
	}

	s.enqueueLiveRecordingJob("stop", liveStreamID, body.ChannelID, streamKey, title, description)

	c.JSON(http.StatusOK, gin.H{"message": "stream stopped", "vod_created": false})
}

func (s *Server) getChannelLiveStatus(c *gin.Context) {
	channelID := c.Param("channel_id")

	var streamID, title, description, streamKey, status, channelName string
	var usePublisherPresence bool
	var startedAt, endedAt sql.NullTime
	var channelAvatar sql.NullString
	var channelVerified bool
	var thumbnailURL sql.NullString

	err := s.db.QueryRow(`
		SELECT
			COALESCE(ls.id, ''),
			COALESCE(ls.title, 'Live Stream'),
			COALESCE(ls.description, ''),
			COALESCE(ls.stream_key, ''),
			COALESCE(ls.status, 'offline'),
			COALESCE(ls.use_publisher_presence, false),
			COALESCE(ls.thumbnail_url, ''),
			ls.started_at,
			ls.ended_at,
			c.name,
			COALESCE(c.avatar_url, ''),
			COALESCE(c.verified, false)
		FROM channels c
		LEFT JOIN live_streams ls ON ls.channel_id = c.id
		WHERE c.id = $1
	`, channelID).Scan(
		&streamID,
		&title,
		&description,
		&streamKey,
		&status,
		&usePublisherPresence,
		&thumbnailURL,
		&startedAt,
		&endedAt,
		&channelName,
		&channelAvatar,
		&channelVerified,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load channel live status"})
		return
	}

	avatar := ""
	if channelAvatar.Valid {
		avatar = normalizeAvatarURL(channelAvatar.String)
	}

	isLive, publisherDetectedLive, resolvedStatus := persistedLiveState(status, usePublisherPresence)

	payload := gin.H{
		"id":                      streamID,
		"channel_id":              channelID,
		"title":                   title,
		"description":             description,
		"status":                  resolvedStatus,
		"manual_status":           status,
		"use_publisher_presence":  usePublisherPresence,
		"publisher_detected_live": publisherDetectedLive,
		"thumbnail_url":          func() string { if thumbnailURL.Valid { return thumbnailURL.String }; return "" }(),
		"started_at":              parseNullTime(startedAt),
		"ended_at":                parseNullTime(endedAt),
		"channel":                 gin.H{"id": channelID, "name": channelName, "avatar_url": avatar, "verified": channelVerified},
		"is_live":                 isLive,
		"playback_url":            "",
		"playback_url_public":     "",
	}

	if streamKey != "" {
		payload["playback_url"] = s.livePlaybackURL(streamKey)
		payload["playback_url_public"] = s.livePlaybackURL(streamKey)
	}

	c.JSON(http.StatusOK, payload)
}

func (s *Server) listActiveLiveStreams(c *gin.Context) {
	rows, err := s.db.Query(`
		SELECT
			ls.channel_id,
			ls.title,
			ls.description,
			ls.stream_key,
			ls.status,
			COALESCE(ls.use_publisher_presence, false),
			COALESCE(ls.thumbnail_url, ''),
			ls.started_at,
			c.name,
			COALESCE(c.avatar_url, ''),
			COALESCE(c.verified, false)
		FROM live_streams ls
		JOIN channels c ON c.id = ls.channel_id
		WHERE ls.status = 'live' OR COALESCE(ls.use_publisher_presence, false) = true
		ORDER BY ls.started_at DESC NULLS LAST
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list live streams"})
		return
	}
	defer rows.Close()

	streams := make([]gin.H, 0)
	for rows.Next() {
		var channelID, title, description, streamKey, status, channelName string
		var usePublisherPresence bool
		var startedAt sql.NullTime
		var avatarRaw sql.NullString
		var thumbnailURL sql.NullString
		var verified bool
		if err := rows.Scan(&channelID, &title, &description, &streamKey, &status, &usePublisherPresence, &thumbnailURL, &startedAt, &channelName, &avatarRaw, &verified); err != nil {
			continue
		}

		isLive, _, _ := persistedLiveState(status, usePublisherPresence)
		if !isLive {
			continue
		}

		avatar := ""
		if avatarRaw.Valid {
			avatar = normalizeAvatarURL(avatarRaw.String)
		}

		streams = append(streams, gin.H{
			"channel_id":             channelID,
			"title":                  title,
			"description":            description,
			"status":                 "live",
			"is_live":                true,
			"use_publisher_presence": usePublisherPresence,
			"thumbnail_url":          func() string { if thumbnailURL.Valid { return thumbnailURL.String }; return "" }(),
			"started_at":             parseNullTime(startedAt),
			"playback_url":           s.livePlaybackURL(streamKey),
			"channel":                gin.H{"id": channelID, "name": channelName, "avatar_url": avatar, "verified": verified},
		})
	}

	if streams == nil {
		streams = []gin.H{}
	}
	c.JSON(http.StatusOK, streams)
}

func (s *Server) getLiveChatMessages(c *gin.Context) {
	channelID := c.Param("channel_id")
	limit := 60
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			if parsed > 0 && parsed <= 200 {
				limit = parsed
			}
		}
	}

	rows, err := s.db.Query(`
		SELECT
			m.id,
			m.message,
			m.created_at,
			ch.id,
			ch.name,
			COALESCE(ch.avatar_url, ''),
			COALESCE(ch.verified, false)
		FROM live_chat_messages m
		JOIN live_streams ls ON ls.id = m.live_stream_id
		JOIN channels ch ON ch.id = m.channel_id
		WHERE ls.channel_id = $1
		ORDER BY m.created_at DESC
		LIMIT $2
	`, channelID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load live chat messages"})
		return
	}
	defer rows.Close()

	messages := make([]gin.H, 0)
	for rows.Next() {
		var id, message, actorChannelID, actorName string
		var createdAt time.Time
		var avatarRaw sql.NullString
		var verified bool
		if err := rows.Scan(&id, &message, &createdAt, &actorChannelID, &actorName, &avatarRaw, &verified); err != nil {
			continue
		}

		avatar := ""
		if avatarRaw.Valid {
			avatar = normalizeAvatarURL(avatarRaw.String)
		}

		messages = append(messages, gin.H{
			"id":         id,
			"message":    message,
			"created_at": createdAt,
			"channel": gin.H{
				"id":         actorChannelID,
				"name":       actorName,
				"avatar_url": avatar,
				"verified":   verified,
			},
		})
	}

	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	if messages == nil {
		messages = []gin.H{}
	}

	c.JSON(http.StatusOK, messages)
}

func (s *Server) postLiveChatMessage(c *gin.Context) {
	userID, ok := s.requireUserID(c)
	if !ok {
		return
	}

	targetChannelID := strings.TrimSpace(c.Param("channel_id"))
	if targetChannelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	var body postLiveChatRequest
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	body.ChannelID = strings.TrimSpace(body.ChannelID)
	body.Message = strings.TrimSpace(body.Message)
	if body.ChannelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required for chat identity"})
		return
	}
	if body.Message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message is required"})
		return
	}
	if len(body.Message) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message too long"})
		return
	}

	isOwner, err := s.channelBelongsToUser(body.ChannelID, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify chat identity"})
		return
	}
	if !isOwner {
		c.JSON(http.StatusForbidden, gin.H{"error": "you do not own this chat channel"})
		return
	}

	if err := s.ensureLiveStreamForChannel(targetChannelID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initialize live stream"})
		return
	}

	var liveStreamID, streamKey, status string
	var usePublisherPresence bool
	err = s.db.QueryRow(
		"SELECT id, stream_key, status, COALESCE(use_publisher_presence, false) FROM live_streams WHERE channel_id = $1",
		targetChannelID,
	).Scan(&liveStreamID, &streamKey, &status, &usePublisherPresence)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve target live stream"})
		return
	}

	isLive, _, _ := persistedLiveState(status, usePublisherPresence)
	if !isLive {
		c.JSON(http.StatusBadRequest, gin.H{"error": "live chat is available only while stream is live"})
		return
	}

	messageID := uuid.New().String()
	_, err = s.db.Exec(
		"INSERT INTO live_chat_messages (id, live_stream_id, channel_id, message, created_at) VALUES ($1, $2, $3, $4, NOW())",
		messageID,
		liveStreamID,
		body.ChannelID,
		body.Message,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to post live chat message"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "chat message sent", "id": messageID})
}
