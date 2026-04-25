package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

type userVideoAsset struct {
	videoID      string
	hlsPath      string
	thumbnailURL string
}

func (s *Server) getMyAccount(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var account struct {
		ID        string `json:"id"`
		Username  string `json:"username"`
		Email     string `json:"email"`
		UserType  string `json:"user_type"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
	}

	err := s.db.QueryRow(
		"SELECT id, username, email, user_type, COALESCE(status, 'active'), created_at FROM users WHERE id = $1",
		userID,
	).Scan(&account.ID, &account.Username, &account.Email, &account.UserType, &account.Status, &account.CreatedAt)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	c.JSON(http.StatusOK, account)
}

func (s *Server) updateMyEmail(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var body struct {
		Email           string `json:"email"`
		CurrentPassword string `json:"current_password"`
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	newEmail := strings.ToLower(strings.TrimSpace(body.Email))
	if newEmail == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email is required"})
		return
	}
	if body.CurrentPassword == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "current password is required"})
		return
	}

	var hashedPassword string
	err := s.db.QueryRow("SELECT password FROM users WHERE id = $1", userID).Scan(&hashedPassword)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(body.CurrentPassword)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid current password"})
		return
	}

	var existingUserID string
	err = s.db.QueryRow("SELECT id FROM users WHERE LOWER(email) = $1 AND id <> $2", newEmail, userID).Scan(&existingUserID)
	if err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "email already in use"})
		return
	}
	if err != sql.ErrNoRows {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate email"})
		return
	}

	if _, err := s.db.Exec("UPDATE users SET email = $1 WHERE id = $2", newEmail, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update email"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "email updated", "email": newEmail})
}

func (s *Server) updateMyPassword(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	if body.CurrentPassword == "" || body.NewPassword == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "current_password and new_password are required"})
		return
	}

	if len(body.NewPassword) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "new password must be at least 6 characters"})
		return
	}

	var hashedPassword string
	err := s.db.QueryRow("SELECT password FROM users WHERE id = $1", userID).Scan(&hashedPassword)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(body.CurrentPassword)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid current password"})
		return
	}

	newHashedPassword, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "password hash failed"})
		return
	}

	if _, err := s.db.Exec("UPDATE users SET password = $1 WHERE id = $2", string(newHashedPassword), userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update password"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "password updated"})
}

func (s *Server) deleteMyAccount(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var body struct {
		CurrentPassword string `json:"current_password"`
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	if body.CurrentPassword == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "current password is required"})
		return
	}

	var hashedPassword string
	err := s.db.QueryRow("SELECT password FROM users WHERE id = $1", userID).Scan(&hashedPassword)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(body.CurrentPassword)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid current password"})
		return
	}

	if err := s.scrubAndDeleteUser(userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "account deleted"})
}

func (s *Server) scrubAndDeleteUser(userID string) error {
	rows, err := s.db.Query("SELECT id FROM channels WHERE user_id = $1", userID)
	if err != nil {
		return fmt.Errorf("failed to fetch channels: %w", err)
	}
	defer rows.Close()

	channelIDs := []string{}
	for rows.Next() {
		var channelID string
		if err := rows.Scan(&channelID); err != nil {
			return fmt.Errorf("failed to read channel id: %w", err)
		}
		channelIDs = append(channelIDs, channelID)
	}

	videoAssets := make([]userVideoAsset, 0)
	for _, channelID := range channelIDs {
		videoRows, err := s.db.Query("SELECT id, COALESCE(hls_path, ''), COALESCE(thumbnail_url, '') FROM videos WHERE channel_id = $1", channelID)
		if err != nil {
			return fmt.Errorf("failed to fetch video assets: %w", err)
		}

		for videoRows.Next() {
			asset := userVideoAsset{}
			if err := videoRows.Scan(&asset.videoID, &asset.hlsPath, &asset.thumbnailURL); err != nil {
				videoRows.Close()
				return fmt.Errorf("failed to read video asset: %w", err)
			}
			videoAssets = append(videoAssets, asset)
		}

		videoRows.Close()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Personal account interactions (comments/likes posted as personal profile).
	if _, err := tx.Exec("DELETE FROM likes WHERE channel_id = $1", userID); err != nil {
		return fmt.Errorf("failed to delete personal likes: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM comments WHERE channel_id = $1", userID); err != nil {
		return fmt.Errorf("failed to delete personal comments: %w", err)
	}

	for _, channelID := range channelIDs {
		if _, err := tx.Exec("DELETE FROM likes WHERE channel_id = $1", channelID); err != nil {
			return fmt.Errorf("failed to delete channel likes: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM comments WHERE channel_id = $1", channelID); err != nil {
			return fmt.Errorf("failed to delete channel comments: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM likes WHERE video_id IN (SELECT id FROM videos WHERE channel_id = $1)", channelID); err != nil {
			return fmt.Errorf("failed to delete likes on user videos: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM comments WHERE video_id IN (SELECT id FROM videos WHERE channel_id = $1)", channelID); err != nil {
			return fmt.Errorf("failed to delete comments on user videos: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM video_categories WHERE video_id IN (SELECT id FROM videos WHERE channel_id = $1)", channelID); err != nil {
			return fmt.Errorf("failed to delete video categories: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM views WHERE video_id IN (SELECT id FROM videos WHERE channel_id = $1)", channelID); err != nil {
			return fmt.Errorf("failed to delete video views: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM videos WHERE channel_id = $1", channelID); err != nil {
			return fmt.Errorf("failed to delete videos: %w", err)
		}
	}

	if _, err := tx.Exec("DELETE FROM channels WHERE user_id = $1", userID); err != nil {
		return fmt.Errorf("failed to delete channels: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM users WHERE id = $1", userID); err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit account deletion: %w", err)
	}

	// Best-effort disk cleanup after DB deletion.
	for _, asset := range videoAssets {
		s.cleanupVideoAssets(asset)
	}

	return nil
}

func (s *Server) cleanupVideoAssets(asset userVideoAsset) {
	videoDir := extractVideoOutputDir(asset)
	if videoDir != "" {
		_ = os.RemoveAll(videoDir)
	}

	home := os.Getenv("HOME")
	if home == "" || asset.videoID == "" {
		return
	}

	downloadsDir := filepath.Join(home, "giltube", "downloads")
	matches, err := filepath.Glob(filepath.Join(downloadsDir, asset.videoID+"*"))
	if err != nil {
		return
	}
	for _, match := range matches {
		_ = os.RemoveAll(match)
	}
}

func extractVideoOutputDir(asset userVideoAsset) string {
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}

	baseOutput := filepath.Join(home, "giltube", "output")

	candidates := []string{asset.hlsPath, asset.thumbnailURL}
	for _, candidate := range candidates {
		normalized := strings.TrimSpace(candidate)
		if normalized == "" {
			continue
		}
		if strings.HasPrefix(normalized, "/videos/") {
			relative := strings.TrimPrefix(normalized, "/videos/")
			parts := strings.Split(relative, "/")
			if len(parts) > 0 && parts[0] != "" {
				return filepath.Join(baseOutput, parts[0])
			}
		}
	}

	if asset.videoID != "" {
		return filepath.Join(baseOutput, asset.videoID)
	}

	return ""
}
