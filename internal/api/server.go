package api

import (
	"database/sql"
	"github.com/gil/giltube/config"
	"github.com/gil/giltube/internal/db"
	"github.com/gil/giltube/internal/presence"
	"github.com/gil/giltube/internal/queue"
	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/webauthn"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Server struct {
	router            *gin.Engine
	cfg               *config.Config
	db                *sql.DB
	queue             *queue.Queue
	presence          *presence.Presence
	liveStateCache    map[string]liveStateCacheEntry
	liveStateCacheMu  sync.RWMutex
	webauthn          *webauthn.WebAuthn
	passkeySessions   map[string]passkeySessionState
	passkeySessionsMu sync.RWMutex
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
	s := &Server{
		cfg:             cfg,
		db:              database,
		liveStateCache:  make(map[string]liveStateCacheEntry),
		passkeySessions: make(map[string]passkeySessionState),
	}
	s.router = gin.Default()
	s.queue = queue.New(cfg.RedisURL)
	s.presence = presence.New(cfg.RedisURL)

	origins := strings.Split(cfg.WebAuthnRPOrigins, ",")
	trimmedOrigins := make([]string, 0, len(origins))
	for _, origin := range origins {
		candidate := strings.TrimSpace(origin)
		if candidate != "" {
			trimmedOrigins = append(trimmedOrigins, candidate)
		}
	}
	if len(trimmedOrigins) == 0 {
		trimmedOrigins = []string{"http://localhost:3000"}
	}

	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName: cfg.WebAuthnRPDisplayName,
		RPID:          cfg.WebAuthnRPID,
		RPOrigins:     trimmedOrigins,
	})
	if err != nil {
		panic(err)
	}
	s.webauthn = wa

	if err := s.ensurePasskeyTable(); err != nil {
		panic(err)
	}

	if err := s.ensureLiveStreamsTable(); err != nil {
		panic(err)
	}

	if s.cfg.PushEnabled {
		if strings.TrimSpace(s.cfg.VAPIDPublicKey) == "" || strings.TrimSpace(s.cfg.VAPIDPrivateKey) == "" {
			log.Println("push notifications disabled: missing VAPID_PUBLIC_KEY or VAPID_PRIVATE_KEY")
			s.cfg.PushEnabled = false
			s.cfg.PushSendEnabled = false
		}
	}

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
		ReadTimeout:    1 * time.Hour,   // 1 hour for reading entire request (large uploads)
		WriteTimeout:   1 * time.Hour,   // 1 hour for writing response
		IdleTimeout:    5 * time.Minute, // 5 minutes for idle connections
		MaxHeaderBytes: 1 << 20,         // 1MB max header size
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
		api.POST("/videos/:id/re-encode", s.reEncodeVideo)
		api.GET("/videos/:id/comments", s.getVideoComments)
		api.GET("/channels/:channel_id/analytics", s.getChannelAnalytics)
		api.POST("/videos/:id/comments", s.createComment)
		api.DELETE("/comments/:comment_id", s.deleteComment)
		api.POST("/comments/:comment_id/like", s.likeComment)
		api.DELETE("/comments/:comment_id/like", s.unlikeComment)
		api.GET("/comments/:comment_id/liked", s.checkIfCommentLiked)
		api.GET("/videos/:id/stream/*filepath", s.streamVideo)
		api.GET("/live/active", s.listActiveLiveStreams)
		// Presence endpoints for live streams
		api.POST("/live/:video_id/presence", s.joinPresence)
		api.DELETE("/live/:video_id/presence", s.leavePresence)
		api.GET("/live/:video_id/presence/stream", s.presenceStream)
		api.GET("/live/channels/:channel_id", s.getChannelLiveStatus)
		api.GET("/live/channels/:channel_id/chat", s.getLiveChatMessages)
		api.POST("/live/channels/:channel_id/chat", s.authMiddleware(), s.postLiveChatMessage)
		api.GET("/live/me", s.authMiddleware(), s.getMyLiveStream)
		api.POST("/live/me/publisher-presence", s.authMiddleware(), s.setMyPublisherPresence)
		api.PUT("/live/me/settings", s.authMiddleware(), s.updateMyLiveStreamSettings)
		api.POST("/live/me/key/rotate", s.authMiddleware(), s.rotateMyLiveStreamKey)
		api.POST("/live/me/start", s.authMiddleware(), s.startMyLiveStream)
		api.POST("/live/me/stop", s.authMiddleware(), s.stopMyLiveStream)
		api.GET("/videos/:id/download", s.downloadVideo)
		api.GET("/videos/:id/download-status", s.getDownloadStatus)
		api.GET("/downloads/:videoID/:quality", s.serveDownload)
		api.POST("/videos/upload-chunk", s.uploadChunk)
		api.POST("/videos/finalize-upload", s.finalizeUpload)
		api.POST("/videos", s.uploadVideo)
		api.PUT("/videos/:id", s.updateVideo)
		api.DELETE("/videos/:id", s.deleteVideo)
		api.POST("/users", s.createUser)
		api.GET("/user/:user_id", s.getUser)
		api.GET("/users/:user_id/channels", s.listUserChannels)
		api.POST("/channels", s.createChannel)
		api.GET("/channels/:channel_id/info", s.getChannelInfo)
		api.GET("/channels/:channel_id/videos", s.getChannelVideos)
		api.PUT("/channels/:channel_id", s.updateChannel)
		api.DELETE("/channels/:channel_id", s.deleteChannel)
		api.POST("/login", s.login)
		api.GET("/account/me", s.authMiddleware(), s.getMyAccount)
		api.PUT("/account/email", s.authMiddleware(), s.updateMyEmail)
		api.PUT("/account/password", s.authMiddleware(), s.updateMyPassword)
		api.DELETE("/account", s.authMiddleware(), s.deleteMyAccount)
		api.GET("/passkeys", s.authMiddleware(), s.listMyPasskeys)
		api.POST("/passkeys/register/begin", s.authMiddleware(), s.beginPasskeyRegistration)
		api.POST("/passkeys/register/finish", s.authMiddleware(), s.finishPasskeyRegistration)
		api.POST("/passkeys/login/begin", s.beginPasskeyLogin)
		api.POST("/passkeys/login/finish", s.finishPasskeyLogin)
		api.DELETE("/passkeys/:id", s.authMiddleware(), s.deleteMyPasskey)

		notifications := api.Group("/notifications")
		notifications.Use(s.authMiddleware())
		{
			notifications.GET("", s.listNotifications)
			notifications.GET("/unread-count", s.getUnreadNotificationCount)
			notifications.GET("/push/config", s.getPushConfig)
			notifications.PATCH("/:id/read", s.markNotificationRead)
			notifications.POST("/read-all", s.markAllNotificationsRead)
			notifications.POST("/push/subscribe", s.subscribePush)
			notifications.POST("/push/unsubscribe", s.unsubscribePush)
		}

		// Admin routes
		admin := api.Group("/admin")
		admin.Use(s.authMiddleware(), s.isAdmin)
		{
			admin.GET("/stats", s.getAdminStats)
			admin.GET("/users", s.getAdminUsers)
			admin.POST("/users/:id/toggle-admin", s.toggleAdminStatus)
			admin.DELETE("/users/:id", s.deleteUser)
			admin.POST("/users/:id/suspend", s.suspendUser)
			admin.POST("/users/:id/ban", s.banUser)
			admin.POST("/users/:id/unban", s.unbanUser)
			admin.POST("/users/:id/unsuspend", s.unsuspendUser)
			admin.GET("/channels", s.getAdminChannels)
			admin.POST("/channels/:id/suspend", s.suspendChannel)
			admin.POST("/channels/:id/ban", s.banChannel)
			admin.POST("/channels/:id/unban", s.unbanChannel)
			admin.POST("/channels/:id/unsuspend", s.unsuspendChannel)
			admin.PUT("/videos/:id/verify", s.verifyVideo)
		}
	}
}

// Middleware to authenticate user from token
func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Try to get user_id from header or query param
		userID := c.GetHeader("X-User-ID")
		if userID == "" {
			userID = c.Query("user_id")
		}

		if userID != "" {
			c.Set("user_id", userID)
		}

		c.Next()
	}
}
