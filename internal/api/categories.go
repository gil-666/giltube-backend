package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gil/giltube/internal/db"
	"github.com/gil/giltube/internal/models"
)

func (s *Server) listCategories(c *gin.Context) {
	categories, err := db.GetCategoriesWithVideos(s.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch categories"})
		return
	}

	if categories == nil {
		categories = []map[string]interface{}{}
	}

	c.JSON(http.StatusOK, categories)
}

func (s *Server) listAllCategories(c *gin.Context) {
	categories, err := db.GetAllCategories(s.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch categories"})
		return
	}

	if categories == nil {
		categories = []map[string]interface{}{}
	}

	c.JSON(http.StatusOK, categories)
}

func (s *Server) getVideosByCategory(c *gin.Context) {
	slug := c.Param("slug")

	type ChannelResponse struct {
		ID          string `json:"id"`
		UserID      string `json:"user_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		CreatedAt   time.Time `json:"created_at"`
		AvatarURL   string `json:"avatar_url"`
		Verified    bool `json:"verified"`
	}

	type VideoResponse struct {
		models.Video
		Channel ChannelResponse `json:"channel"`
	}

	// Get pagination parameters
	limit := 12
	offset := 0
	if l := c.DefaultQuery("limit", ""); l != "" {
		if parsed := parseInt(l, 12); parsed > 0 {
			limit = parsed
			if limit > 100 {
				limit = 100 // Cap at 100 to prevent abuse
			}
		}
	}
	if o := c.DefaultQuery("offset", ""); o != "" {
		if parsed := parseInt(o, 0); parsed >= 0 {
			offset = parsed
		}
	}

	// Query videos by category slug with pagination
	rows, err := s.db.Query(`
		SELECT 
			v.id,
			v.title,
			v.description,
			v.status,
			COALESCE(v.views, 0),
			v.hls_path,
			COALESCE(v.thumbnail_url, ''),
			COALESCE(v.has_custom_thumbnail, false),
			COALESCE(v.explicit, false),
			COALESCE(v.width, 0),
			v.created_at,
			v.channel_id,
			c.id,
			c.user_id,
			c.name,
			c.description,
			c.created_at,
			c.avatar_url,
			COALESCE(c.verified, false)
		FROM videos v
		LEFT JOIN channels c ON v.channel_id = c.id
		JOIN video_categories vc ON v.id = vc.video_id
		JOIN categories cat ON vc.category_id = cat.id
		WHERE v.status = 'ready' AND cat.slug = $1
		ORDER BY v.created_at DESC
		LIMIT $2 OFFSET $3
	`, slug, limit, offset)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db query failed"})
		return
	}
	defer rows.Close()

	videos := []VideoResponse{}

	for rows.Next() {
		var v models.Video
		var chID, chUserID, chName, chDesc sql.NullString
		var chCreatedAt sql.NullTime
		var chAvatarURL sql.NullString
		var chVerified sql.NullBool

		err := rows.Scan(
			&v.ID,
			&v.Title,
			&v.Description,
			&v.Status,
			&v.Views,
			&v.HLSPath,
			&v.ThumbnailURL,
			&v.HasCustomThumbnail,
			&v.Explicit,
			&v.Width,
			&v.CreatedAt,
			&v.ChannelID,

			&chID,
			&chUserID,
			&chName,
			&chDesc,
			&chCreatedAt,
			&chAvatarURL,
			&chVerified,
		)
		if err != nil {
			fmt.Println("Scan error:", err)
			continue
		}

		// Build avatar URL
		avatarURL := ""
		if chAvatarURL.Valid && chAvatarURL.String != "" {
			avatarURL = fmt.Sprintf("/avatars/%s", chAvatarURL.String)
		}

		// Fetch categories for this video
		categories, err := db.GetVideoCategories(s.db, v.ID)
		if err == nil && categories != nil {
			for _, cat := range categories {
				v.Categories = append(v.Categories, models.Category{
					ID:          cat["id"].(string),
					Name:        cat["name"].(string),
					Slug:        cat["slug"].(string),
					Description: cat["description"].(string),
				})
			}
		}

		videos = append(videos, VideoResponse{
			Video: v,
			Channel: ChannelResponse{
				ID:          chID.String,
				UserID:      chUserID.String,
				Name:        chName.String,
				Description: chDesc.String,
				CreatedAt:   chCreatedAt.Time,
				AvatarURL:   avatarURL,
				Verified:    chVerified.Bool,
			},
		})
	}

	if videos == nil {
		videos = []VideoResponse{}
	}

	c.JSON(http.StatusOK, videos)
}
