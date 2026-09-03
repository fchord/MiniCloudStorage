package db

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/fchord/MiniCloudStorage/migrations"
)

const (
	ReasonUserCancel     = "user_cancel"
	ReasonPageClose      = "page_close"
	ReasonIdleTimeout    = "idle_timeout"
	ReasonChunkHTTP      = "chunk_http"
	ReasonCompleteFailed = "complete_failed"
	ReasonUnknown        = "unknown"

	TabInProgress  = "in_progress"
	TabCompleted   = "completed"
	TabCancelled   = "cancelled"
	TabOtherFailed = "other_failed"
	TabExpired     = "expired"

	ReasonDetailMax = 400
)

func (p *Pool) RunMigrations(ctx context.Context) error {
	if _, err := p.inner.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrations.SQL, ".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		if err := p.inner.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename = $1)`, name,
		).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := fs.ReadFile(migrations.SQL, name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if _, err := p.inner.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := p.inner.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`, name,
		); err != nil {
			return fmt.Errorf("record %s: %w", name, err)
		}
	}
	return nil
}

func TruncateDetail(s string) string {
	if len(s) <= ReasonDetailMax {
		return s
	}
	return s[:ReasonDetailMax]
}
