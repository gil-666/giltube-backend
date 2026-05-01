package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SherClockHolmes/webpush-go"
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

type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Type  string `json:"type"`
	Icon  string `json:"icon"`
	Image string `json:"image"`
}

func (s *Server) notifyLiveStarted(channelID, actorUserID, title, description string) error {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil
	}

	rows, err := s.db.Query(`
		SELECT id
		FROM users
		WHERE COALESCE(status, 'active') = 'active'
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	metadata := map[string]interface{}{
		"live_channel_id": channelID,
		"title":           strings.TrimSpace(title),
		"description":     strings.TrimSpace(description),
	}

	for rows.Next() {
		var recipientUserID string
		if err := rows.Scan(&recipientUserID); err != nil {
			continue
		}
		if strings.TrimSpace(recipientUserID) == "" || recipientUserID == strings.TrimSpace(actorUserID) {
			continue
		}

		_ = s.createNotification(notificationCreateInput{
			RecipientUserID: recipientUserID,
			ActorChannelID:  channelID,
			ActorUserID:     actorUserID,
			Type:            "live_started",
			Metadata:        metadata,
		})
	}

	return rows.Err()
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

		item["url"] = buildNotificationURL(eventType, actorChannelID, videoID, commentID)
		items = append(items, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"items":  items,
		"limit":  limit,
		"offset": offset,
	})
}

func buildNotificationURL(eventType string, actorChannelID string, videoID sql.NullString, commentID sql.NullString) string {
	if eventType == "live_started" && strings.TrimSpace(actorChannelID) != "" {
		return fmt.Sprintf("/live/%s", actorChannelID)
	}
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
		ON CONFLICT (
			recipient_user_id,
			actor_channel_id,
			type,
			COALESCE(related_video_id, ''),
			COALESCE(related_comment_id, ''),
			minute_bucket
		) DO NOTHING`,
		uuid.New().String(),
		input.RecipientUserID,
		input.ActorChannelID,
		input.Type,
		input.RelatedVideoID,
		input.RelatedCommentID,
		string(metadataJSON),
	)
	if err != nil {
		return err
	}

	if err := s.sendNotificationPush(input); err != nil {
		log.Printf("push dispatch summary recipient=%s type=%s err=%v", input.RecipientUserID, input.Type, err)
	}

	return nil
}

func (s *Server) sendNotificationPush(input notificationCreateInput) error {
	if !s.cfg.PushEnabled || !s.cfg.PushSendEnabled {
		return nil
	}
	if strings.TrimSpace(s.cfg.VAPIDPublicKey) == "" || strings.TrimSpace(s.cfg.VAPIDPrivateKey) == "" {
		return nil
	}

	videoID := sql.NullString{}
	commentID := sql.NullString{}
	if input.RelatedVideoID != nil {
		videoID.Valid = true
		videoID.String = *input.RelatedVideoID
	}
	if input.RelatedCommentID != nil {
		commentID.Valid = true
		commentID.String = *input.RelatedCommentID
	}

	var actorName, actorAvatar string
	_ = s.db.QueryRow("SELECT name, COALESCE(avatar_url, '') FROM channels WHERE id = $1", input.ActorChannelID).Scan(&actorName, &actorAvatar)
	if strings.TrimSpace(actorName) == "" {
		actorName = "Someone"
	}
	avatarURL := normalizeAvatarURL(actorAvatar)
	avatarPushURL := s.toAbsolutePublicURL(avatarURL)

	payload := pushPayload{
		Title: "Giltube",
		Body:  summarizePushBody(input.Type, actorName),
		URL:   buildNotificationURL(input.Type, input.ActorChannelID, videoID, commentID),
		Type:  input.Type,
		Icon:  avatarPushURL,
		Image: avatarPushURL,
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	rows, err := s.db.Query(
		`SELECT endpoint, p256dh_key, auth_key
		 FROM push_subscriptions
		 WHERE recipient_user_id = $1 AND is_active = true`,
		input.RecipientUserID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	options := &webpush.Options{
		Subscriber:      s.cfg.VAPIDSubject,
		VAPIDPublicKey:  s.cfg.VAPIDPublicKey,
		VAPIDPrivateKey: s.cfg.VAPIDPrivateKey,
		TTL:             30,
	}

	attempted := 0
	delivered := 0
	failed := 0

	for rows.Next() {
		var endpoint, p256dhKey, authKey string
		if err := rows.Scan(&endpoint, &p256dhKey, &authKey); err != nil {
			continue
		}
		attempted++

		subscription := &webpush.Subscription{
			Endpoint: endpoint,
			Keys: webpush.Keys{
				P256dh: p256dhKey,
				Auth:   authKey,
			},
		}

		resp, err := webpush.SendNotification(payloadJSON, subscription, options)
		if err != nil {
			failed++
			log.Printf("push send failed endpoint=%s err=%v", endpoint, err)
			if resp != nil {
				respBody := ""
				if b, readErr := io.ReadAll(resp.Body); readErr == nil {
					respBody = strings.TrimSpace(string(b))
				}
				log.Printf("push send response endpoint=%s status=%d body=%s", endpoint, resp.StatusCode, respBody)
				if resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound {
					_, _ = s.db.Exec(
						"UPDATE push_subscriptions SET is_active = false, updated_at = NOW() WHERE endpoint = $1",
						endpoint,
					)
				}
				_ = resp.Body.Close()
			}
			continue
		}

		if resp != nil {
			respBody := ""
			if b, readErr := io.ReadAll(resp.Body); readErr == nil {
				respBody = strings.TrimSpace(string(b))
			}
			log.Printf("push send response endpoint=%s status=%d body=%s", endpoint, resp.StatusCode, respBody)
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				delivered++
			} else {
				failed++
			}
			if resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound {
				_, _ = s.db.Exec(
					"UPDATE push_subscriptions SET is_active = false, updated_at = NOW() WHERE endpoint = $1",
					endpoint,
				)
			}
			_ = resp.Body.Close()
		}
	}

	if err := rows.Err(); err != nil {
		return err
	}

	if attempted == 0 {
		return fmt.Errorf("no active push subscriptions")
	}
	if delivered == 0 {
		return fmt.Errorf("push delivery failed for all subscriptions (attempted=%d failed=%d)", attempted, failed)
	}

	return nil
}

func (s *Server) toAbsolutePublicURL(path string) string {
	raw := strings.TrimSpace(path)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	origins := strings.Split(s.cfg.WebAuthnRPOrigins, ",")
	for _, origin := range origins {
		candidate := strings.TrimSpace(origin)
		if candidate == "" {
			continue
		}
		return strings.TrimRight(candidate, "/") + raw
	}
	return raw
}

func summarizePushBody(notificationType, actorName string) string {
	switch notificationType {
	case "comment_video":
		return actorName + " commented on your video"
	case "reply_comment":
		return actorName + " replied to your comment"
	case "like_video":
		return actorName + " liked your video"
	case "like_comment":
		return actorName + " liked your comment"
	case "live_started":
		return actorName + " started a live stream"
	default:
		return actorName + " sent you a notification"
	}
}
