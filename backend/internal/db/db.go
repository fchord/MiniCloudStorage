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
	LastProgressAt time.Time
	Password       *string
}

type Completed struct {
	ID          uuid.UUID
	Code        string
	Filename    string
	ContentType string
	SizeBytes   int64
	SeaweedPath string
	Password    *string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

type Expired struct {
	ID          uuid.UUID
	Code        string
	Filename    string
	SizeBytes   int64
	ContentType string
	Password    *string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	ExpiredAt   time.Time
	SeaweedPath string
}

type FailedSession struct {
	ID             uuid.UUID
	Filename       string
	ContentType    string
	TotalSize      int64
	ChunkSize      int
	TotalChunks    int
	ReceivedChunks []bool
	CreatedAt      time.Time
	Password       *string
	UserCancelled  bool
	ReasonCode     string
	HTTPStatus     *int
	ReasonDetail   *string
	FailedAt       time.Time
}

type AdminRecord struct {
	ID          uuid.UUID
	Kind        string
	Code        string
	Filename    string
	SizeBytes   int64
	StoragePath string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	Password    *string
	ReasonCode  string
}

type FailSessionParams struct {
	Session       UploadSession
	UserCancelled bool
	ReasonCode    string
	HTTPStatus    *int
	ReasonDetail  string
}

func Connect(ctx context.Context, databaseURL string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 16
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

func (p *Pool) Ping(ctx context.Context) error {
	if p == nil || p.inner == nil {
		return fmt.Errorf("db pool is nil")
	}
	return p.inner.Ping(ctx)
}

func IsNoRows(err error) bool {
	return err == pgx.ErrNoRows
}

func (p *Pool) InsertSession(ctx context.Context, s UploadSession) error {
	_, err := p.inner.Exec(ctx, `
		INSERT INTO upload_sessions (
			id, filename, content_type, total_size, chunk_size, total_chunks,
			received_chunks, password, last_progress_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
	`, s.ID, s.Filename, s.ContentType, s.TotalSize, s.ChunkSize, s.TotalChunks, s.ReceivedChunks, s.Password)
	return err
}

func (p *Pool) GetSession(ctx context.Context, id uuid.UUID) (UploadSession, error) {
	var s UploadSession
	err := p.inner.QueryRow(ctx, `
		SELECT id, filename, content_type, total_size, chunk_size, total_chunks,
		       received_chunks, created_at, last_progress_at, password
		FROM upload_sessions WHERE id = $1
	`, id).Scan(
		&s.ID, &s.Filename, &s.ContentType, &s.TotalSize, &s.ChunkSize, &s.TotalChunks,
		&s.ReceivedChunks, &s.CreatedAt, &s.LastProgressAt, &s.Password,
	)
	return s, err
}

func (p *Pool) MarkChunk(ctx context.Context, id uuid.UUID, index int) (UploadSession, error) {
	var s UploadSession
	err := p.inner.QueryRow(ctx, `
		UPDATE upload_sessions
		SET received_chunks[$1] = TRUE, last_progress_at = now()
		WHERE id = $2
		RETURNING id, filename, content_type, total_size, chunk_size, total_chunks,
		          received_chunks, created_at, last_progress_at, password
	`, index+1, id).Scan(
		&s.ID, &s.Filename, &s.ContentType, &s.TotalSize, &s.ChunkSize, &s.TotalChunks,
		&s.ReceivedChunks, &s.CreatedAt, &s.LastProgressAt, &s.Password,
	)
	return s, err
}

func (p *Pool) CompleteSession(ctx context.Context, file Completed) error {
	tx, err := p.inner.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var locked uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT id FROM upload_sessions WHERE id = $1 FOR UPDATE`, file.ID,
	).Scan(&locked); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO completed (
			id, code, filename, content_type, size_bytes, seaweed_path,
			password, created_at, expires_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, file.ID, file.Code, file.Filename, file.ContentType, file.SizeBytes,
		file.SeaweedPath, file.Password, file.CreatedAt, file.ExpiresAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM upload_sessions WHERE id = $1`, file.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Pool) FailSession(ctx context.Context, in FailSessionParams) error {
	tx, err := p.inner.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	s := in.Session
	var locked uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT id FROM upload_sessions WHERE id = $1 FOR UPDATE`, s.ID,
	).Scan(&locked)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}

	detail := TruncateDetail(in.ReasonDetail)
	var detailArg any
	if detail != "" {
		detailArg = detail
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO failed_sessions (
			id, filename, content_type, total_size, chunk_size, total_chunks,
			received_chunks, created_at, password, user_cancelled, reason_code,
			http_status, reason_detail
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (id) DO NOTHING
	`, s.ID, s.Filename, s.ContentType, s.TotalSize, s.ChunkSize, s.TotalChunks,
		s.ReceivedChunks, s.CreatedAt, s.Password, in.UserCancelled, in.ReasonCode,
		in.HTTPStatus, detailArg); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM upload_sessions WHERE id = $1`, s.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Pool) GetCompleted(ctx context.Context, code string) (Completed, error) {
	var f Completed
	err := p.inner.QueryRow(ctx, `
		SELECT id, code, filename, content_type, size_bytes, seaweed_path,
		       password, created_at, expires_at
		FROM completed WHERE code = $1
	`, code).Scan(
		&f.ID, &f.Code, &f.Filename, &f.ContentType, &f.SizeBytes, &f.SeaweedPath,
		&f.Password, &f.CreatedAt, &f.ExpiresAt,
	)
	return f, err
}

func (p *Pool) GetExpired(ctx context.Context, code string) (Expired, error) {
	var f Expired
	err := p.inner.QueryRow(ctx, `
		SELECT id, code, filename, size_bytes, content_type, password,
		       created_at, expires_at, expired_at, COALESCE(seaweed_path, '')
		FROM expired WHERE code = $1
	`, code).Scan(
		&f.ID, &f.Code, &f.Filename, &f.SizeBytes, &f.ContentType, &f.Password,
		&f.CreatedAt, &f.ExpiresAt, &f.ExpiredAt, &f.SeaweedPath,
	)
	return f, err
}

func (p *Pool) CodeExists(ctx context.Context, code string) (bool, error) {
	var exists bool
	err := p.inner.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM completed WHERE code = $1
			UNION ALL
			SELECT 1 FROM expired WHERE code = $1
		)
	`, code).Scan(&exists)
	return exists, err
}

func (p *Pool) DueCompleted(ctx context.Context, now time.Time) ([]Completed, error) {
	rows, err := p.inner.Query(ctx, `
		SELECT id, code, filename, content_type, size_bytes, seaweed_path,
		       password, created_at, expires_at
		FROM completed
		WHERE expires_at <= $1
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Completed
	for rows.Next() {
		var f Completed
		if err := rows.Scan(
			&f.ID, &f.Code, &f.Filename, &f.ContentType, &f.SizeBytes, &f.SeaweedPath,
			&f.Password, &f.CreatedAt, &f.ExpiresAt,
		); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (p *Pool) MoveCompletedToExpired(ctx context.Context, f Completed, seaweedPath string) error {
	tx, err := p.inner.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var locked uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT id FROM completed WHERE id = $1 FOR UPDATE`, f.ID,
	).Scan(&locked); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO expired (
			id, code, filename, size_bytes, content_type, password,
			created_at, expires_at, seaweed_path
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET
			seaweed_path = EXCLUDED.seaweed_path,
			expired_at = expired.expired_at
	`, f.ID, f.Code, f.Filename, f.SizeBytes, f.ContentType, f.Password,
		f.CreatedAt, f.ExpiresAt, seaweedPath); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM completed WHERE id = $1`, f.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Pool) StaleSessions(ctx context.Context, olderThan time.Time) ([]UploadSession, error) {
	rows, err := p.inner.Query(ctx, `
		SELECT id, filename, content_type, total_size, chunk_size, total_chunks,
		       received_chunks, created_at, last_progress_at, password
		FROM upload_sessions
		WHERE last_progress_at < $1
	`, olderThan)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UploadSession
	for rows.Next() {
		var s UploadSession
		if err := rows.Scan(
			&s.ID, &s.Filename, &s.ContentType, &s.TotalSize, &s.ChunkSize, &s.TotalChunks,
			&s.ReceivedChunks, &s.CreatedAt, &s.LastProgressAt, &s.Password,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Pool) DeleteOldFailed(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := p.inner.Exec(ctx, `DELETE FROM failed_sessions WHERE failed_at < $1`, olderThan)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (p *Pool) DeleteOldExpired(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := p.inner.Exec(ctx, `DELETE FROM expired WHERE expired_at < $1`, olderThan)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (p *Pool) ListAdmin(ctx context.Context, tab string) ([]AdminRecord, error) {
	switch tab {
	case TabInProgress:
		return p.listInProgress(ctx)
	case TabCompleted:
		return p.listCompleted(ctx)
	case TabCancelled:
		return p.listFailed(ctx, true)
	case TabOtherFailed:
		return p.listFailed(ctx, false)
	case TabExpired:
		return p.listExpired(ctx)
	default:
		return nil, fmt.Errorf("unknown tab %q", tab)
	}
}

func (p *Pool) listInProgress(ctx context.Context) ([]AdminRecord, error) {
	rows, err := p.inner.Query(ctx, `
		SELECT id, filename, total_size, created_at, password
		FROM upload_sessions
		ORDER BY last_progress_at DESC
		LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminRecord
	for rows.Next() {
		var r AdminRecord
		if err := rows.Scan(&r.ID, &r.Filename, &r.SizeBytes, &r.CreatedAt, &r.Password); err != nil {
			return nil, err
		}
		r.Kind = TabInProgress
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Pool) listCompleted(ctx context.Context) ([]AdminRecord, error) {
	rows, err := p.inner.Query(ctx, `
		SELECT id, code, filename, size_bytes, seaweed_path, created_at, expires_at, password
		FROM completed
		ORDER BY created_at DESC
		LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminRecord
	for rows.Next() {
		var r AdminRecord
		var expires time.Time
		if err := rows.Scan(&r.ID, &r.Code, &r.Filename, &r.SizeBytes, &r.StoragePath, &r.CreatedAt, &expires, &r.Password); err != nil {
			return nil, err
		}
		r.Kind = TabCompleted
		r.ExpiresAt = &expires
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Pool) listFailed(ctx context.Context, cancelled bool) ([]AdminRecord, error) {
	rows, err := p.inner.Query(ctx, `
		SELECT id, filename, total_size, created_at, password, reason_code
		FROM failed_sessions
		WHERE user_cancelled = $1
		ORDER BY failed_at DESC
		LIMIT 500
	`, cancelled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	kind := TabOtherFailed
	if cancelled {
		kind = TabCancelled
	}
	var out []AdminRecord
	for rows.Next() {
		var r AdminRecord
		if err := rows.Scan(&r.ID, &r.Filename, &r.SizeBytes, &r.CreatedAt, &r.Password, &r.ReasonCode); err != nil {
			return nil, err
		}
		r.Kind = kind
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Pool) listExpired(ctx context.Context) ([]AdminRecord, error) {
	rows, err := p.inner.Query(ctx, `
		SELECT id, code, filename, size_bytes, created_at, expires_at, password
		FROM expired
		ORDER BY expired_at DESC
		LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminRecord
	for rows.Next() {
		var r AdminRecord
		var expires time.Time
		if err := rows.Scan(&r.ID, &r.Code, &r.Filename, &r.SizeBytes, &r.CreatedAt, &expires, &r.Password); err != nil {
			return nil, err
		}
		r.Kind = TabExpired
		r.ExpiresAt = &expires
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Pool) GetSessionByID(ctx context.Context, id uuid.UUID) (UploadSession, error) {
	return p.GetSession(ctx, id)
}

func (p *Pool) GetCompletedByID(ctx context.Context, id uuid.UUID) (Completed, error) {
	var f Completed
	err := p.inner.QueryRow(ctx, `
		SELECT id, code, filename, content_type, size_bytes, seaweed_path,
		       password, created_at, expires_at
		FROM completed WHERE id = $1
	`, id).Scan(
		&f.ID, &f.Code, &f.Filename, &f.ContentType, &f.SizeBytes, &f.SeaweedPath,
		&f.Password, &f.CreatedAt, &f.ExpiresAt,
	)
	return f, err
}

func (p *Pool) GetFailedByID(ctx context.Context, id uuid.UUID) (FailedSession, error) {
	var s FailedSession
	err := p.inner.QueryRow(ctx, `
		SELECT id, filename, content_type, total_size, chunk_size, total_chunks,
		       received_chunks, created_at, password, user_cancelled, reason_code,
		       http_status, reason_detail, failed_at
		FROM failed_sessions WHERE id = $1
	`, id).Scan(
		&s.ID, &s.Filename, &s.ContentType, &s.TotalSize, &s.ChunkSize, &s.TotalChunks,
		&s.ReceivedChunks, &s.CreatedAt, &s.Password, &s.UserCancelled, &s.ReasonCode,
		&s.HTTPStatus, &s.ReasonDetail, &s.FailedAt,
	)
	return s, err
}

func (p *Pool) GetExpiredByID(ctx context.Context, id uuid.UUID) (Expired, error) {
	var f Expired
	err := p.inner.QueryRow(ctx, `
		SELECT id, code, filename, size_bytes, content_type, password,
		       created_at, expires_at, expired_at, COALESCE(seaweed_path, '')
		FROM expired WHERE id = $1
	`, id).Scan(
		&f.ID, &f.Code, &f.Filename, &f.SizeBytes, &f.ContentType, &f.Password,
		&f.CreatedAt, &f.ExpiresAt, &f.ExpiredAt, &f.SeaweedPath,
	)
	return f, err
}

func (p *Pool) DeleteSessionRow(ctx context.Context, id uuid.UUID) error {
	_, err := p.inner.Exec(ctx, `DELETE FROM upload_sessions WHERE id = $1`, id)
	return err
}

func (p *Pool) DeleteCompletedRow(ctx context.Context, id uuid.UUID) error {
	_, err := p.inner.Exec(ctx, `DELETE FROM completed WHERE id = $1`, id)
	return err
}

func (p *Pool) DeleteFailedRow(ctx context.Context, id uuid.UUID) error {
	_, err := p.inner.Exec(ctx, `DELETE FROM failed_sessions WHERE id = $1`, id)
	return err
}

func (p *Pool) DeleteExpiredRow(ctx context.Context, id uuid.UUID) error {
	_, err := p.inner.Exec(ctx, `DELETE FROM expired WHERE id = $1`, id)
	return err
}

func (p *Pool) ClearPassword(ctx context.Context, id uuid.UUID) (string, error) {
	tag, err := p.inner.Exec(ctx, `UPDATE completed SET password = NULL WHERE id = $1 AND password IS NOT NULL AND password <> ''`, id)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() > 0 {
		return TabCompleted, nil
	}
	tag, err = p.inner.Exec(ctx, `UPDATE expired SET password = NULL WHERE id = $1 AND password IS NOT NULL AND password <> ''`, id)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() > 0 {
		return TabExpired, nil
	}
	tag, err = p.inner.Exec(ctx, `UPDATE failed_sessions SET password = NULL WHERE id = $1 AND password IS NOT NULL AND password <> ''`, id)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() > 0 {
		return "failed", nil
	}
	tag, err = p.inner.Exec(ctx, `UPDATE upload_sessions SET password = NULL WHERE id = $1 AND password IS NOT NULL AND password <> ''`, id)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() > 0 {
		return TabInProgress, nil
	}
	return "", pgx.ErrNoRows
}

func (p *Pool) LocateID(ctx context.Context, id uuid.UUID) (string, error) {
	var exists bool
	if err := p.inner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upload_sessions WHERE id=$1)`, id).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return TabInProgress, nil
	}
	if err := p.inner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM completed WHERE id=$1)`, id).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return TabCompleted, nil
	}
	if err := p.inner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM failed_sessions WHERE id=$1)`, id).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return "failed", nil
	}
	if err := p.inner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM expired WHERE id=$1)`, id).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return TabExpired, nil
	}
	return "", pgx.ErrNoRows
}

func (p *Pool) CountCompletedMissingUUID(ctx context.Context) (int, error) {
	var n int
	err := p.inner.QueryRow(ctx, `SELECT COUNT(*) FROM completed WHERE id IS NULL`).Scan(&n)
	return n, err
}

func (p *Pool) CountCompleted(ctx context.Context) (int, error) {
	var n int
	err := p.inner.QueryRow(ctx, `SELECT COUNT(*) FROM completed`).Scan(&n)
	return n, err
}
