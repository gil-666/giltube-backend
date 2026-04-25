package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

type passkeySessionState struct {
	Flow        string
	UserID      string
	DisplayName string
	SessionData webauthn.SessionData
	ExpiresAt   time.Time
}

type webauthnAccountUser struct {
	id          string
	username    string
	email       string
	userType    string
	status      string
	credentials []webauthn.Credential
}

func (u *webauthnAccountUser) WebAuthnID() []byte {
	return []byte(u.id)
}

func (u *webauthnAccountUser) WebAuthnName() string {
	return u.email
}

func (u *webauthnAccountUser) WebAuthnDisplayName() string {
	if u.username != "" {
		return u.username
	}
	return u.email
}

func (u *webauthnAccountUser) WebAuthnCredentials() []webauthn.Credential {
	return u.credentials
}

func (s *Server) ensurePasskeyTable() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS passkeys (
			id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			credential_id TEXT NOT NULL UNIQUE,
			credential_data JSONB NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_used_at TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_passkeys_user_id ON passkeys(user_id);
	`)
	return err
}

func (s *Server) listMyPasskeys(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	rows, err := s.db.Query(`
		SELECT id, name, credential_id, created_at, last_used_at
		FROM passkeys
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list passkeys"})
		return
	}
	defer rows.Close()

	type passkeyRecord struct {
		ID           string     `json:"id"`
		Name         string     `json:"name"`
		CredentialID string     `json:"credential_id"`
		CreatedAt    time.Time  `json:"created_at"`
		LastUsedAt   *time.Time `json:"last_used_at"`
	}

	passkeys := make([]passkeyRecord, 0)
	for rows.Next() {
		var pk passkeyRecord
		if err := rows.Scan(&pk.ID, &pk.Name, &pk.CredentialID, &pk.CreatedAt, &pk.LastUsedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read passkeys"})
			return
		}
		passkeys = append(passkeys, pk)
	}

	c.JSON(http.StatusOK, passkeys)
}

func (s *Server) beginPasskeyRegistration(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	user, err := s.loadWebAuthnUser(userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if user.status == "banned" {
		c.JSON(http.StatusForbidden, gin.H{"error": "account banned"})
		return
	}

	opts, sessionData, err := s.webauthn.BeginRegistration(
		user,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationPreferred,
		}),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to begin passkey registration"})
		return
	}

	sessionToken := uuid.New().String()
	passkeyName := strings.TrimSpace(body.Name)
	if passkeyName == "" {
		passkeyName = "Passkey"
	}

	s.setPasskeySession(sessionToken, passkeySessionState{
		Flow:        "register",
		UserID:      user.id,
		DisplayName: passkeyName,
		SessionData: *sessionData,
		ExpiresAt:   time.Now().UTC().Add(10 * time.Minute),
	})

	c.JSON(http.StatusOK, gin.H{
		"session_token": sessionToken,
		"options":       opts,
	})
}

func (s *Server) finishPasskeyRegistration(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	sessionToken := strings.TrimSpace(c.Query("session_token"))
	if sessionToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_token is required"})
		return
	}

	state, ok := s.getPasskeySession(sessionToken)
	if !ok || state.Flow != "register" || state.UserID != userID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired session"})
		return
	}

	user, err := s.loadWebAuthnUser(userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	credential, err := s.webauthn.FinishRegistration(user, state.SessionData, c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to finish passkey registration"})
		return
	}

	credentialJSON, err := json.Marshal(credential)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store passkey"})
		return
	}

	credentialID := base64.RawURLEncoding.EncodeToString(credential.ID)
	passkeyID := uuid.New().String()
	_, err = s.db.Exec(
		"INSERT INTO passkeys (id, user_id, name, credential_id, credential_data, created_at, last_used_at) VALUES ($1, $2, $3, $4, $5::jsonb, NOW(), NOW())",
		passkeyID,
		user.id,
		state.DisplayName,
		credentialID,
		string(credentialJSON),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save passkey"})
		return
	}

	s.deletePasskeySession(sessionToken)
	c.JSON(http.StatusOK, gin.H{"message": "passkey registered", "passkey_id": passkeyID})
}

func (s *Server) beginPasskeyLogin(c *gin.Context) {
	opts, sessionData, err := s.webauthn.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationPreferred),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to begin passkey login"})
		return
	}

	sessionToken := uuid.New().String()
	s.setPasskeySession(sessionToken, passkeySessionState{
		Flow:        "login",
		SessionData: *sessionData,
		ExpiresAt:   time.Now().UTC().Add(10 * time.Minute),
	})

	c.JSON(http.StatusOK, gin.H{
		"session_token": sessionToken,
		"options":       opts,
	})
}

func (s *Server) finishPasskeyLogin(c *gin.Context) {
	sessionToken := strings.TrimSpace(c.Query("session_token"))
	if sessionToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_token is required"})
		return
	}

	state, ok := s.getPasskeySession(sessionToken)
	if !ok || state.Flow != "login" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired session"})
		return
	}

	resolvedUser, credential, err := s.webauthn.FinishPasskeyLogin(
		func(rawID, userHandle []byte) (webauthn.User, error) {
			if len(userHandle) == 0 {
				credentialID := base64.RawURLEncoding.EncodeToString(rawID)
				var userID string
				err := s.db.QueryRow("SELECT user_id FROM passkeys WHERE credential_id = $1", credentialID).Scan(&userID)
				if err != nil {
					return nil, fmt.Errorf("credential owner not found")
				}
				return s.loadWebAuthnUser(userID)
			}
			return s.loadWebAuthnUser(string(userHandle))
		},
		state.SessionData,
		c.Request,
	)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "passkey verification failed"})
		return
	}

	user, ok := resolvedUser.(*webauthnAccountUser)
	if !ok || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if user.status == "banned" {
		c.JSON(http.StatusForbidden, gin.H{"error": "account banned", "status": "banned"})
		return
	}

	credentialID := base64.RawURLEncoding.EncodeToString(credential.ID)
	_, _ = s.db.Exec("UPDATE passkeys SET last_used_at = NOW() WHERE user_id = $1 AND credential_id = $2", user.id, credentialID)

	s.deletePasskeySession(sessionToken)
	c.JSON(http.StatusOK, gin.H{
		"message":     "login successful",
		"user_id":     user.id,
		"user_type":   user.userType,
		"status":      user.status,
		"username":    user.username,
		"email":       user.email,
		"auth_method": "passkey",
	})
}

func (s *Server) deleteMyPasskey(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	passkeyID := c.Param("id")
	if passkeyID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "passkey id is required"})
		return
	}

	res, err := s.db.Exec("DELETE FROM passkeys WHERE id = $1 AND user_id = $2", passkeyID, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete passkey"})
		return
	}

	rows, err := res.RowsAffected()
	if err != nil || rows == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "passkey not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "passkey deleted"})
}

func (s *Server) loadWebAuthnUser(userID string) (*webauthnAccountUser, error) {
	user := &webauthnAccountUser{}
	err := s.db.QueryRow(
		"SELECT id, username, email, user_type, COALESCE(status, 'active') FROM users WHERE id = $1",
		userID,
	).Scan(&user.id, &user.username, &user.email, &user.userType, &user.status)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query("SELECT credential_data FROM passkeys WHERE user_id = $1", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	credentials := make([]webauthn.Credential, 0)
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, err
		}

		var credential webauthn.Credential
		if err := json.Unmarshal(blob, &credential); err != nil {
			return nil, fmt.Errorf("failed to decode passkey credential: %w", err)
		}
		credentials = append(credentials, credential)
	}

	user.credentials = credentials
	return user, nil
}

func (s *Server) setPasskeySession(token string, state passkeySessionState) {
	s.cleanupExpiredPasskeySessions()
	s.passkeySessionsMu.Lock()
	defer s.passkeySessionsMu.Unlock()
	s.passkeySessions[token] = state
}

func (s *Server) getPasskeySession(token string) (passkeySessionState, bool) {
	s.cleanupExpiredPasskeySessions()
	s.passkeySessionsMu.RLock()
	defer s.passkeySessionsMu.RUnlock()
	state, ok := s.passkeySessions[token]
	return state, ok
}

func (s *Server) deletePasskeySession(token string) {
	s.passkeySessionsMu.Lock()
	defer s.passkeySessionsMu.Unlock()
	delete(s.passkeySessions, token)
}

func (s *Server) cleanupExpiredPasskeySessions() {
	now := time.Now().UTC()
	s.passkeySessionsMu.Lock()
	defer s.passkeySessionsMu.Unlock()
	for token, state := range s.passkeySessions {
		if now.After(state.ExpiresAt) {
			delete(s.passkeySessions, token)
		}
	}
}

