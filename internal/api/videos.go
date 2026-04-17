package api

import (
	"net/http"
	"time"
	"fmt"
	"strings"
	"path/filepath"
	"os"
	"database/sql"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gil/giltube/internal/models"
	"github.com/gil/giltube/internal/queue"
	"github.com/gil/giltube/internal/db"
)


func (s *Server) uploadVideo(c *gin.Context) {
	file, err := c.FormFile("video")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no file"})
		return
	}

	path := "/tmp/" + file.Filename

	if err := c.SaveUploadedFile(file, path); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save failed"})
		return
	}
	videoID := uuid.New().String()
	video := models.Video{
		ID:        videoID,
		Title:     c.PostForm("title"),
		Description:  c.PostForm("description"),
		Status:    "uploaded",
		CreatedAt: time.Now().UTC(),
		ChannelID: c.PostForm("channel_id"),

	}
	var channelID = strings.TrimSpace(c.PostForm("channel_id"))
	fmt.Println("channel_id:", channelID)
	_, err = s.db.Exec(
	"INSERT INTO videos (id, title, description, status, created_at, channel_id, hls_path) VALUES ($1, $2, $3, $4, $5, $6, $7)",
	video.ID,
	video.Title,
	video.Description,
	video.Status,
	video.CreatedAt,
	channelID,
	video.HLSPath,
	)

	
	if err != nil {
	fmt.Println("DB ERROR:", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	return
	}


	err= s.queue.Enqueue(queue.Job{VideoID: video.ID, FilePath: path})
	if err != nil {
		fmt.Println("Queue ERROR:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "queue failed"})
		return
	}

	c.JSON(http.StatusCreated, video)
}

func (s *Server) listVideos(c *gin.Context) {
	type ChannelResponse struct {
		ID          string `json:"id"`
		UserID      string `json:"user_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		CreatedAt   time.Time `json:"created_at"`
		AvatarURL   string `json:"avatar_url"`
	}

	type VideoResponse struct {
		models.Video
		Channel ChannelResponse `json:"channel"`
	}

	// Public endpoint - return all published videos ordered by recent
	rows, err := s.db.Query(`
		SELECT 
			v.id,
			v.title,
			v.description,
			v.status,
			COALESCE(v.views, 0),
			v.hls_path,
			v.thumbnail_url,
			v.created_at,
			v.channel_id,
			c.id,
			c.user_id,
			c.name,
			c.description,
			c.created_at,
			c.avatar_url
		FROM videos v
		LEFT JOIN channels c ON v.channel_id = c.id
		WHERE v.status = 'ready'
		ORDER BY v.created_at DESC
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db query failed"})
		return
	}
	defer rows.Close()

	videos := []VideoResponse{}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}

	for rows.Next() {
		var v models.Video
		var chID, chUserID, chName, chDesc sql.NullString
		var chCreatedAt sql.NullTime
		var chAvatarURL sql.NullString

		err := rows.Scan(
			&v.ID,
			&v.Title,
			&v.Description,
			&v.Status,
			&v.Views,
			&v.HLSPath,
			&v.ThumbnailURL,
			&v.CreatedAt,
			&v.ChannelID,

			&chID,
			&chUserID,
			&chName,
			&chDesc,
			&chCreatedAt,
			&chAvatarURL,
		)
		if err != nil {
			fmt.Println("Scan error:", err)
			continue
		}

		// Build avatar URL
		avatarURL := ""
		if chAvatarURL.Valid && chAvatarURL.String != "" {
			avatarURL = fmt.Sprintf("%s://%s/avatars/%s", scheme, c.Request.Host, chAvatarURL.String)
		}

		videos = append(videos, VideoResponse{
			Video: v,
			Channel: ChannelResponse{
				ID:          chID.String,
				UserID:      chUserID.String,
				Name:        chName.String,
				Description: chDesc.String,
				CreatedAt:   chCreatedAt.Time,
				AvatarURL:   avatarURL,
			},
		})
	}

	if videos == nil {
		videos = []VideoResponse{}
	}

	c.JSON(http.StatusOK, videos)
}

func (s *Server) listMyVideos(c *gin.Context) {
	// Get user_id from context (set by middleware), query param, or header
	userID := c.GetString("user_id")
	if userID == "" {
		userID = c.DefaultQuery("user_id", "")
	}
	if userID == "" {
		userID = c.GetHeader("X-User-ID")
	}
	
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// Get channel_id from query params (required)
	channelID := c.Query("channel_id")
	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	// Verify that the channel belongs to this user
	var channelUserID string
	err := s.db.QueryRow(
		"SELECT user_id FROM channels WHERE id = $1",
		channelID,
	).Scan(&channelUserID)
	if err != nil || channelUserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}

	type ChannelResponse struct {
		ID          string `json:"id"`
		UserID      string `json:"user_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		CreatedAt   time.Time `json:"created_at"`
		AvatarURL   string `json:"avatar_url"`
	}

	type VideoResponse struct {
		models.Video
		Channel ChannelResponse `json:"channel"`
	}

	// User's videos from specific channel - return ALL videos regardless of status
	// (uploaded, processing, ready, failed, etc.)
	rows, err := s.db.Query(`
		SELECT 
			v.id,
			v.title,
			v.description,
			v.status,
			COALESCE(v.progress, 0),
			COALESCE(v.views, 0),
			v.hls_path,
			v.thumbnail_url,
			v.created_at,
			v.channel_id,
			c.id,
			c.user_id,
			c.name,
			c.description,
			c.created_at,
			c.avatar_url
		FROM videos v
		LEFT JOIN channels c ON v.channel_id = c.id
		WHERE v.channel_id = $1
		ORDER BY v.created_at DESC
	`, channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db query failed"})
		return
	}
	defer rows.Close()

	videos := []VideoResponse{}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}

	for rows.Next() {
		var v models.Video
		var chID, chUserID, chName, chDesc sql.NullString
		var chCreatedAt sql.NullTime
		var chAvatarURL sql.NullString
		var thumbnailURL sql.NullString

		err := rows.Scan(
			&v.ID,
			&v.Title,
			&v.Description,
			&v.Status,
			&v.Progress,
			&v.Views,
			&v.HLSPath,
			&thumbnailURL,
			&v.CreatedAt,
			&v.ChannelID,

			&chID,
			&chUserID,
			&chName,
			&chDesc,
			&chCreatedAt,
			&chAvatarURL,
		)
		if err != nil {
			fmt.Println("Scan error:", err)
			continue
		}

		// Handle NULL thumbnail_url
		if thumbnailURL.Valid {
			v.ThumbnailURL = thumbnailURL.String
		} else {
			v.ThumbnailURL = ""
		}

		// Build avatar URL
		avatarURL := ""
		if chAvatarURL.Valid && chAvatarURL.String != "" {
			avatarURL = fmt.Sprintf("%s://%s/avatars/%s", scheme, c.Request.Host, chAvatarURL.String)
		}

		videos = append(videos, VideoResponse{
			Video: v,
			Channel: ChannelResponse{
				ID:          chID.String,
				UserID:      chUserID.String,
				Name:        chName.String,
				Description: chDesc.String,
				CreatedAt:   chCreatedAt.Time,
				AvatarURL:   avatarURL,
			},
		})
	}

	if videos == nil {
		videos = []VideoResponse{}
	}

	c.JSON(http.StatusOK, videos)
}



func (s *Server) streamVideo(c *gin.Context) {
	videoID := c.Param("id")
	requestPath := c.Param("filepath") // includes /master.m3u8 or /0/segment.ts

	// 1. Check video exists + status
	var status string
	err := s.db.QueryRow(
		"SELECT status FROM videos WHERE id=$1",
		videoID,
	).Scan(&status)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
		return
	}

	if status != "ready" {
		c.JSON(http.StatusForbidden, gin.H{"error": "video not ready"})
		return
	}

	// 2. Build full path
	baseDir := filepath.Join(os.Getenv("HOME"), "giltube/output", videoID)
	fullPath := filepath.Join(baseDir, requestPath)

	// 🔥 IMPORTANT: prevent path traversal attack
	if !strings.HasPrefix(fullPath, baseDir) {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid path"})
		return
	}

	// 3. Serve file
	c.File(fullPath)
}

func (s *Server) getVideo(c *gin.Context) {
	id := c.Param("id")

	type ChannelResponse struct {
		ID          string `json:"id"`
		UserID      string `json:"user_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		CreatedAt   time.Time `json:"created_at"`
		AvatarURL   string `json:"avatar_url"`
	}

	type VideoResponse struct {
		models.Video
		Channel ChannelResponse `json:"channel"`
	}

	var v models.Video
	var ch models.Channel

	err := s.db.QueryRow(`
		SELECT 
			v.id,
			v.title,
			v.description,
			v.status,
			COALESCE(v.views, 0),
			v.hls_path,
			v.thumbnail_url,
			v.created_at,
			v.channel_id,
			c.id,
			c.user_id,
			c.name,
			c.description,
			c.created_at,
			c.avatar_url
		FROM videos v
		LEFT JOIN channels c ON v.channel_id = c.id
		WHERE v.id=$1
	`, id).Scan(
		&v.ID,
		&v.Title,
		&v.Description,
		&v.Status,
		&v.Views,
		&v.HLSPath,
		&v.ThumbnailURL,
		&v.CreatedAt,
		&v.ChannelID,

		&ch.ID,
		&ch.UserID,
		&ch.Name,
		&ch.Description,
		&ch.CreatedAt,
		&ch.AvatarURL,
	)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	// Build avatar URL
	avatarURL := ""
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if ch.AvatarURL.Valid && ch.AvatarURL.String != "" {
		avatarURL = fmt.Sprintf("%s://%s/avatars/%s", scheme, c.Request.Host, ch.AvatarURL.String)
	}

	c.JSON(http.StatusOK, VideoResponse{
		Video: v,
		Channel: ChannelResponse{
			ID:          ch.ID,
			UserID:      ch.UserID,
			Name:        ch.Name,
			Description: ch.Description,
			CreatedAt:   ch.CreatedAt,
			AvatarURL:   avatarURL,
		},
	})
}

func (s *Server) getChannelVideos(c *gin.Context) {
	channelID := c.Param("channel_id")

	type ChannelResponse struct {
		ID          string `json:"id"`
		UserID      string `json:"user_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		CreatedAt   time.Time `json:"created_at"`
		AvatarURL   string `json:"avatar_url"`
	}

	type VideoResponse struct {
		models.Video
		Channel ChannelResponse `json:"channel"`
	}

	rows, err := s.db.Query(`
		SELECT 
			v.id,
			v.title,
			v.description,
			v.status,
			COALESCE(v.views, 0),
			v.hls_path,
			v.thumbnail_url,
			v.created_at,
			v.channel_id,
			c.id,
			c.user_id,
			c.name,
			c.description,
			c.created_at,
			c.avatar_url
		FROM videos v
		JOIN channels c ON v.channel_id = c.id
		WHERE v.channel_id=$1 AND v.status='ready'
		ORDER BY v.created_at DESC
	`, channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db query failed"})
		return
	}
	defer rows.Close()

	videos := []VideoResponse{}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}

	for rows.Next() {
		var v models.Video
		var chID, chUserID, chName, chDesc sql.NullString
		var chCreatedAt sql.NullTime
		var chAvatarURL sql.NullString

		err := rows.Scan(
			&v.ID,
			&v.Title,
			&v.Description,
			&v.Status,
			&v.Views,
			&v.HLSPath,
			&v.ThumbnailURL,
			&v.CreatedAt,
			&v.ChannelID,

			&chID,
			&chUserID,
			&chName,
			&chDesc,
			&chCreatedAt,
			&chAvatarURL,
		)
		if err != nil {
			continue
		}

		// Build avatar URL
		avatarURL := ""
		if chAvatarURL.Valid && chAvatarURL.String != "" {
			avatarURL = fmt.Sprintf("%s://%s/avatars/%s", scheme, c.Request.Host, chAvatarURL.String)
		}

		videos = append(videos, VideoResponse{
			Video: v,
			Channel: ChannelResponse{
				ID:          chID.String,
				UserID:      chUserID.String,
				Name:        chName.String,
				Description: chDesc.String,
				CreatedAt:   chCreatedAt.Time,
				AvatarURL:   avatarURL,
			},
		})
	}

	if videos == nil {
		videos = []VideoResponse{}
	}

	c.JSON(http.StatusOK, videos)
}

func (s *Server) downloadVideo(c *gin.Context) {
	videoID := c.Param("id")
	quality := c.DefaultQuery("quality", "1080p")

	// Get video from database
	var video models.Video
	err := s.db.QueryRow(
		"SELECT id, title, status FROM videos WHERE id = $1",
		videoID,
	).Scan(&video.ID, &video.Title, &video.Status)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
		return
	}

	// Only allow download of processed videos
	if video.Status != "ready" && video.Status != "published" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "video is not ready for download"})
		return
	}

	// Build paths
	homeDir := os.Getenv("HOME")
	videoDir := filepath.Join(homeDir, "giltube/output", videoID, quality)
	playlistPath := filepath.Join(videoDir, "playlist.m3u8")

	// Check if playlist exists
	if _, err := os.Stat(playlistPath); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, gin.H{"error": "video quality not found"})
		return
	}

	// Prepare output file path
	outputDir := filepath.Join(homeDir, "giltube/downloads")
	os.MkdirAll(outputDir, 0755)
	outputFile := filepath.Join(outputDir, fmt.Sprintf("%s_%s.mp4", videoID, quality))

	// Check if file already exists and is recent (less than 1 hour old)
	fileInfo, err := os.Stat(outputFile)
	if err == nil && fileInfo.ModTime().Add(1*time.Hour).After(time.Now().UTC()) {
		// File exists and is recent, serve it immediately
		fmt.Println("Serving cached download:", outputFile)
		c.FileAttachment(outputFile, fmt.Sprintf("%s.mp4", video.Title))
		return
	}

	// File doesn't exist or is old, queue a download job
	err = s.queue.EnqueueDownload(queue.DownloadJob{
		VideoID: videoID,
		Quality: quality,
	})

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to queue download"})
		return
	}

	// Return status response with polling endpoint
	c.JSON(http.StatusAccepted, gin.H{
		"status": "processing",
		"message": "Your download is being prepared. Please check back in a moment.",
		"check_url": fmt.Sprintf("/api/v1/videos/%s/download-status?quality=%s", videoID, quality),
	})
}

func (s *Server) getDownloadStatus(c *gin.Context) {
	videoID := c.Param("id")
	quality := c.DefaultQuery("quality", "1080p")

	homeDir := os.Getenv("HOME")
	outputDir := filepath.Join(homeDir, "giltube/downloads")
	outputFile := filepath.Join(outputDir, fmt.Sprintf("%s_%s.mp4", videoID, quality))

	// Check if file exists
	fileInfo, err := os.Stat(outputFile)
	if err == nil && fileInfo.Size() > 0 {
		// File is ready - return a direct download endpoint instead of static route
		c.JSON(http.StatusOK, gin.H{
			"status": "ready",
			"message": "Your download is ready",
			"file_url": fmt.Sprintf("/api/v1/downloads/%s/%s", videoID, quality),
		})
		return
	}

	// Still processing
	c.JSON(http.StatusOK, gin.H{
		"status": "processing",
		"message": "Your download is still being prepared.",
	})
}

func (s *Server) serveDownload(c *gin.Context) {
	videoID := c.Param("videoID")
	quality := c.Param("quality")

	homeDir := os.Getenv("HOME")
	outputDir := filepath.Join(homeDir, "giltube/downloads")
	outputFile := filepath.Join(outputDir, fmt.Sprintf("%s_%s.mp4", videoID, quality))

	// Security: prevent path traversal
	if !strings.HasPrefix(outputFile, outputDir) {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid path"})
		return
	}

	// Check if file exists
	fileInfo, err := os.Stat(outputFile)
	if err != nil || fileInfo.Size() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "download not found"})
		return
	}

	// Serve the file
	c.FileAttachment(outputFile, fmt.Sprintf("%s_%s.mp4", videoID, quality))
}

func (s *Server) deleteVideo(c *gin.Context) {
	videoID := c.Param("id")

	// Get video from database to find HLS path and thumbnail
	var video models.Video
	err := s.db.QueryRow(
		"SELECT id, hls_path, thumbnail_url FROM videos WHERE id = $1",
		videoID,
	).Scan(&video.ID, &video.HLSPath, &video.ThumbnailURL)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
		return
	}

	// Delete video from database
	_, err = s.db.Exec("DELETE FROM videos WHERE id = $1", videoID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete video"})
		return
	}

	// Clean up HLS files
	if video.HLSPath != "" {
		homeDir := os.Getenv("HOME")
		hlsPath := filepath.Join(homeDir, "giltube/output", videoID)
		if _, err := os.Stat(hlsPath); err == nil {
			os.RemoveAll(hlsPath)
		}
	}

	// Clean up download files
	homeDir := os.Getenv("HOME")
	downloadsDir := filepath.Join(homeDir, "giltube/downloads")
	if files, err := os.ReadDir(downloadsDir); err == nil {
		for _, file := range files {
			if strings.HasPrefix(file.Name(), videoID+"_") {
				os.Remove(filepath.Join(downloadsDir, file.Name()))
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "video deleted successfully"})
}

func (s *Server) incrementViews(c *gin.Context) {
	videoID := c.Param("id")

	// Increment views in database
	err := db.IncrementVideoViews(s.db, videoID)
	if err != nil {
		fmt.Println("Failed to increment views:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to increment views"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "views incremented"})
}

