package api

import (
	"net/http"
	"time"
	"fmt"
	"strings"
	"path/filepath"
	"os"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gil/giltube/internal/models"
	"github.com/gil/giltube/internal/queue"
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
		CreatedAt: time.Now(),
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
	type VideoResponse struct {
		models.Video
		Channel models.Channel `json:"channel"`
	}

	rows, err := s.db.Query(`
		SELECT 
			v.id,
			v.title,
			v.description,
			v.status,
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
		WHERE v.status='ready'
		ORDER BY v.created_at DESC
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db query failed"})
		return
	}
	defer rows.Close()

	videos := []VideoResponse{}

	for rows.Next() {
		var v models.Video
		var ch models.Channel

		err := rows.Scan(
			&v.ID,
			&v.Title,
			&v.Description,
			&v.Status,
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
			continue
		}

		videos = append(videos, VideoResponse{
			Video:   v,
			Channel: ch,
		})
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

	type VideoResponse struct {
		models.Video
		Channel models.Channel `json:"channel"`
	}

	var v models.Video
	var ch models.Channel

	err := s.db.QueryRow(`
		SELECT 
			v.id,
			v.title,
			v.description,
			v.status,
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

	c.JSON(http.StatusOK, VideoResponse{
		Video:   v,
		Channel: ch,
	})
}



