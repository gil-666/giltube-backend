package api

import (
	"net/http"
	"strings"
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
		Email:     strings.ToLower(body.Email),
		Password:  string(hashedPassword),
		UserType:  "user",
		CreatedAt: time.Now().UTC(),
	}

	_, err = s.db.Exec(
		"INSERT INTO users (id, username, email, password, user_type, created_at) VALUES ($1,$2,$3,$4,$5,$6)",
		user.ID, user.Username, user.Email, user.Password, user.UserType, user.CreatedAt,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// don’t return password
	user.Password = ""

	c.JSON(http.StatusCreated, user)
}
func (s *Server) getUser(c *gin.Context) {
	userID := c.Param("user_id")
	
	var user models.User
	err := s.db.QueryRow(
		"SELECT id, username, email, user_type, COALESCE(status, 'active'), created_at FROM users WHERE id = $1",
		userID,
	).Scan(&user.ID, &user.Username, &user.Email, &user.UserType, &user.Status, &user.CreatedAt)
	
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	
	c.JSON(http.StatusOK, user)
}