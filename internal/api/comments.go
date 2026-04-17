package api

import (
	"fmt"
	"net/http"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gil/giltube/internal/db"
)

func (s *Server) getVideoComments(c *gin.Context) {
	videoID := c.Param("id")

	comments, err := db.GetComments(s.db, videoID)
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
	channelID := c.PostForm("channel_id")
	text := c.PostForm("text")

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

	commentID := uuid.New().String()
	err := db.CreateComment(s.db, commentID, videoID, channelID, text)
	if err != nil {
		fmt.Println("DB error creating comment:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create comment"})
		return
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
