package api

import (
	"io"
	"bytes"
	"net/http"
	"time"
	"fmt"
	"strings"
	"strconv"
	"path/filepath"
	"os"
	"mime/multipart"
	"database/sql"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gil/giltube/internal/models"
	"github.com/gil/giltube/internal/queue"
	"github.com/gil/giltube/internal/db"
)

func parseInt(value string, defaultVal int) int {
	if value == "" {
		return defaultVal
	}
	num, err := strconv.Atoi(value)
	if err != nil {
		return defaultVal
	}
	return num
}


func (s *Server) uploadVideo(c *gin.Context) {
	file, err := c.FormFile("video")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no file"})
		return
	}

	path := filepath.Join(os.TempDir(), file.Filename)

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

	categoryIDsStr := c.PostForm("category_ids")
	if categoryIDsStr != "" {
		categoryIDs := strings.Split(categoryIDsStr, ",")
		var validCategoryIDs []string
		for _, id := range categoryIDs {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				validCategoryIDs = append(validCategoryIDs, trimmed)
			}
		}
		
		if len(validCategoryIDs) > 0 {
			if err := db.AssignCategoriesToVideo(s.db, video.ID, validCategoryIDs); err != nil {
				fmt.Println("Category assignment error:", err)
				// Continue anyway, don't fail the upload
			}
		}
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
		Verified    bool `json:"verified"`
	}

	type VideoResponse struct {
		models.Video
		Channel ChannelResponse `json:"channel"`
	}

	limit := 12
	offset := 0
	if l := c.DefaultQuery("limit", ""); l != "" {
		if parsed := parseInt(l, 12); parsed > 0 {
			limit = parsed
			if limit > 100 {
				limit = 100
			}
		}
	}
	if o := c.DefaultQuery("offset", ""); o != "" {
		if parsed := parseInt(o, 0); parsed >= 0 {
			offset = parsed
		}
	}

	rows, err := s.db.Query(`
		SELECT 
			v.id,
			v.title,
			v.description,
			v.status,
			COALESCE(v.views, 0),
			v.hls_path,
			COALESCE(v.thumbnail_url, ''),
			COALESCE(v.has_custom_thumbnail, false),
			COALESCE(v.explicit, false),
			COALESCE(v.width, 0),
			v.created_at,
			v.channel_id,
			c.id,
			c.user_id,
			c.name,
			c.description,
			c.created_at,
			c.avatar_url,
			COALESCE(c.verified, false)
		FROM videos v
		LEFT JOIN channels c ON v.channel_id = c.id
		WHERE v.status = 'ready' AND (v.hidden IS NULL OR v.hidden = false)
		ORDER BY v.created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db query failed"})
		return
	}
	defer rows.Close()

	videos := []VideoResponse{}

	for rows.Next() {
		var v models.Video
		var chID, chUserID, chName, chDesc sql.NullString
		var chCreatedAt sql.NullTime
		var chAvatarURL sql.NullString
		var chVerified sql.NullBool

		err := rows.Scan(
			&v.ID,
			&v.Title,
			&v.Description,
			&v.Status,
			&v.Views,
			&v.HLSPath,
			&v.ThumbnailURL,
			&v.HasCustomThumbnail,
			&v.Explicit,
			&v.Width,
			&v.CreatedAt,
			&v.ChannelID,

			&chID,
			&chUserID,
			&chName,
			&chDesc,
			&chCreatedAt,
			&chAvatarURL,
			&chVerified,
		)
		if err != nil {
			fmt.Println("Scan error:", err)
			continue
		}

		// Build avatar URL
		avatarURL := ""
		if chAvatarURL.Valid && chAvatarURL.String != "" {
			avatarURL = fmt.Sprintf("/avatars/%s", chAvatarURL.String)
		}

		// Fetch categories for this video
		categories, err := db.GetVideoCategories(s.db, v.ID)
		if err == nil && categories != nil {
			for _, cat := range categories {
				v.Categories = append(v.Categories, models.Category{
					ID:          cat["id"].(string),
					Name:        cat["name"].(string),
					Slug:        cat["slug"].(string),
					Description: cat["description"].(string),
				})
			}
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
				Verified:    chVerified.Bool,
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

	channelID := c.Query("channel_id")
	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

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
		Verified    bool `json:"verified"`
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
			COALESCE(v.progress, 0),
			COALESCE(v.views, 0),
			COALESCE((SELECT COUNT(*) FROM likes WHERE video_id = v.id), 0),
			v.hls_path,
			v.thumbnail_url,
			COALESCE(v.has_custom_thumbnail, false),
		COALESCE(v.explicit, false),
		COALESCE(v.width, 0),
		v.created_at,
		v.channel_id,
		c.id,
		c.user_id,
		c.name,
		c.description,
		c.created_at,
		c.avatar_url,
		COALESCE(c.verified, false)
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

	for rows.Next() {
		var v models.Video
		var chID, chUserID, chName, chDesc sql.NullString
		var chCreatedAt sql.NullTime
		var chAvatarURL sql.NullString
		var thumbnailURL sql.NullString
		var chVerified sql.NullBool

		err := rows.Scan(
			&v.ID,
			&v.Title,
			&v.Description,
			&v.Status,
			&v.Progress,
			&v.Views,
			&v.Likes,
			&v.HLSPath,
			&thumbnailURL,
			&v.HasCustomThumbnail,
			&v.Explicit,
			&v.Width,
			&v.CreatedAt,
			&v.ChannelID,

			&chID,
			&chUserID,
			&chName,
			&chDesc,
			&chCreatedAt,
			&chAvatarURL,
			&chVerified,
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
			avatarURL = fmt.Sprintf("/avatars/%s", chAvatarURL.String)
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
				Verified:    chVerified.Bool,
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
	requestPath := c.Param("filepath")

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

	baseDir := filepath.Join(os.Getenv("HOME"), "giltube/output", videoID)
	fullPath := filepath.Join(baseDir, requestPath)

	if !strings.HasPrefix(fullPath, baseDir) {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid path"})
		return
	}

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
		Verified    bool `json:"verified"`
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
			COALESCE(v.progress, 0),
			COALESCE(v.views, 0),
			COALESCE((SELECT COUNT(*) FROM likes WHERE video_id = v.id), 0),
			COALESCE(v.hls_path, ''),
			COALESCE(v.thumbnail_url, ''),
			COALESCE(v.has_custom_thumbnail, false) as has_custom_thumbnail,
		COALESCE(v.explicit, false),
		COALESCE(v.width, 0),
		v.created_at,
		v.channel_id,
		c.id,
		c.user_id,
		c.name,
		c.description,
		c.created_at,
		c.avatar_url,
		COALESCE(c.verified, false)
		FROM videos v
		LEFT JOIN channels c ON v.channel_id = c.id
		WHERE v.id=$1
	`, id).Scan(
		&v.ID,
		&v.Title,
		&v.Description,
		&v.Status,
		&v.Progress,
		&v.Views,
		&v.Likes,
		&v.HLSPath,
		&v.ThumbnailURL,
		&v.HasCustomThumbnail,
		&v.Explicit,
		&v.Width,
		&v.CreatedAt,
		&v.ChannelID,

		&ch.ID,
		&ch.UserID,
		&ch.Name,
		&ch.Description,
		&ch.CreatedAt,
		&ch.AvatarURL,
		&ch.Verified,
	)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	// Build avatar URL
	avatarURL := ""
	if ch.AvatarURL.Valid && ch.AvatarURL.String != "" {
		avatarURL = fmt.Sprintf("/avatars/%s", ch.AvatarURL.String)
	}

	fmt.Printf("DEBUG getVideo response: id=%s, explicit=%v\n", v.ID, v.Explicit)
	
	// Fetch categories for this video
	categories, err := db.GetVideoCategories(s.db, v.ID)
	if err == nil && categories != nil {
		// Convert map slice to Category slice
		for _, cat := range categories {
			v.Categories = append(v.Categories, models.Category{
				ID:          cat["id"].(string),
				Name:        cat["name"].(string),
				Slug:        cat["slug"].(string),
				Description: cat["description"].(string),
				CreatedAt:   time.Now(), // This is fetched separately
			})
		}
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
			Verified:    ch.Verified,
		},
	})
}

func (s *Server) likeVideo(c *gin.Context) {
	videoID := c.Param("id")
	channelID := c.Query("channel_id")
	
	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	liked, err := db.CheckIfLiked(s.db, videoID, channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "check failed"})
		return
	}
	if liked {
		c.JSON(http.StatusConflict, gin.H{"error": "already liked"})
		return
	}

	likeID := uuid.New().String()
	err = db.CreateLike(s.db, likeID, videoID, channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "like failed"})
		return
	}

	likesCount, _ := db.GetLikesCount(s.db, videoID)
	c.JSON(http.StatusOK, gin.H{"likes": likesCount, "liked": true})
}

func (s *Server) unlikeVideo(c *gin.Context) {
	videoID := c.Param("id")
	channelID := c.Query("channel_id")
	
	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	liked, err := db.CheckIfLiked(s.db, videoID, channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "check failed"})
		return
	}
	if !liked {
		c.JSON(http.StatusConflict, gin.H{"error": "not liked"})
		return
	}

	err = db.DeleteLike(s.db, videoID, channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "unlike failed"})
		return
	}

	likesCount, _ := db.GetLikesCount(s.db, videoID)
	c.JSON(http.StatusOK, gin.H{"likes": likesCount, "liked": false})
}

func (s *Server) checkIfLiked(c *gin.Context) {
	videoID := c.Param("id")
	channelID := c.Query("channel_id")
	
	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	liked, err := db.CheckIfLiked(s.db, videoID, channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "check failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"liked": liked})
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
		Verified    bool `json:"verified"`
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
			COALESCE(v.has_custom_thumbnail, false),
		COALESCE(v.explicit, false),
		COALESCE(v.width, 0),
		v.created_at,
		v.channel_id,
		c.id,
		c.user_id,
		c.name,
		c.description,
		c.created_at,
		c.avatar_url,
		COALESCE(c.verified, false)
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

	for rows.Next() {
		var v models.Video
		var chID, chUserID, chName, chDesc sql.NullString
		var chCreatedAt sql.NullTime
		var chAvatarURL sql.NullString
		var chVerified sql.NullBool

		err := rows.Scan(
			&v.ID,
			&v.Title,
			&v.Description,
			&v.Status,
			&v.Views,
			&v.HLSPath,
			&v.ThumbnailURL,
			&v.HasCustomThumbnail,
			&v.Explicit,
			&v.Width,
			&v.CreatedAt,
			&v.ChannelID,

			&chID,
			&chUserID,
			&chName,
			&chDesc,
			&chCreatedAt,
			&chAvatarURL,
			&chVerified,
		)
		if err != nil {
			continue
		}

		// Build avatar URL
		avatarURL := ""
		if chAvatarURL.Valid && chAvatarURL.String != "" {
			avatarURL = fmt.Sprintf("/avatars/%s", chAvatarURL.String)
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
				Verified:    chVerified.Bool,
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

func (s *Server) reEncodeVideo(c *gin.Context) {
	videoID := c.Param("id")
	
	// Check if video exists
	var video models.Video
	err := s.db.QueryRow("SELECT id, status FROM videos WHERE id = $1", videoID).Scan(&video.ID, &video.Status)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
		return
	}
	
	// Get file path from request (user must provide the original video file)
	filePath := c.PostForm("file_path")
	
	// If no file path provided, try to get uploaded file
	if filePath == "" {
		file, err := c.FormFile("video")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "must provide file_path or upload video file"})
			return
		}
		
		// Save uploaded file to temp
		tempPath := filepath.Join(os.TempDir(), fmt.Sprintf("%s-%s", videoID, file.Filename))
		if err := c.SaveUploadedFile(file, tempPath); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save file"})
			return
		}
		filePath = tempPath
	} else {
		// Verify file exists if path provided
		if _, err := os.Stat(filePath); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "file_path does not exist"})
			return
		}
	}
	
	// Queue for re-encoding
	err = s.queue.Enqueue(queue.Job{VideoID: videoID, FilePath: filePath})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to queue video"})
		return
	}
	
	// Update video status
	_, err = s.db.Exec("UPDATE videos SET status = $1, progress = 0 WHERE id = $2", "processing", videoID)
	if err != nil {
		fmt.Printf("Warning: failed to update video status: %v\n", err)
	}
	
	c.JSON(http.StatusOK, gin.H{
		"message": "video queued for re-encoding",
		"video_id": videoID,
		"file_path": filePath,
	})
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

func (s *Server) getChannelAnalytics(c *gin.Context) {
	channelID := c.Param("channel_id")

	// Public endpoint - no authentication required
	analytics, err := db.GetChannelAnalytics(s.db, channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch analytics"})
		return
	}

	c.JSON(http.StatusOK, analytics)
}

func (s *Server) uploadChunk(c *gin.Context) {
	// Get the chunk file
	file, err := c.FormFile("chunk")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no chunk file provided"})
		return
	}

	// Get or create upload ID - accept both camelCase and snake_case
	uploadID := c.PostForm("uploadSessionId")
	if uploadID == "" {
		uploadID = c.PostForm("upload_id")
	}
	if uploadID == "" {
		uploadID = c.Query("uploadSessionId")
	}
	if uploadID == "" {
		uploadID = c.Query("upload_id")
	}
	// Generate new upload ID if not provided
	if uploadID == "" {
		uploadID = uuid.New().String()
	}

	// Get chunk index - accept both camelCase and snake_case
	chunkIndex := c.PostForm("chunkIndex")
	if chunkIndex == "" {
		chunkIndex = c.PostForm("chunk_index")
	}
	if chunkIndex == "" {
		chunkIndex = c.Query("chunkIndex")
	}
	if chunkIndex == "" {
		chunkIndex = c.Query("chunk_index")
	}
	if chunkIndex == "" {
		chunkIndex = "0"
	}

	// Create upload directory if it doesn't exist
	uploadsDir := filepath.Join("/tmp", "giltube-uploads", uploadID)
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create upload directory"})
		return
	}

	// Save chunk with index as filename
	chunkPath := filepath.Join(uploadsDir, fmt.Sprintf("chunk_%s", chunkIndex))
	if err := c.SaveUploadedFile(file, chunkPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save chunk"})
		return
	}

	fmt.Printf("Saved chunk %s for upload %s (size: %d bytes)\n", chunkIndex, uploadID, file.Size)

	// Return the upload_id so frontend can use it for subsequent requests
	c.JSON(http.StatusOK, gin.H{
		"upload_id": uploadID,
		"chunk_index": chunkIndex,
		"message": "chunk uploaded successfully",
	})
}

func (s *Server) finalizeUpload(c *gin.Context) {
	// Accept both camelCase and snake_case parameter names
	uploadID := c.PostForm("uploadSessionId")
	if uploadID == "" {
		uploadID = c.PostForm("upload_id")
	}
	if uploadID == "" {
		uploadID = c.Query("uploadSessionId")
	}
	if uploadID == "" {
		uploadID = c.Query("upload_id")
	}
	if uploadID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "upload_id/uploadSessionId is required"})
		return
	}

	title := c.PostForm("title")
	if title == "" {
		title = c.Query("title")
	}
	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}

	description := c.PostForm("description")
	if description == "" {
		description = c.Query("description")
	}

	categoryIDs := c.PostFormArray("category_ids[]")
	if len(categoryIDs) == 0 {
		categoryIDs = c.QueryArray("category_ids[]")
	}
	if len(categoryIDs) == 0 {
		categoryIDsStr := c.PostForm("category_ids")
		if categoryIDsStr == "" {
			categoryIDsStr = c.Query("category_ids")
		}
		if categoryIDsStr != "" {
			parts := strings.Split(categoryIDsStr, ",")
			for _, part := range parts {
				trimmed := strings.TrimSpace(part)
				if trimmed != "" {
					categoryIDs = append(categoryIDs, trimmed)
				}
			}
		}
	}

	// Get explicit flag
	explicitStr := c.PostForm("explicit")
	explicit := explicitStr == "true"

	// Accept both camelCase and snake_case for channel_id
	channelID := c.PostForm("channel_id")
	if channelID == "" {
		channelID = c.PostForm("channelId")
	}
	if channelID == "" {
		channelID = c.Query("channel_id")
	}
	if channelID == "" {
		channelID = c.Query("channelId")
	}
	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id/channelId is required"})
		return
	}

	// Check channel status - reject if suspended or banned
	var channelStatus string
	err := s.db.QueryRow("SELECT COALESCE(status, 'active') FROM channels WHERE id = $1", channelID).Scan(&channelStatus)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
		return
	}
	if channelStatus != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "channel is " + channelStatus + " and cannot upload videos"})
		return
	}

	// Check user status - allow upload if active or suspended, reject if banned
	var userID string
	err = s.db.QueryRow("SELECT user_id FROM channels WHERE id = $1", channelID).Scan(&userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "channel owner not found"})
		return
	}

	var userStatus string
	err = s.db.QueryRow("SELECT COALESCE(status, 'active') FROM users WHERE id = $1", userID).Scan(&userStatus)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if userStatus == "banned" {
		c.JSON(http.StatusForbidden, gin.H{"error": "user account is banned and cannot upload videos"})
		return
	}

	// Get all chunks from upload directory
	uploadsDir := filepath.Join("/tmp", "giltube-uploads", uploadID)
	
	// List all chunks in the directory
	entries, err := os.ReadDir(uploadsDir)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "upload not found or expired"})
		return
	}

	if len(entries) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no chunks found for this upload"})
		return
	}

	// Create final video file
	videoID := uuid.New().String()
	finalPath := filepath.Join("/tmp", fmt.Sprintf("%s.mp4", videoID))

	// Combine chunks into final file
	finalFile, err := os.Create(finalPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create final file"})
		return
	}
	defer finalFile.Close()

	// Write each chunk to the final file
	for i := 0; i < len(entries); i++ {
		chunkPath := filepath.Join(uploadsDir, fmt.Sprintf("chunk_%d", i))
		chunkFile, err := os.Open(chunkPath)
		if err != nil {
			// Chunks don't need to be sequential, just combine all chunk_* files
			continue
		}

		if _, err := io.Copy(finalFile, chunkFile); err != nil {
			chunkFile.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to combine chunks"})
			return
		}
		chunkFile.Close()
	}

	// Create video record in database
	video := models.Video{
		ID:          videoID,
		Title:       title,
		Description: description,
		Status:      "uploaded",
		CreatedAt:   time.Now().UTC(),
		ChannelID:   channelID,
		Explicit:    explicit,
	}

	// Handle thumbnail upload if provided
	var thumbnailURL string
	var hasCustomThumbnail bool
	
	thumbnailFile, err := c.FormFile("thumbnail")
	if err == nil && thumbnailFile != nil {
		// Save custom thumbnail using same path as edit-video
		homeDir := os.Getenv("HOME")
		thumbnailsDir := filepath.Join(homeDir, "giltube/output", videoID)
		if err := os.MkdirAll(thumbnailsDir, 0755); err == nil {
			// Save with timestamped name
			thumbnailName := fmt.Sprintf("custom_thumbnail_%d.jpg", time.Now().Unix())
			thumbnailPath := filepath.Join(thumbnailsDir, thumbnailName)
			
			if err := c.SaveUploadedFile(thumbnailFile, thumbnailPath); err == nil {
				// Set thumbnail URL and mark as custom
				thumbnailURL = fmt.Sprintf("/videos/%s/%s", videoID, thumbnailName)
				hasCustomThumbnail = true
			}
		}
	}

	_, err = s.db.Exec(
		"INSERT INTO videos (id, title, description, status, created_at, channel_id, hls_path, explicit, thumbnail_url, has_custom_thumbnail) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)",
		video.ID,
		video.Title,
		video.Description,
		video.Status,
		video.CreatedAt,
		channelID,
		video.HLSPath,
		explicit,
		thumbnailURL,
		hasCustomThumbnail,
	)

	if err != nil {
		fmt.Println("DB ERROR:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save video record"})
		return
	}

	_ = db.AssignCategoriesToVideo(s.db, video.ID, categoryIDs)

	// Queue video for processing
	err = s.queue.Enqueue(queue.Job{VideoID: video.ID, FilePath: finalPath})
	if err != nil {
		fmt.Println("Queue ERROR:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to queue video for processing"})
		return
	}

	// Clean up upload directory
	os.RemoveAll(uploadsDir)

	fmt.Printf("Finalized upload %s as video %s (title: %s)\n", uploadID, videoID, title)

	c.JSON(http.StatusCreated, gin.H{
		"video_id": video.ID,
		"title": video.Title,
		"status": video.Status,
		"created_at": video.CreatedAt,
	})
}

func (s *Server) updateVideo(c *gin.Context) {
	videoID := c.Param("id")
	userID := c.GetHeader("X-User-ID")
	
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// Get video to verify ownership and current state
	var video models.Video
	var channelID string
	err := s.db.QueryRow(
		"SELECT id, channel_id FROM videos WHERE id = $1",
		videoID,
	).Scan(&video.ID, &channelID)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
		return
	}

	// Verify user owns this video via channel ownership
	var channelUserID string
	err = s.db.QueryRow(
		"SELECT user_id FROM channels WHERE id = $1",
		channelID,
	).Scan(&channelUserID)

	if err != nil || channelUserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}

	// Get form data - handle both JSON and form-data
	var req struct {
		Title                  string `json:"title" form:"title"`
		Description            string `json:"description" form:"description"`
		RevertToAutoThumbnail  bool   `json:"revert_to_auto_thumbnail" form:"revert_to_auto_thumbnail"`
		Explicit               *bool  `json:"explicit" form:"explicit"`
		CategoryIDs            string `json:"category_ids" form:"category_ids"`
	}
	
	// Try parsing as JSON first, then fall back to form
	contentType := c.ContentType()
	fmt.Printf("DEBUG: contentType=%q\n", contentType)
	
	// Read body for debugging
	bodyBytes, _ := io.ReadAll(c.Request.Body)
	fmt.Printf("DEBUG: body=%s\n", string(bodyBytes))
	
	// Reset body for actual parsing
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	
	if strings.Contains(contentType, "application/json") {
		if err := c.BindJSON(&req); err != nil {
			fmt.Printf("DEBUG JSON parse error: %v\n", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
			return
		}
	} else {
		// Parse as form data
		req.Title = c.PostForm("title")
		req.Description = c.PostForm("description")
		req.RevertToAutoThumbnail = c.PostForm("revert_to_auto_thumbnail") == "true"
		req.CategoryIDs = c.PostForm("category_ids")
		
		// Parse explicit as boolean from form
		explicitStr := c.PostForm("explicit")
		if explicitStr != "" {
			explicitBool := explicitStr == "true"
			req.Explicit = &explicitBool
		}
	}

	// Get thumbnail file if uploaded (only available with form data)
	var thumbnailFile *multipart.FileHeader
	if !strings.Contains(contentType, "application/json") {
		thumbnailFile, _ = c.FormFile("thumbnail")
	}
	
	fmt.Printf("DEBUG updateVideo: title=%q, hasFile=%v, revert=%v, explicit=%v\n", req.Title, thumbnailFile != nil, req.RevertToAutoThumbnail, req.Explicit)
	
	// Build update data
	updateFields := []string{}
	updateArgs := []interface{}{}
	argCount := 1

	if req.Title != "" {
		updateFields = append(updateFields, fmt.Sprintf("title = $%d", argCount))
		updateArgs = append(updateArgs, req.Title)
		argCount++
	}

	if req.Description != "" {
		updateFields = append(updateFields, fmt.Sprintf("description = $%d", argCount))
		updateArgs = append(updateArgs, req.Description)
		argCount++
	}

	// Handle explicit flag (18+ content warning)
	if req.Explicit != nil {
		updateFields = append(updateFields, fmt.Sprintf("explicit = $%d", argCount))
		updateArgs = append(updateArgs, *req.Explicit)
		argCount++
	}

	// Handle thumbnail upload
	if err == nil && thumbnailFile != nil {
		// Save custom thumbnail
		homeDir := os.Getenv("HOME")
		thumbnailsDir := filepath.Join(homeDir, "giltube/output", videoID)
		if err := os.MkdirAll(thumbnailsDir, 0755); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create thumbnail directory"})
			return
		}

		// Save with unique name
		thumbnailName := fmt.Sprintf("custom_thumbnail_%d.jpg", time.Now().Unix())
		thumbnailPath := filepath.Join(thumbnailsDir, thumbnailName)
		
		if err := c.SaveUploadedFile(thumbnailFile, thumbnailPath); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save thumbnail"})
			return
		}

		// Update database with thumbnail path
		thumbnailURL := fmt.Sprintf("/videos/%s/%s", videoID, thumbnailName)
		updateFields = append(updateFields, fmt.Sprintf("thumbnail_url = $%d", argCount))
		updateArgs = append(updateArgs, thumbnailURL)
		argCount++

		// Always mark as custom thumbnail when uploading a new one
		updateFields = append(updateFields, fmt.Sprintf("has_custom_thumbnail = $%d", argCount))
		updateArgs = append(updateArgs, true)
		argCount++
	} else if req.RevertToAutoThumbnail {
		// Clear custom thumbnail and revert to auto-generated
		autoThumbnailURL := fmt.Sprintf("/videos/%s/thumbnail.jpg", videoID)
		updateFields = append(updateFields, fmt.Sprintf("thumbnail_url = $%d", argCount))
		updateArgs = append(updateArgs, autoThumbnailURL)
		argCount++
		
		updateFields = append(updateFields, fmt.Sprintf("has_custom_thumbnail = $%d", argCount))
		updateArgs = append(updateArgs, false)
		argCount++
	}

	if len(updateFields) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	// Add video ID as last parameter
	updateArgs = append(updateArgs, videoID)
	
	// Build and execute update query
	query := fmt.Sprintf("UPDATE videos SET %s WHERE id = $%d", 
		strings.Join(updateFields, ", "), argCount)
	
	fmt.Printf("DEBUG updateVideo: query=%s, args=%v\n", query, updateArgs)
	
	_, err = s.db.Exec(query, updateArgs...)
	if err != nil {
		fmt.Println("Update error:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update video"})
		return
	}

	// Handle category updates if provided
	if req.CategoryIDs != "" {
		categoryIDs := strings.Split(req.CategoryIDs, ",")
		var validCategoryIDs []string
		for _, id := range categoryIDs {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				validCategoryIDs = append(validCategoryIDs, trimmed)
			}
		}
		
		if len(validCategoryIDs) > 0 {
			if err := db.AssignCategoriesToVideo(s.db, videoID, validCategoryIDs); err != nil {
				fmt.Println("Category assignment error:", err)
				// Continue anyway, don't fail the update
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "video updated successfully"})
}

