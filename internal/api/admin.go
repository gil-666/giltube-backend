package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Middleware to check if user is admin
func (s *Server) isAdmin(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		c.Abort()
		return
	}

	var userType string
	err := s.db.QueryRow(
		"SELECT user_type FROM users WHERE id = $1",
		userID,
	).Scan(&userType)

	if err != nil || userType != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin access required"})
		c.Abort()
		return
	}

	c.Next()
}

// GET /api/admin/users - Get all users with stats
func (s *Server) getAdminUsers(c *gin.Context) {
	rows, err := s.db.Query(`
		SELECT 
			u.id,
			u.username,
			u.email,
			u.user_type,
			COALESCE(u.status, 'active'),
			u.created_at,
			COUNT(DISTINCT ch.id) as channel_count,
			COUNT(DISTINCT v.id) as video_count,
			COALESCE(SUM(v.views), 0) as total_views
		FROM users u
		LEFT JOIN channels ch ON u.id = ch.user_id
		LEFT JOIN videos v ON ch.id = v.channel_id
		GROUP BY u.id, u.username, u.email, u.user_type, u.status, u.created_at
		ORDER BY u.created_at DESC
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	type UserStats struct {
		ID           string `json:"id"`
		Username     string `json:"username"`
		Email        string `json:"email"`
		UserType     string `json:"user_type"`
		Status       string `json:"status"`
		CreatedAt    string `json:"created_at"`
		ChannelCount int    `json:"channel_count"`
		VideoCount   int    `json:"video_count"`
		TotalViews   int64  `json:"total_views"`
	}

	var users []UserStats
	for rows.Next() {
		var user UserStats
		err := rows.Scan(&user.ID, &user.Username, &user.Email, &user.UserType, &user.Status, &user.CreatedAt, &user.ChannelCount, &user.VideoCount, &user.TotalViews)
		if err != nil {
			continue
		}
		users = append(users, user)
	}

	c.JSON(http.StatusOK, users)
}

// POST /api/admin/users/:id/toggle-admin - Toggle user admin status
func (s *Server) toggleAdminStatus(c *gin.Context) {
	userID := c.Param("id")

	// Get current type
	var currentType string
	err := s.db.QueryRow("SELECT user_type FROM users WHERE id = $1", userID).Scan(&currentType)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	newType := "user"
	if currentType == "user" {
		newType = "admin"
	}

	_, err = s.db.Exec("UPDATE users SET user_type = $1 WHERE id = $2", newType, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"user_id": userID, "user_type": newType})
}

// DELETE /api/admin/users/:id - Delete user and associated data
func (s *Server) deleteUser(c *gin.Context) {
	userID := c.Param("id")
	adminID := c.GetString("user_id")

	// Prevent self-deletion
	if userID == adminID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot delete your own account"})
		return
	}

	if err := s.scrubAndDeleteUser(userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "user deleted successfully"})
}

// GET /api/admin/stats - Get platform statistics
func (s *Server) getAdminStats(c *gin.Context) {
	type Stats struct {
		TotalUsers      int64 `json:"total_users"`
		TotalChannels   int64 `json:"total_channels"`
		TotalVideos     int64 `json:"total_videos"`
		TotalViews      int64 `json:"total_views"`
		TotalComments   int64 `json:"total_comments"`
		AdminCount      int64 `json:"admin_count"`
		TotalCategories int64 `json:"total_categories"`
	}

	var stats Stats

	queries := []struct {
		query string
		dest  *int64
	}{
		{"SELECT COUNT(*) FROM users", &stats.TotalUsers},
		{"SELECT COUNT(*) FROM channels", &stats.TotalChannels},
		{"SELECT COUNT(*) FROM videos", &stats.TotalVideos},
		{"SELECT COALESCE(SUM(views), 0) FROM videos", &stats.TotalViews},
		{"SELECT COUNT(*) FROM comments", &stats.TotalComments},
		{"SELECT COUNT(*) FROM users WHERE user_type = 'admin'", &stats.AdminCount},
		{"SELECT COUNT(*) FROM categories", &stats.TotalCategories},
	}

	for _, q := range queries {
		err := s.db.QueryRow(q.query).Scan(q.dest)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, stats)
}

// PUT /api/admin/videos/:id/verify - Verify/feature a video
func (s *Server) verifyVideo(c *gin.Context) {
	videoID := c.Param("id")

	_, err := s.db.Exec("UPDATE videos SET verified = true WHERE id = $1", videoID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "video verified"})
}

// GET /api/admin/channels - Get all channels
func (s *Server) getAdminChannels(c *gin.Context) {
	rows, err := s.db.Query(`
		SELECT 
			ch.id,
			ch.name,
			ch.user_id,
			u.username,
			ch.created_at,
			COALESCE(ch.status, 'active'),
			COUNT(DISTINCT v.id) as video_count,
			COALESCE(SUM(v.views), 0) as total_views
		FROM channels ch
		JOIN users u ON ch.user_id = u.id
		LEFT JOIN videos v ON ch.id = v.channel_id
		GROUP BY ch.id, ch.name, ch.user_id, u.username, ch.created_at, ch.status
		ORDER BY total_views DESC
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	type ChannelStats struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		UserID    string `json:"user_id"`
		Username  string `json:"username"`
		CreatedAt string `json:"created_at"`
		Status    string `json:"status"`
		VideoCount int    `json:"video_count"`
		TotalViews int64  `json:"total_views"`
	}

	var channels []ChannelStats
	for rows.Next() {
		var ch ChannelStats
		err := rows.Scan(&ch.ID, &ch.Name, &ch.UserID, &ch.Username, &ch.CreatedAt, &ch.Status, &ch.VideoCount, &ch.TotalViews)
		if err != nil {
			continue
		}
		channels = append(channels, ch)
	}

	c.JSON(http.StatusOK, channels)
}

// POST /api/admin/users/:id/suspend - Suspend a user
func (s *Server) suspendUser(c *gin.Context) {
	userID := c.Param("id")
	adminID := c.GetString("user_id")

	// Prevent admin from suspending themselves
	if userID == adminID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot suspend yourself"})
		return
	}

	_, err := s.db.Exec("UPDATE users SET status = $1 WHERE id = $2", "suspended", userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "user suspended", "user_id": userID, "status": "suspended"})
}

// POST /api/admin/users/:id/ban - Ban a user
func (s *Server) banUser(c *gin.Context) {
	userID := c.Param("id")
	adminID := c.GetString("user_id")

	// Prevent admin from banning themselves
	if userID == adminID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot ban yourself"})
		return
	}

	// Start transaction to ban user and hide their videos
	tx, err := s.db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	// Ban user
	_, err = tx.Exec("UPDATE users SET status = $1 WHERE id = $2", "banned", userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Hide all videos from banned user's channels
	_, err = tx.Exec(`
		UPDATE videos SET hidden = true 
		WHERE channel_id IN (SELECT id FROM channels WHERE user_id = $1)
	`, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "user banned", "user_id": userID, "status": "banned"})
}

// POST /api/admin/users/:id/unsuspend - Unsuspend a user
func (s *Server) unsuspendUser(c *gin.Context) {
	userID := c.Param("id")

	_, err := s.db.Exec("UPDATE users SET status = $1 WHERE id = $2", "active", userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "user unsuspended", "user_id": userID, "status": "active"})
}

// POST /api/admin/channels/:id/suspend - Suspend a channel
func (s *Server) suspendChannel(c *gin.Context) {
	channelID := c.Param("id")

	_, err := s.db.Exec("UPDATE channels SET status = $1 WHERE id = $2", "suspended", channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "channel suspended", "channel_id": channelID, "status": "suspended"})
}

// POST /api/admin/channels/:id/ban - Ban a channel
func (s *Server) banChannel(c *gin.Context) {
	channelID := c.Param("id")

	// Start transaction to ban channel and hide its videos
	tx, err := s.db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	// Ban channel
	_, err = tx.Exec("UPDATE channels SET status = $1 WHERE id = $2", "banned", channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Hide all videos in banned channel
	_, err = tx.Exec("UPDATE videos SET hidden = true WHERE channel_id = $1", channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "channel banned", "channel_id": channelID, "status": "banned"})
}

// POST /api/admin/channels/:id/unsuspend - Unsuspend a channel
func (s *Server) unsuspendChannel(c *gin.Context) {
	channelID := c.Param("id")

	_, err := s.db.Exec("UPDATE channels SET status = $1 WHERE id = $2", "active", channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "channel unsuspended", "channel_id": channelID, "status": "active"})
}

// POST /api/admin/users/:id/unban - Unban a user and unhide their videos
func (s *Server) unbanUser(c *gin.Context) {
	userID := c.Param("id")

	// Start transaction to unban user and unhide their videos
	tx, err := s.db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	// Unban user
	_, err = tx.Exec("UPDATE users SET status = $1 WHERE id = $2", "active", userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Unhide all videos from unbanned user's channels
	_, err = tx.Exec(`
		UPDATE videos SET hidden = false 
		WHERE channel_id IN (SELECT id FROM channels WHERE user_id = $1)
	`, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "user unbanned", "user_id": userID, "status": "active"})
}

// POST /api/admin/channels/:id/unban - Unban a channel and unhide its videos
func (s *Server) unbanChannel(c *gin.Context) {
	channelID := c.Param("id")

	// Start transaction to unban channel and unhide its videos
	tx, err := s.db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	// Unban channel
	_, err = tx.Exec("UPDATE channels SET status = $1 WHERE id = $2", "active", channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Unhide all videos in unbanned channel
	_, err = tx.Exec("UPDATE videos SET hidden = false WHERE channel_id = $1", channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "channel unbanned", "channel_id": channelID, "status": "active"})
}
