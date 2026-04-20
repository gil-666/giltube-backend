package api

import (
	"net/http"
	"time"
	"github.com/gil/giltube/internal/db"
	"database/sql"
	"os"
	"path/filepath"
	"github.com/gin-gonic/gin"
	"github.com/gil/giltube/config"
	"github.com/gil/giltube/internal/queue"
)

type Server struct {
	router *gin.Engine
	cfg    *config.Config
	db     *sql.DB
	queue  *queue.Queue
}
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "*")
		c.Writer.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(200)
			return
		}

		c.Next()
	}
}


func NewServer(cfg *config.Config) *Server {
	// gin.SetMode(gin.ReleaseMode)
	database := db.Connect(cfg.DatabaseURL)
	s := &Server{cfg: cfg, db: database}
	s.router = gin.Default()
	s.queue = queue.New(cfg.RedisURL)
	
	// Allow large file uploads - buffer up to 1GB before spilling to disk
	s.router.MaxMultipartMemory = 1 << 30 // 1GB
	
	s.router.Static("/videos", filepath.Join(os.Getenv("HOME"), "giltube/output"))
	s.router.Static("/downloads", filepath.Join(os.Getenv("HOME"), "giltube/downloads"))
	s.router.Static("/avatars", filepath.Join(os.Getenv("HOME"), "giltube/giltube-backend/data/avatars"))
	s.router.Use(CORSMiddleware())
	s.setupRoutes()
	return s
}

func (s *Server) Run(addr string) error {
	srv := &http.Server{
		Addr:           addr,
		Handler:        s.router,
		ReadTimeout:    1 * time.Hour,     // 1 hour for reading entire request (large uploads)
		WriteTimeout:   1 * time.Hour,     // 1 hour for writing response
		IdleTimeout:    5 * time.Minute,   // 5 minutes for idle connections
		MaxHeaderBytes: 1 << 20,           // 1MB max header size
	}
	return srv.ListenAndServe()
}

func (s *Server) setupRoutes() {
	api := s.router.Group("/api/v1")
	{
		api.GET("/health", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
		})

		// Search 
		api.GET("/search", s.search)

		// Categories
		api.GET("/categories", s.listCategories)
		api.GET("/categories/all", s.listAllCategories)
		api.GET("/categories/:slug/videos", s.getVideosByCategory)

		api.GET("/videos", s.listVideos)
		api.GET("/my-videos", s.listMyVideos)
		api.GET("/videos/:id", s.getVideo)
		api.POST("/videos/:id/view", s.incrementViews)
		api.POST("/videos/:id/like", s.likeVideo)
		api.DELETE("/videos/:id/like", s.unlikeVideo)
		api.GET("/videos/:id/liked", s.checkIfLiked)
		api.GET("/videos/:id/comments", s.getVideoComments)
		api.GET("/channels/:channel_id/analytics", s.getChannelAnalytics)
		api.POST("/videos/:id/comments", s.createComment)
		api.DELETE("/comments/:comment_id", s.deleteComment)
		api.GET("/videos/:id/stream/*filepath", s.streamVideo)
		api.GET("/videos/:id/download", s.downloadVideo)
		api.GET("/videos/:id/download-status", s.getDownloadStatus)
		api.GET("/downloads/:videoID/:quality", s.serveDownload)
		api.POST("/videos/upload-chunk", s.uploadChunk)
		api.POST("/videos/finalize-upload", s.finalizeUpload)
		api.POST("/videos", s.uploadVideo)
		api.PUT("/videos/:id", s.updateVideo)
		api.DELETE("/videos/:id", s.deleteVideo)
		api.POST("/users", s.createUser)
		api.GET("/users/:user_id/channels", s.listUserChannels)
		api.POST("/channels", s.createChannel)
		api.GET("/channels/:channel_id/info", s.getChannelInfo)
		api.GET("/channels/:channel_id/videos", s.getChannelVideos)
		api.PUT("/channels/:channel_id", s.updateChannel)
		api.DELETE("/channels/:channel_id", s.deleteChannel)
		api.POST("/login", s.login) 

	}
}
