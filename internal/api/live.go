package api

import (
	"encoding/json"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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
			started_at TIMESTAMPTZ NULL,
			ended_at TIMESTAMPTZ NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		ALTER TABLE live_streams
		ADD COLUMN IF NOT EXISTS use_publisher_presence BOOLEAN NOT NULL DEFAULT FALSE;

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

func (s *Server) resolveLiveState(manualStatus string, usePublisherPresence bool, streamKey string) (bool, bool) {
	manualLive := manualStatus == "live"
	if !usePublisherPresence || strings.TrimSpace(streamKey) == "" {
		return manualLive, false
	}

	publisherLive, err := s.mediamtxPathIsLive("live/" + streamKey)
	if err != nil {
		// Fallback to manual status if presence check fails.
		return manualLive, false
	}
	return publisherLive, publisherLive
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

	err = s.db.QueryRow(`
		SELECT id, channel_id, title, description, stream_key, status, use_publisher_presence, started_at, ended_at, created_at, updated_at
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
		&startedAt,
		&endedAt,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load live stream"})
		return
	}

	isLive, publisherDetectedLive := s.resolveLiveState(status, usePublisherPresence, streamKey)
	resolvedStatus := "offline"
	if isLive {
		resolvedStatus = "live"
	}

	c.JSON(http.StatusOK, gin.H{
		"id":           id,
		"channel_id":   channelID,
		"title":        title,
		"description":  description,
		"stream_key":   streamKey,
		"status":       resolvedStatus,
		"manual_status": status,
		"use_publisher_presence": usePublisherPresence,
		"publisher_detected_live": publisherDetectedLive,
		"started_at":   parseNullTime(startedAt),
		"ended_at":     parseNullTime(endedAt),
		"created_at":   createdAt,
		"updated_at":   updatedAt,
		"ingest_url":   s.liveIngestURL(),
		"ingest_url_local": s.liveLocalIngestURL(),
		"ingest_url_lan": s.liveLanIngestURL(),
		"stream_name":  streamKey,
		"playback_url": s.livePlaybackURL(streamKey),
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
		"message": "publisher presence setting updated",
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
		"message": "live stream settings saved",
		"title": title,
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

	newKey, err := generateStreamKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate stream key"})
		return
	}

	_, err = s.db.Exec(`
		UPDATE live_streams
		SET stream_key = $1, status = 'offline', started_at = NULL, ended_at = NOW(), updated_at = NOW()
		WHERE channel_id = $2
	`, newKey, body.ChannelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rotate stream key"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      "stream key rotated",
		"stream_key":   newKey,
		"ingest_url":   s.liveIngestURL(),
		"ingest_url_local": s.liveLocalIngestURL(),
		"ingest_url_lan": s.liveLanIngestURL(),
		"stream_name":  newKey,
		"playback_url": s.livePlaybackURL(newKey),
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

	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = "Live Stream"
	}
	description := strings.TrimSpace(body.Description)

	_, err = s.db.Exec(`
		UPDATE live_streams
		SET title = $1, description = $2, status = 'live', started_at = NOW(), ended_at = NULL, updated_at = NOW()
		WHERE channel_id = $3
	`, title, description, body.ChannelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start stream"})
		return
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

	_, err = s.db.Exec(`
		UPDATE live_streams
		SET status = 'offline', ended_at = NOW(), updated_at = NOW()
		WHERE channel_id = $1
	`, body.ChannelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to stop stream"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "stream stopped"})
}

func (s *Server) getChannelLiveStatus(c *gin.Context) {
	channelID := c.Param("channel_id")

	var title, description, streamKey, status, channelName string
	var usePublisherPresence bool
	var startedAt, endedAt sql.NullTime
	var channelAvatar sql.NullString
	var channelVerified bool

	err := s.db.QueryRow(`
		SELECT
			COALESCE(ls.title, 'Live Stream'),
			COALESCE(ls.description, ''),
			COALESCE(ls.stream_key, ''),
			COALESCE(ls.status, 'offline'),
			COALESCE(ls.use_publisher_presence, false),
			ls.started_at,
			ls.ended_at,
			c.name,
			COALESCE(c.avatar_url, ''),
			COALESCE(c.verified, false)
		FROM channels c
		LEFT JOIN live_streams ls ON ls.channel_id = c.id
		WHERE c.id = $1
	`, channelID).Scan(
		&title,
		&description,
		&streamKey,
		&status,
		&usePublisherPresence,
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

	isLive, publisherDetectedLive := s.resolveLiveState(status, usePublisherPresence, streamKey)
	resolvedStatus := "offline"
	if isLive {
		resolvedStatus = "live"
	}

	payload := gin.H{
		"channel_id":   channelID,
		"title":        title,
		"description":  description,
		"status":       resolvedStatus,
		"manual_status": status,
		"use_publisher_presence": usePublisherPresence,
		"publisher_detected_live": publisherDetectedLive,
		"started_at":   parseNullTime(startedAt),
		"ended_at":     parseNullTime(endedAt),
		"channel":      gin.H{"id": channelID, "name": channelName, "avatar_url": avatar, "verified": channelVerified},
		"is_live":      isLive,
		"playback_url": "",
		"playback_url_public": "",
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
		var verified bool
		if err := rows.Scan(&channelID, &title, &description, &streamKey, &status, &usePublisherPresence, &startedAt, &channelName, &avatarRaw, &verified); err != nil {
			continue
		}

		isLive, _ := s.resolveLiveState(status, usePublisherPresence, streamKey)
		if !isLive {
			continue
		}

		avatar := ""
		if avatarRaw.Valid {
			avatar = normalizeAvatarURL(avatarRaw.String)
		}

		streams = append(streams, gin.H{
			"channel_id":   channelID,
			"title":        title,
			"description":  description,
			"status":       "live",
			"is_live":      true,
			"use_publisher_presence": usePublisherPresence,
			"started_at":   parseNullTime(startedAt),
			"playback_url": s.livePlaybackURL(streamKey),
			"channel":      gin.H{"id": channelID, "name": channelName, "avatar_url": avatar, "verified": verified},
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

	isLive, _ := s.resolveLiveState(status, usePublisherPresence, streamKey)
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
