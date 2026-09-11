package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	ListenAddr     string
	DatabaseURL    string
	FilerURL       string
	FilerPrefix    string
	Collection     string
	ChunkSize      int64
	MaxSize        int64
	PublicBaseURL  string
	StaticDir      string
	FileTTL        time.Duration
	SessionTTL     time.Duration
	RecordTTL      time.Duration
	SeaweedFileTTL string
	SeaweedTmpTTL  string
	AdminPassword  string
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddr:     env("LISTEN_ADDR", ":8080"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		FilerURL:       env("SEAWEEDFS_FILER_URL", "http://seaweedfs-filer.default.svc.cluster.local:8888"),
		FilerPrefix:    env("SEAWEEDFS_PREFIX", "/minicloudstorage"),
		Collection:     env("SEAWEEDFS_COLLECTION", "minicloudstorage"),
		PublicBaseURL:  env("PUBLIC_BASE_URL", "https://minicloudstorage.19121122.xyz"),
		StaticDir:      env("STATIC_DIR", "/app/static"),
		FileTTL:        7 * 24 * time.Hour,
		SessionTTL:     24 * time.Hour,
		RecordTTL:      31 * 24 * time.Hour,
		SeaweedFileTTL: env("SEAWEEDFS_FILE_TTL", "8d"),
		SeaweedTmpTTL:  env("SEAWEEDFS_TMP_TTL", "2d"),
		AdminPassword:  os.Getenv("ADMIN_PASSWORD"),
	}
	var err error
	cfg.ChunkSize, err = envInt("CHUNK_SIZE", 8<<20)
	if err != nil {
		return cfg, err
	}
	cfg.MaxSize, err = envInt("MAX_SIZE", 4<<30)
	if err != nil {
		return cfg, err
	}
	if cfg.DatabaseURL == "" {
		return cfg, fmt.Errorf("DATABASE_URL is required")
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return n, nil
}
