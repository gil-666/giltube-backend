package api

import (
	"net/http"
	"time"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gil/giltube/internal/models"
	"github.com/gil/giltube/internal/transcoder"
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
		ThumbnailURL: "/videos/" + videoID + "/thumbnail.jpg",
		ChannelID: c.PostForm("channel_id"),
	}
	_, err = s.db.Exec(
	"INSERT INTO videos (id, title, description, status, created_at, channel_id) VALUES ($1, $2, $3, $4, $5, $6)",
	video.ID,
	video.Title,
	video.Description,
	video.Status,
	video.CreatedAt,
	video.ChannelID,
	)

	
	if err != nil {
	fmt.Println("DB ERROR:", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	return
	}


	go transcoder.ProcessVideo(path, video.ID)

	c.JSON(http.StatusCreated, video)
}

func (s *Server) listVideos(c *gin.Context) {
	rows, err := s.db.Query(
		"SELECT id, title, description, status, channel_id, thumbnail_url, created_at FROM videos",
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db query failed"})
		return
	}
	defer rows.Close()

	videos := []models.Video{}

	for rows.Next() {
		var v models.Video
		err := rows.Scan(
			&v.ID,
			&v.Title,
			&v.Description,
			&v.Status,
			&v.ChannelID,
			&v.ThumbnailURL,
			&v.CreatedAt,
		)
		if err != nil {
			continue
		}
		videos = append(videos, v)
	}

	c.JSON(http.StatusOK, videos)
}
