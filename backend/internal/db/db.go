package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Pool struct {
	inner *pgxpool.Pool
}

type UploadSession struct {
	ID             uuid.UUID
	Filename       string
	ContentType    string
	TotalSize      int64
	ChunkSize      int
	TotalChunks    int
	ReceivedChunks []bool
	CreatedAt      time.Time
}

type File struct {
	Code        string
	Filename    string
	ContentType string
	SizeBytes   int64
	SeaweedPath string
	Password    *string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	Status      string
}

func Connect(ctx context.Context, databaseURL string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Pool{inner: pool}, nil
}

func (p *Pool) Close() { p.inner.Close() }

func (p *Pool) Migrate(ctx context.Context, sql string) error {
	_, err := p.inner.Exec(ctx, sql)
	return err
}

func (p *Pool) InsertSession(ctx context.Context, s UploadSession) error {
	_, err := p.inner.Exec(ctx, `
		INSERT INTO upload_sessions (id, filename, content_type, total_size, chunk_size, total_chunks, received_chunks)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, s.ID, s.Filename, s.ContentType, s.TotalSize, s.ChunkSize, s.TotalChunks, s.ReceivedChunks)
	return err
}

func (p *Pool) GetSession(ctx context.Context, id uuid.UUID) (UploadSession, error) {
	var s UploadSession
	err := p.inner.QueryRow(ctx, `
		SELECT id, filename, content_type, total_size, chunk_size, total_chunks, received_chunks, created_at
		FROM upload_sessions WHERE id = $1
	`, id).Scan(&s.ID, &s.Filename, &s.ContentType, &s.TotalSize, &s.ChunkSize, &s.TotalChunks, &s.ReceivedChunks, &s.CreatedAt)
	if err != nil {
		return s, err
	}
	return s, nil
}

func (p *Pool) MarkChunk(ctx context.Context, id uuid.UUID, index int) (UploadSession, error) {
	var s UploadSession
	err := p.inner.QueryRow(ctx, `
		UPDATE upload_sessions
		SET received_chunks[$1] = TRUE
		WHERE id = $2
		RETURNING id, filename, content_type, total_size, chunk_size, total_chunks, received_chunks, created_at
	`, index+1, id).Scan(&s.ID, &s.Filename, &s.ContentType, &s.TotalSize, &s.ChunkSize, &s.TotalChunks, &s.ReceivedChunks, &s.CreatedAt)
	return s, err
}

func (p *Pool) DeleteSession(ctx context.Context, id uuid.UUID) error {
	_, err := p.inner.Exec(ctx, `DELETE FROM upload_sessions WHERE id = $1`, id)
	return err
}

func (p *Pool) InsertFile(ctx context.Context, file File) error {
	_, err := p.inner.Exec(ctx, `
		INSERT INTO files (code, filename, content_type, size_bytes, seaweed_path, password, created_at, expires_at, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, file.Code, file.Filename, file.ContentType, file.SizeBytes, file.SeaweedPath, file.Password, file.CreatedAt, file.ExpiresAt, file.Status)
	return err
}

func (p *Pool) GetFile(ctx context.Context, code string) (File, error) {
	var file File
	err := p.inner.QueryRow(ctx, `
		SELECT code, filename, content_type, size_bytes, seaweed_path, password, created_at, expires_at, status
		FROM files WHERE code = $1
	`, code).Scan(&file.Code, &file.Filename, &file.ContentType, &file.SizeBytes, &file.SeaweedPath, &file.Password, &file.CreatedAt, &file.ExpiresAt, &file.Status)
	return file, err
}

func (p *Pool) CodeExists(ctx context.Context, code string) (bool, error) {
	var exists bool
	err := p.inner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM files WHERE code = $1)`, code).Scan(&exists)
	return exists, err
}

func (p *Pool) ExpiredFiles(ctx context.Context, now time.Time) ([]File, error) {
	rows, err := p.inner.Query(ctx, `
		SELECT code, filename, content_type, size_bytes, seaweed_path, password, created_at, expires_at, status
		FROM files
		WHERE expires_at < $1 AND status <> 'expired'
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		var file File
		if err := rows.Scan(&file.Code, &file.Filename, &file.ContentType, &file.SizeBytes, &file.SeaweedPath, &file.Password, &file.CreatedAt, &file.ExpiresAt, &file.Status); err != nil {
			return nil, err
		}
		out = append(out, file)
	}
	return out, rows.Err()
}

func (p *Pool) MarkExpiredAndDeleteRow(ctx context.Context, code string) error {
	_, err := p.inner.Exec(ctx, `DELETE FROM files WHERE code = $1`, code)
	return err
}

func (p *Pool) StaleSessions(ctx context.Context, olderThan time.Time) ([]UploadSession, error) {
	rows, err := p.inner.Query(ctx, `
		SELECT id, filename, content_type, total_size, chunk_size, total_chunks, received_chunks, created_at
		FROM upload_sessions
		WHERE created_at < $1
	`, olderThan)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UploadSession
	for rows.Next() {
		var s UploadSession
		if err := rows.Scan(&s.ID, &s.Filename, &s.ContentType, &s.TotalSize, &s.ChunkSize, &s.TotalChunks, &s.ReceivedChunks, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func IsNoRows(err error) bool {
	return err == pgx.ErrNoRows
}

func (p *Pool) Ping(ctx context.Context) error {
	if p == nil || p.inner == nil {
		return fmt.Errorf("db pool is nil")
	}
	return p.inner.Ping(ctx)
}
