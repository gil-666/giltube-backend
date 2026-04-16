package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gil/giltube/internal/models"
)

func (s *Server) createChannel(c *gin.Context) {
	var body struct {
		UserID      string `json:"user_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	channel := models.Channel{
		ID:          uuid.New().String(),
		UserID:      body.UserID,
		Name:        body.Name,
		Description: body.Description,
		CreatedAt:   time.Now(),
	}

	_, err := s.db.Exec(
		"INSERT INTO channels (id, user_id, name, description, created_at) VALUES ($1,$2,$3,$4,$5)",
		channel.ID, channel.UserID, channel.Name, channel.Description, channel.CreatedAt,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, channel)
}
