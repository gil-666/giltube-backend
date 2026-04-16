package config

import "os"

type Config struct {
	Port        string
	DatabaseURL string
	RedisURL    string
	MinioURL    string
	MinioKey    string
	MinioSecret string
	MinioBucket string
	MediaMTXURL string
}

func Load() *Config {
	return &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://giltube:giltube@localhost:5432/giltube?sslmode=disable"),
		RedisURL:    getEnv("REDIS_URL", "redis://localhost:6379"),
		MinioURL:    getEnv("MINIO_URL", "localhost:9000"),
		MinioKey:    getEnv("MINIO_KEY", "giltube"),
		MinioSecret: getEnv("MINIO_SECRET", "giltube123"),
		MinioBucket: getEnv("MINIO_BUCKET", "giltube"),
		MediaMTXURL: getEnv("MEDIAMTX_URL", "localhost:8554"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
