package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type SearchResult struct {
	Type      string      `json:"type"` // "video" or "channel"
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	Name      string      `json:"name,omitempty"`
	Channel   string      `json:"channel,omitempty"`
	Description string      `json:"description,omitempty"`
	ChannelID string      `json:"channel_id,omitempty"`
	Avatar    string      `json:"avatar,omitempty"`
	Thumbnail string      `json:"thumbnail,omitempty"`
	Views     int         `json:"views,omitempty"`
	Verified  bool        `json:"verified,omitempty"`
	Score     float64     `json:"-"` // Internal scoring for ranking
}

// searchVideos and Channels with smart ranking algorithm
func (s *Server) search(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "search query required"})
		return
	}

	page := 1
	if p := c.Query("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}

	pageSize := 20
	offset := (page - 1) * pageSize

	// Search videos and channels (get all matching results)
	videos, err := s.searchVideos(query)
	if err != nil {
		fmt.Printf("Error searching videos: %v\n", err)
	}

	channels, err := s.searchChannels(query)
	if err != nil {
		fmt.Printf("Error searching channels: %v\n", err)
	}

	// Combine and rank results
	results := s.rankResults(query, videos, channels)

	// Paginate combined results
	var paginatedResults []SearchResult
	if len(results) > offset {
		end := offset + pageSize
		if end > len(results) {
			end = len(results)
		}
		paginatedResults = results[offset:end]
	}

	c.JSON(http.StatusOK, gin.H{
		"results": paginatedResults,
		"total":   len(results),
		"page":    page,
		"per_page": pageSize,
	})
}

// searchVideos by title or description
func (s *Server) searchVideos(query string) ([]SearchResult, error) {
	searchPattern := "%" + query + "%"
	rows, err := s.db.Query(`
		SELECT v.id, v.title, v.description, v.views, v.thumbnail_url, c.id, c.name, c.verified
		FROM videos v
		JOIN channels c ON v.channel_id = c.id
		WHERE (v.status = 'ready')
		AND (v.title ILIKE $1 OR v.description ILIKE $1)
		ORDER BY v.views DESC
		LIMIT 200
	`, searchPattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var videoID string
		var r SearchResult
		var channelID, channelName string
		var verified sql.NullBool
		var thumbnailFilename string
		r.Type = "video"

		if err := rows.Scan(
			&videoID,
			&r.Title,
			&r.Description,
			&r.Views,
			&thumbnailFilename,
			&channelID,
			&channelName,
			&verified,
		); err != nil {
			return nil, err
		}

		r.ID = videoID
		r.Channel = channelName
		r.ChannelID = channelID
		if verified.Valid {
			r.Verified = verified.Bool
		}

		// Build thumbnail URL path - handle if already contains full path
		if thumbnailFilename != "" {
			if strings.Contains(thumbnailFilename, "/videos/") {
				// Already a full path
				r.Thumbnail = thumbnailFilename
			} else {
				// Just a filename, build the path
				r.Thumbnail = "/videos/" + videoID + "/" + thumbnailFilename
			}
		}

		results = append(results, r)
	}

	return results, rows.Err()
}

// searchChannels by name or description
func (s *Server) searchChannels(query string) ([]SearchResult, error) {
	searchPattern := "%" + query + "%"
	rows, err := s.db.Query(`
		SELECT id, name, avatar_url, verified, description
		FROM channels
		WHERE name ILIKE $1 OR description ILIKE $1
		ORDER BY name ASC
		LIMIT 200
	`, searchPattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var verified sql.NullBool
		var desc sql.NullString
		r.Type = "channel"

		if err := rows.Scan(
			&r.ID,
			&r.Name,
			&r.Avatar,
			&verified,
			&desc,
		); err != nil {
			return nil, err
		}

		if verified.Valid {
			r.Verified = verified.Bool
		}

		r.Title = r.Name // Use name as title for display
		r.Description = desc.String // Set description if available
		// Add /avatars/ prefix to avatar filename
		if r.Avatar != "" {
			r.Avatar = "/avatars/" + r.Avatar
		}

		results = append(results, r)
	}

	return results, rows.Err()
}

// rankResults combines videos and channels with smart ranking algorithm
func (s *Server) rankResults(query string, videos, channels []SearchResult) []SearchResult {
	lowerQuery := strings.ToLower(strings.TrimSpace(query))

	// Calculate scores
	for i := range videos {
		videos[i].Score = s.calculateVideoScore(&videos[i], lowerQuery)
	}

	for i := range channels {
		channels[i].Score = s.calculateChannelScore(&channels[i], lowerQuery)
	}

	// Combine results
	all := append(channels, videos...)

	// Sort by score descending
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[j].Score > all[i].Score {
				all[i], all[j] = all[j], all[i]
			}
		}
	}

	return all
}

// calculateChannelScore gives priority to channel name matches but allows videos to show
func (s *Server) calculateChannelScore(ch *SearchResult, query string) float64 {
	lowerName := strings.ToLower(ch.Name)
	score := 0.0

	// Exact match gets high score but not overwhelmingly high
	if lowerName == query {
		score = 200.0
	} else if strings.Contains(lowerName, query) {
		// If query is contained in channel name, boost score
		score = 100.0
		// Give extra points if match is at the beginning
		if strings.HasPrefix(lowerName, query) {
			score += 50.0
		}
	}

	return score
}

// calculateVideoScore prioritizes title matches and view count
func (s *Server) calculateVideoScore(v *SearchResult, query string) float64 {
	lowerTitle := strings.ToLower(v.Title)
	score := 0.0

	// Title match gets priority
	if strings.Contains(lowerTitle, query) {
		// Exact title match
		if lowerTitle == query {
			score = 250.0
		} else {
			score = 80.0
			// Beginning of title match
			if strings.HasPrefix(lowerTitle, query) {
				score += 60.0
			}
		}
	}

	// Boost by views (divide by 5000 to make views more competitive with title matches)
	score += float64(v.Views) / 5000.0

	return score
}
