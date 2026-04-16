package api

import (
	"net/http"
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
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
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
	s.router.Static("/videos", filepath.Join(os.Getenv("HOME"), "giltube/output"))
	s.router.Use(CORSMiddleware())
	s.setupRoutes()
	return s
}

func (s *Server) Run(addr string) error {
	return s.router.Run(addr)
}

func (s *Server) setupRoutes() {
	api := s.router.Group("/api/v1")
	{
		api.GET("/health", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
		})

		api.GET("/videos", s.listVideos)
		api.GET("/videos/:id", s.getVideo)
		api.GET("/videos/:id/stream/*filepath", s.streamVideo)
		api.POST("/videos", s.uploadVideo)
		api.POST("/users", s.createUser)
		api.POST("/channels", s.createChannel)
		api.POST("/login", s.login) 

	}
}
