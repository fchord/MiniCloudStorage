package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fchord/MiniCloudStorage/internal/api"
	"github.com/fchord/MiniCloudStorage/internal/config"
	"github.com/fchord/MiniCloudStorage/internal/db"
	"github.com/fchord/MiniCloudStorage/internal/storage"
)

func main() {
	cleanupOnly := flag.Bool("cleanup", false, "run expiry cleanup once and exit")
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.LUTC | log.Lshortfile)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx := context.Background()
	store, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx, db.SchemaSQL); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	filer := storage.NewFiler(cfg.FilerURL, cfg.FilerPrefix, cfg.Collection)
	h := api.New(cfg, store, filer)

	if *cleanupOnly {
		if err := h.Cleanup(ctx); err != nil {
			log.Fatalf("cleanup: %v", err)
		}
		return
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           h.Router(),
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       30 * time.Minute,
		WriteTimeout:      60 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	go func() {
		log.Printf("listening on %s public=%s prefix=%s", cfg.ListenAddr, cfg.PublicBaseURL, cfg.FilerPrefix)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
