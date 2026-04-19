package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gil/giltube/internal/models"
)

// saveChannelAvatar saves an uploaded avatar file and returns the relative path
func saveChannelAvatar(c *gin.Context, channelID string) (string, error) {
	// Create avatars directory if it doesn't exist
	avatarDir := filepath.Join(os.Getenv("HOME"), "giltube", "giltube-backend", "data", "avatars")
	if err := os.MkdirAll(avatarDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create avatar directory: %w", err)
	}

	// Get file from request
	file, err := c.FormFile("avatar")
	if err != nil {
		return "", nil // Avatar is optional
	}

	// Generate filename using channel ID and timestamp
	ext := filepath.Ext(file.Filename)
	filename := fmt.Sprintf("%s_%d%s", channelID, time.Now().UTC().Unix(), ext)
	filepath := filepath.Join(avatarDir, filename)

	// Save file to disk
	if err := c.SaveUploadedFile(file, filepath); err != nil {
		return "", fmt.Errorf("failed to save avatar: %w", err)
	}

	// Return just the filename for the database, but we'll convert to URL when returning in responses
	return filename, nil
}

// channelResponse converts a Channel model to a response format with string avatar_url
func channelResponse(c *gin.Context, ch models.Channel) gin.H {
	avatarURL := ""
	if ch.AvatarURL.Valid {
		filename := ch.AvatarURL.String
		// Handle both old format (full path) and new format (just filename)
		if !strings.HasPrefix(filename, "/avatars/") {
			// Just filename - prepend /avatars/
			avatarURL = fmt.Sprintf("/avatars/%s", filename)
		} else {
			// Already has /avatars/ - use as is
			avatarURL = filename
		}
	}
	
	return gin.H{
		"id":          ch.ID,
		"user_id":     ch.UserID,
		"name":        ch.Name,
		"description": ch.Description,
		"created_at":  ch.CreatedAt,
		"avatar_url":  avatarURL,
	}
}

func (s *Server) createChannel(c *gin.Context) {
	userID := c.PostForm("user_id")
	name := c.PostForm("name")
	description := c.PostForm("description")

	if userID == "" || name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id and name are required"})
		return
	}

	channel := models.Channel{
		ID:          uuid.New().String(),
		UserID:      userID,
		Name:        name,
		Description: description,
		CreatedAt:   time.Now().UTC(),
	}

	// Try to save avatar if provided
	avatarPath, err := saveChannelAvatar(c, channel.ID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to save avatar: %v", err)})
		return
	}
	
	// Set avatar_url as sql.NullString
	if avatarPath != "" {
		channel.AvatarURL = sql.NullString{String: avatarPath, Valid: true}
	}

	_, err = s.db.Exec(
		"INSERT INTO channels (id, user_id, name, description, avatar_url, created_at) VALUES ($1,$2,$3,$4,$5,$6)",
		channel.ID, channel.UserID, channel.Name, channel.Description, channel.AvatarURL.String, channel.CreatedAt,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, channelResponse(c, channel))
}

func (s *Server) listUserChannels(c *gin.Context) {
	userID := c.Param("user_id")

	rows, err := s.db.Query(
		"SELECT id, user_id, name, description, created_at, avatar_url FROM channels WHERE user_id=$1 ORDER BY created_at DESC",
		userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query failed"})
		return
	}
	defer rows.Close()

	var channels []models.Channel
	for rows.Next() {
		var channel models.Channel
		if err := rows.Scan(&channel.ID, &channel.UserID, &channel.Name, &channel.Description, &channel.CreatedAt, &channel.AvatarURL); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scan failed"})
			return
		}
		channels = append(channels, channel)
	}

	if channels == nil {
		channels = []models.Channel{}
	}

	// Convert to response format with proper avatar URLs
	var response []gin.H
	for _, ch := range channels {
		response = append(response, channelResponse(c, ch))
	}

	c.JSON(http.StatusOK, response)
}

func (s *Server) updateChannel(c *gin.Context) {
	channelID := c.Param("channel_id")
	name := c.PostForm("name")
	description := c.PostForm("description")

	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	// Start building the UPDATE query
	avatarPath, err := saveChannelAvatar(c, channelID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to save avatar: %v", err)})
		return
	}

	// If avatar was uploaded, update all fields including avatar
	if avatarPath != "" {
		_, err = s.db.Exec(
			"UPDATE channels SET name=$1, description=$2, avatar_url=$3 WHERE id=$4",
			name, description, avatarPath, channelID,
		)
	} else {
		// If no avatar, update only name and description
		_, err = s.db.Exec(
			"UPDATE channels SET name=$1, description=$2 WHERE id=$3",
			name, description, channelID,
		)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Fetch and return the updated channel
	var channel models.Channel
	err = s.db.QueryRow(
		"SELECT id, user_id, name, description, created_at, avatar_url FROM channels WHERE id=$1",
		channelID,
	).Scan(&channel.ID, &channel.UserID, &channel.Name, &channel.Description, &channel.CreatedAt, &channel.AvatarURL)
	
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch updated channel"})
		return
	}

	c.JSON(http.StatusOK, channelResponse(c, channel))
}

func (s *Server) deleteChannel(c *gin.Context) {
	channelID := c.Param("channel_id")

	_, err := s.db.Exec("DELETE FROM channels WHERE id=$1", channelID)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "channel deleted"})
}

func (s *Server) getChannelInfo(c *gin.Context) {
	channelID := c.Param("channel_id")

	var channel models.Channel
	var username string

	// Get channel with owner username
	err := s.db.QueryRow(
		"SELECT c.id, c.user_id, c.name, c.description, c.created_at, c.avatar_url, u.username FROM channels c JOIN users u ON c.user_id = u.id WHERE c.id=$1",
		channelID,
	).Scan(&channel.ID, &channel.UserID, &channel.Name, &channel.Description, &channel.CreatedAt, &channel.AvatarURL, &username)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"channel":        channelResponse(c, channel),
		"owner_username": username,
	})
}
