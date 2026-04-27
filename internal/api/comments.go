package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gil/giltube/internal/db"
)

func (s *Server) getVideoComments(c *gin.Context) {
	videoID := c.Param("id")
	actorChannelID := strings.TrimSpace(c.Query("channel_id"))

	comments, err := db.GetComments(s.db, videoID, actorChannelID)
	if err != nil {
		fmt.Println("DB error getting comments:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get comments"})
		return
	}

	if comments == nil {
		comments = []map[string]interface{}{}
	}

	c.JSON(http.StatusOK, comments)
}

func (s *Server) createComment(c *gin.Context) {
	videoID := c.Param("id")
	channelID := strings.TrimSpace(c.PostForm("channel_id"))
	text := c.PostForm("text")
	parentCommentID := strings.TrimSpace(c.PostForm("parent_comment_id"))

	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	if text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "comment text is required"})
		return
	}

	if len(text) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "comment too long (max 500 characters)"})
		return
	}

	var parentPtr *string
	if parentCommentID != "" {
		var exists int
		err := s.db.QueryRow(
			"SELECT COUNT(1) FROM comments WHERE id = $1 AND video_id = $2",
			parentCommentID,
			videoID,
		).Scan(&exists)
		if err != nil && err != sql.ErrNoRows {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate parent comment"})
			return
		}
		if exists == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid parent comment"})
			return
		}
		parentPtr = &parentCommentID
	}

	commentID := uuid.New().String()
	err := db.CreateComment(s.db, commentID, videoID, channelID, text, parentPtr)
	if err != nil {
		fmt.Println("DB error creating comment:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create comment"})
		return
	}

	var actorUserID string
	_ = s.db.QueryRow("SELECT user_id FROM channels WHERE id = $1", channelID).Scan(&actorUserID)

	var videoOwnerUserID, videoTitle string
	if err := s.db.QueryRow(
		`SELECT ch.user_id, COALESCE(v.title, '')
		 FROM videos v
		 JOIN channels ch ON ch.id = v.channel_id
		 WHERE v.id = $1`,
		videoID,
	).Scan(&videoOwnerUserID, &videoTitle); err == nil {
		_ = s.createNotification(notificationCreateInput{
			RecipientUserID:  videoOwnerUserID,
			ActorChannelID:   channelID,
			ActorUserID:      actorUserID,
			Type:             "comment_video",
			RelatedVideoID:   &videoID,
			RelatedCommentID: &commentID,
			Metadata: map[string]interface{}{
				"text":        text,
				"video_title": videoTitle,
			},
		})
	}

	if parentCommentID != "" {
		var parentOwnerUserID, parentCommentText, parentVideoID string
		if err := s.db.QueryRow(
			`SELECT ch.user_id, COALESCE(c.text, ''), c.video_id
			 FROM comments c
			 JOIN channels ch ON ch.id = c.channel_id
			 WHERE c.id = $1`,
			parentCommentID,
		).Scan(&parentOwnerUserID, &parentCommentText, &parentVideoID); err == nil {
			_ = s.createNotification(notificationCreateInput{
				RecipientUserID:  parentOwnerUserID,
				ActorChannelID:   channelID,
				ActorUserID:      actorUserID,
				Type:             "reply_comment",
				RelatedVideoID:   &parentVideoID,
				RelatedCommentID: &parentCommentID,
				Metadata: map[string]interface{}{
					"text":              text,
					"parent_comment":    parentCommentText,
					"reply_comment_id":  commentID,
				},
			})
		}
	}

	c.JSON(http.StatusCreated, gin.H{"id": commentID, "message": "comment created"})
}

func (s *Server) deleteComment(c *gin.Context) {
	commentID := c.Param("comment_id")

	err := db.DeleteComment(s.db, commentID)
	if err != nil {
		fmt.Println("DB error deleting comment:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete comment"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "comment deleted"})
}

func (s *Server) likeComment(c *gin.Context) {
	commentID := c.Param("comment_id")
	channelID := strings.TrimSpace(c.Query("channel_id"))

	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	var alreadyLiked int
	err := s.db.QueryRow(
		"SELECT COUNT(1) FROM comment_likes WHERE comment_id = $1 AND channel_id = $2",
		commentID,
		channelID,
	).Scan(&alreadyLiked)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "check failed"})
		return
	}
	if alreadyLiked > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "already liked"})
		return
	}

	_, err = s.db.Exec(
		"INSERT INTO comment_likes (id, comment_id, channel_id, created_at) VALUES ($1, $2, $3, NOW())",
		uuid.New().String(),
		commentID,
		channelID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "like failed"})
		return
	}

	var likesCount int
	_ = s.db.QueryRow("SELECT COUNT(1) FROM comment_likes WHERE comment_id = $1", commentID).Scan(&likesCount)

	var actorUserID string
	_ = s.db.QueryRow("SELECT user_id FROM channels WHERE id = $1", channelID).Scan(&actorUserID)

	var recipientUserID, relatedVideoID, commentText string
	if err := s.db.QueryRow(
		`SELECT ch.user_id, c.video_id, COALESCE(c.text, '')
		 FROM comments c
		 JOIN channels ch ON ch.id = c.channel_id
		 WHERE c.id = $1`,
		commentID,
	).Scan(&recipientUserID, &relatedVideoID, &commentText); err == nil {
		_ = s.createNotification(notificationCreateInput{
			RecipientUserID:  recipientUserID,
			ActorChannelID:   channelID,
			ActorUserID:      actorUserID,
			Type:             "like_comment",
			RelatedVideoID:   &relatedVideoID,
			RelatedCommentID: &commentID,
			Metadata: map[string]interface{}{
				"comment": commentText,
			},
		})
	}

	c.JSON(http.StatusOK, gin.H{"likes": likesCount, "liked": true})
}

func (s *Server) unlikeComment(c *gin.Context) {
	commentID := c.Param("comment_id")
	channelID := strings.TrimSpace(c.Query("channel_id"))

	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	res, err := s.db.Exec(
		"DELETE FROM comment_likes WHERE comment_id = $1 AND channel_id = $2",
		commentID,
		channelID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "unlike failed"})
		return
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "not liked"})
		return
	}

	var likesCount int
	_ = s.db.QueryRow("SELECT COUNT(1) FROM comment_likes WHERE comment_id = $1", commentID).Scan(&likesCount)
	c.JSON(http.StatusOK, gin.H{"likes": likesCount, "liked": false})
}

func (s *Server) checkIfCommentLiked(c *gin.Context) {
	commentID := c.Param("comment_id")
	channelID := strings.TrimSpace(c.Query("channel_id"))

	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel_id is required"})
		return
	}

	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(1) FROM comment_likes WHERE comment_id = $1 AND channel_id = $2",
		commentID,
		channelID,
	).Scan(&count)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "check failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"liked": count > 0})
}
