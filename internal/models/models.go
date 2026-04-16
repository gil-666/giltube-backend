package models

import "time"

type Video struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
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
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

