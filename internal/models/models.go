package models

import (
	"database/sql"
	"time"
)

type Video struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Status       string    `json:"status"`
	Progress     int       `json:"progress"`
	Views        int       `json:"views"`
	Likes        int       `json:"likes"`
	CreatedAt    time.Time `json:"created_at"`
	HLSPath	 string    `json:"hls_path"`
	ThumbnailURL string    `json:"thumbnail_url"`
	ChannelID string `json:"channel_id"`
}

type Stream struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	StreamKey string    `json:"stream_key"`
	CreatedAt time.Time `json:"created_at"`
}

type User struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Password  string `json:"-" db:"password"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

type Channel struct {
	ID          string         `json:"id"`
	UserID      string         `json:"user_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	CreatedAt   time.Time      `json:"created_at"`
	AvatarURL   sql.NullString `json:"avatar_url"`
}

type Comment struct {
	ID        string    `json:"id"`
	VideoID   string    `json:"video_id"`
	ChannelID string    `json:"channel_id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

type CommentResponse struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	Channel   struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	} `json:"channel"`
}

type Like struct {
	ID        string    `json:"id"`
	VideoID   string    `json:"video_id"`
	ChannelID string    `json:"channel_id"`
	CreatedAt time.Time `json:"created_at"`
}

