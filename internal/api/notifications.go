package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type notificationCreateInput struct {
	RecipientUserID  string
	ActorChannelID   string
	ActorUserID      string
	Type             string
	RelatedVideoID   *string
	RelatedCommentID *string
	Metadata         map[string]interface{}
}

type pushSubscribeRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

type pushUnsubscribeRequest struct {
	Endpoint string `json:"endpoint"`
}

type markNotificationReadRequest struct {
	IsRead bool `json:"is_read"`
}

func (s *Server) listNotifications(c *gin.Context) {
	userID, ok := s.requireUserID(c)
	if !ok {
		return
	}

	limit := parseInt(c.Query("limit"), 20)
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	offset := parseInt(c.Query("offset"), 0)
	if offset < 0 {
		offset = 0
	}

	unreadOnly := false
	if raw := strings.TrimSpace(c.Query("unread_only")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid unread_only value"})
			return
		}
		unreadOnly = parsed
	}

	query := `
		SELECT
			n.id,
			n.type,
			n.is_read,
			n.created_at,
			ac.id,
			ac.name,
			COALESCE(ac.avatar_url, ''),
			COALESCE(ac.verified, false),
			v.id,
			COALESCE(v.title, ''),
			cm.id,
			COALESCE(cm.text, '')
		FROM notifications n
		JOIN channels ac ON ac.id = n.actor_channel_id
		LEFT JOIN videos v ON v.id = n.related_video_id
		LEFT JOIN comments cm ON cm.id = n.related_comment_id
		WHERE n.recipient_user_id = $1
	`

	args := []interface{}{userID}
	argPos := 2
	if unreadOnly {
		query += fmt.Sprintf(" AND n.is_read = $%d", argPos)
		args = append(args, false)
		argPos++
	}

	query += fmt.Sprintf(" ORDER BY n.created_at DESC, n.id DESC LIMIT $%d OFFSET $%d", argPos, argPos+1)
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list notifications"})
		return
	}
	defer rows.Close()

	items := make([]gin.H, 0)
	for rows.Next() {
		var (
			id, eventType, actorChannelID, actorName, actorAvatar string
			isRead                                                bool
			createdAt                                             time.Time
			actorVerified                                         bool
			videoID, videoTitle, commentID, commentText           sql.NullString
		)

		if err := rows.Scan(
			&id,
			&eventType,
			&isRead,
			&createdAt,
			&actorChannelID,
			&actorName,
			&actorAvatar,
			&actorVerified,
			&videoID,
			&videoTitle,
			&commentID,
			&commentText,
		); err != nil {
			continue
		}

		item := gin.H{
			"id":         id,
			"type":       eventType,
			"is_read":    isRead,
			"created_at": createdAt.UTC().Format(time.RFC3339),
			"actor_channel": gin.H{
				"id":         actorChannelID,
				"name":       actorName,
				"avatar_url": normalizeAvatarURL(actorAvatar),
				"verified":   actorVerified,
			},
			"target_video":   nil,
			"target_comment": nil,
		}

		if videoID.Valid {
			item["target_video"] = gin.H{
				"id":    videoID.String,
				"title": videoTitle.String,
			}
		}

		if commentID.Valid {
			snippet := strings.TrimSpace(commentText.String)
			if len(snippet) > 120 {
				snippet = snippet[:120] + "..."
			}
			item["target_comment"] = gin.H{
				"id":      commentID.String,
				"snippet": snippet,
			}
		}

		item["url"] = buildNotificationURL(eventType, videoID, commentID)
		items = append(items, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"items":  items,
		"limit":  limit,
		"offset": offset,
	})
}

func buildNotificationURL(eventType string, videoID sql.NullString, commentID sql.NullString) string {
	if videoID.Valid && commentID.Valid {
		return fmt.Sprintf("/video/%s?comment=%s", videoID.String, commentID.String)
	}
	if videoID.Valid {
		return fmt.Sprintf("/video/%s", videoID.String)
	}
	return "/notifications"
}

func (s *Server) getUnreadNotificationCount(c *gin.Context) {
	userID, ok := s.requireUserID(c)
	if !ok {
		return
	}

	var unreadCount int
	err := s.db.QueryRow(
		"SELECT COUNT(1) FROM notifications WHERE recipient_user_id = $1 AND is_read = false",
		userID,
	).Scan(&unreadCount)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get unread count"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"unread_count": unreadCount})
}

func (s *Server) markNotificationRead(c *gin.Context) {
	userID, ok := s.requireUserID(c)
	if !ok {
		return
	}

	notificationID := strings.TrimSpace(c.Param("id"))
	if notificationID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "notification id is required"})
		return
	}

	var req markNotificationReadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	res, err := s.db.Exec(
		`UPDATE notifications
		 SET is_read = $1, updated_at = NOW()
		 WHERE id = $2 AND recipient_user_id = $3`,
		req.IsRead,
		notificationID,
		userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update notification"})
		return
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "notification not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": notificationID, "is_read": req.IsRead})
}

func (s *Server) markAllNotificationsRead(c *gin.Context) {
	userID, ok := s.requireUserID(c)
	if !ok {
		return
	}

	res, err := s.db.Exec(
		`UPDATE notifications
		 SET is_read = true, updated_at = NOW()
		 WHERE recipient_user_id = $1 AND is_read = false`,
		userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark notifications as read"})
		return
	}

	updated, _ := res.RowsAffected()
	c.JSON(http.StatusOK, gin.H{"updated": updated})
}

func (s *Server) subscribePush(c *gin.Context) {
	userID, ok := s.requireUserID(c)
	if !ok {
		return
	}

	var req pushSubscribeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.Keys.P256DH = strings.TrimSpace(req.Keys.P256DH)
	req.Keys.Auth = strings.TrimSpace(req.Keys.Auth)
	if req.Endpoint == "" || req.Keys.P256DH == "" || req.Keys.Auth == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "endpoint and keys are required"})
		return
	}

	userAgent := strings.TrimSpace(c.GetHeader("User-Agent"))
	_, err := s.db.Exec(
		`INSERT INTO push_subscriptions (id, recipient_user_id, endpoint, p256dh_key, auth_key, user_agent, is_active, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, true, NOW(), NOW())
		 ON CONFLICT (endpoint)
		 DO UPDATE SET
		 	recipient_user_id = EXCLUDED.recipient_user_id,
		 	p256dh_key = EXCLUDED.p256dh_key,
		 	auth_key = EXCLUDED.auth_key,
		 	user_agent = EXCLUDED.user_agent,
		 	is_active = true,
		 	updated_at = NOW()`,
		uuid.New().String(),
		userID,
		req.Endpoint,
		req.Keys.P256DH,
		req.Keys.Auth,
		userAgent,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save push subscription"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "push subscription saved"})
}

func (s *Server) getPushConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"enabled":         s.cfg.PushEnabled,
		"send_enabled":    s.cfg.PushSendEnabled,
		"vapid_public_key": s.cfg.VAPIDPublicKey,
	})
}

func (s *Server) unsubscribePush(c *gin.Context) {
	userID, ok := s.requireUserID(c)
	if !ok {
		return
	}

	var req pushUnsubscribeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	req.Endpoint = strings.TrimSpace(req.Endpoint)
	if req.Endpoint == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "endpoint is required"})
		return
	}

	res, err := s.db.Exec(
		`UPDATE push_subscriptions
		 SET is_active = false, updated_at = NOW()
		 WHERE recipient_user_id = $1 AND endpoint = $2`,
		userID,
		req.Endpoint,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to deactivate push subscription"})
		return
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "push subscription deactivated"})
}

func (s *Server) createNotification(input notificationCreateInput) error {
	if input.RecipientUserID == "" || input.ActorChannelID == "" || input.Type == "" {
		return nil
	}
	if input.ActorUserID != "" && input.ActorUserID == input.RecipientUserID {
		return nil
	}

	metadataJSON := []byte("{}")
	if input.Metadata != nil {
		encoded, err := json.Marshal(input.Metadata)
		if err == nil {
			metadataJSON = encoded
		}
	}

	_, err := s.db.Exec(
		`INSERT INTO notifications (
			id,
			recipient_user_id,
			actor_channel_id,
			type,
			related_video_id,
			related_comment_id,
			is_read,
			metadata,
			created_at,
			updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, false, $7::jsonb, NOW(), NOW())
		ON CONFLICT ON CONSTRAINT idx_notifications_dedupe_window DO NOTHING`,
		uuid.New().String(),
		input.RecipientUserID,
		input.ActorChannelID,
		input.Type,
		input.RelatedVideoID,
		input.RelatedCommentID,
		string(metadataJSON),
	)
	if err != nil {
		// If dedupe constraint name is not available in older postgres versions,
		// fall back to a plain insert-on-conflict target.
		_, fallbackErr := s.db.Exec(
			`INSERT INTO notifications (
				id,
				recipient_user_id,
				actor_channel_id,
				type,
				related_video_id,
				related_comment_id,
				is_read,
				metadata,
				created_at,
				updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, false, $7::jsonb, NOW(), NOW())
			ON CONFLICT DO NOTHING`,
			uuid.New().String(),
			input.RecipientUserID,
			input.ActorChannelID,
			input.Type,
			input.RelatedVideoID,
			input.RelatedCommentID,
			string(metadataJSON),
		)
		if fallbackErr != nil {
			return fallbackErr
		}
	}

	return nil
}
