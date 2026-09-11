package config

import "testing"

func TestLoadDefaultMaxSizeIsFourGiB(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://minicloudstorage@127.0.0.1:5432/minicloudstorage?sslmode=disable")
	t.Setenv("MAX_SIZE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const want = int64(4) << 30
	if cfg.MaxSize != want {
		t.Fatalf("MaxSize = %d, want %d (4GiB)", cfg.MaxSize, want)
	}
}

func TestLoadMaxSizeOverride(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://minicloudstorage@127.0.0.1:5432/minicloudstorage?sslmode=disable")
	t.Setenv("MAX_SIZE", "4294967296")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxSize != 4<<30 {
		t.Fatalf("MaxSize = %d, want 4GiB", cfg.MaxSize)
	}
}
