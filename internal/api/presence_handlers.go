package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gil/giltube/internal/presence"
)

type joinRequest struct {
	ViewerID  string `json:"viewer_id" binding:"required"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	Anonymous bool   `json:"anonymous"`
}

// joinPresence handles join/heartbeat requests. It creates or refreshes a presence entry.
func (s *Server) joinPresence(c *gin.Context) {
	videoID := c.Param("video_id")
	var req joinRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Try form values
		req.ViewerID = c.PostForm("viewer_id")
		req.Name = c.PostForm("name")
		req.AvatarURL = c.PostForm("avatar_url")
		req.Anonymous = c.PostForm("anonymous") == "true"
		if req.ViewerID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "viewer_id is required"})
			return
		}
	}

	viewer := presence.Viewer{
		ID: req.ViewerID,
		Name: req.Name,
		AvatarURL: req.AvatarURL,
		Anonymous: req.Anonymous,
	}

	// Keep TTL comfortably above the client heartbeat interval.
	if err := s.presence.AddViewer(videoID, viewer, 75); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add presence"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// leavePresence removes a viewer from presence list
func (s *Server) leavePresence(c *gin.Context) {
	videoID := c.Param("video_id")
	viewerID := c.Query("viewer_id")
	if viewerID == "" {
		viewerID = c.PostForm("viewer_id")
	}
	if viewerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "viewer_id is required"})
		return
	}

	if err := s.presence.RemoveViewer(videoID, viewerID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to remove presence"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// presenceStream streams presence events as Server-Sent Events (SSE).
func (s *Server) presenceStream(c *gin.Context) {
	videoID := c.Param("video_id")
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming unsupported"})
		return
	}

	// Send initial snapshot
	viewers, anonCount, err := s.presence.GetViewers(videoID)
	if err == nil {
		initPayload := map[string]interface{}{
			"type": "init",
			"viewers": viewers,
			"anonymous_count": anonCount,
			"now": time.Now().Unix(),
		}
		b, _ := json.Marshal(initPayload)
		fmt.Fprintf(c.Writer, "data: %s\n\n", b)
		flusher.Flush()
	}

	pubsub := s.presence.SubscribeEvents(videoID)
	defer pubsub.Close()

	ch := pubsub.Channel()
	clientGone := c.Request.Context().Done()

	for {
		select {
		case <-clientGone:
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			// Forward pubsub message as SSE
			fmt.Fprintf(c.Writer, "data: %s\n\n", msg.Payload)
			flusher.Flush()
		}
	}
}
