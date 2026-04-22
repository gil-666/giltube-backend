package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) login(c *gin.Context) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	var hashedPassword string
	var userID string
	var userType string
	var status string

	err := s.db.QueryRow(
		"SELECT id, password, user_type, COALESCE(status, 'active') FROM users WHERE LOWER(email)=$1",
		strings.ToLower(body.Email),
	).Scan(&userID, &hashedPassword, &userType, &status)

	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	err = bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(body.Password))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	// Reject login if user is banned
	if status == "banned" {
		c.JSON(http.StatusForbidden, gin.H{"error": "account banned", "status": "banned"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":   "login successful",
		"user_id":   userID,
		"user_type": userType,
		"status":    status,
	})
}
