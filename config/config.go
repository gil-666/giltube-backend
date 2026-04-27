package config

import (
	"os"
	"strings"
)

type Config struct {
	Port        string
	DatabaseURL string
	RedisURL    string
	MinioURL    string
	MinioKey    string
	MinioSecret string
	MinioBucket string
	MediaMTXURL string
	MediaMTXRTMPURL string
	MediaMTXLocalRTMPURL string
	MediaMTXLanRTMPURL string
	MediaMTXHLSBaseURL string
	MediaMTXAPIURL string
	WebAuthnRPID string
	WebAuthnRPOrigins string
	WebAuthnRPDisplayName string
	PushEnabled bool
	PushSendEnabled bool
	VAPIDPublicKey string
	VAPIDPrivateKey string
	VAPIDSubject string
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
		MediaMTXRTMPURL: getEnv("MEDIAMTX_RTMP_URL", "rtmp://live-giltube.gilservers.com:1935"),
		MediaMTXLocalRTMPURL: getEnv("MEDIAMTX_LOCAL_RTMP_URL", "rtmp://127.0.0.1:1935"),
		MediaMTXLanRTMPURL: getEnv("MEDIAMTX_LAN_RTMP_URL", ""),
		MediaMTXHLSBaseURL: getEnv("MEDIAMTX_HLS_BASE_URL", "https://giltube.gilservers.com"),
		MediaMTXAPIURL: getEnv("MEDIAMTX_API_URL", "http://localhost:9997"),
		WebAuthnRPID: getEnv("WEBAUTHN_RP_ID", "giltube.gilservers.com"),
		WebAuthnRPOrigins: getEnv("WEBAUTHN_RP_ORIGINS", "https://giltube.gilservers.com"),
		WebAuthnRPDisplayName: getEnv("WEBAUTHN_RP_DISPLAY_NAME", "GilTube"),
		PushEnabled: getEnvBool("PUSH_ENABLED", true),
		PushSendEnabled: getEnvBool("PUSH_SEND_ENABLED", false),
		VAPIDPublicKey: getEnv("VAPID_PUBLIC_KEY", ""),
		VAPIDPrivateKey: getEnv("VAPID_PRIVATE_KEY", ""),
		VAPIDSubject: getEnv("VAPID_SUBJECT", "mailto:admin@giltube.local"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
