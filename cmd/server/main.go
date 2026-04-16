package main

import (
	"log"
	"github.com/gil/giltube/internal/api"
	"github.com/gil/giltube/config"
)

func main() {
	cfg := config.Load()
	server := api.NewServer(cfg)
	log.Printf("GilTube API starting on :%s", cfg.Port)
	server.Run(":" + cfg.Port)
}
