package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"github.com/gil/giltube/internal/models"
)

func (s *Server) createUser(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	// hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "password hash failed"})
		return
	}

	user := models.User{
		ID:        uuid.New().String(),
		Username:  body.Username,
		Email:     body.Email,
		Password:  string(hashedPassword),
		CreatedAt: time.Now(),
	}

	_, err = s.db.Exec(
		"INSERT INTO users (id, username, email, password, created_at) VALUES ($1,$2,$3,$4,$5)",
		user.ID, user.Username, user.Email, user.Password, user.CreatedAt,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// don’t return password
	user.Password = ""

	c.JSON(http.StatusCreated, user)
}
