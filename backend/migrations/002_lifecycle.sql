-- Four-table lifecycle: completed / upload_sessions / failed_sessions / expired.
-- Rows are moved, not copied. A uuid lives in exactly one table at a time.

ALTER TABLE upload_sessions ADD COLUMN IF NOT EXISTS last_progress_at TIMESTAMPTZ;
UPDATE upload_sessions SET last_progress_at = created_at WHERE last_progress_at IS NULL;
ALTER TABLE upload_sessions ALTER COLUMN last_progress_at SET DEFAULT now();
ALTER TABLE upload_sessions ALTER COLUMN last_progress_at SET NOT NULL;
ALTER TABLE upload_sessions ADD COLUMN IF NOT EXISTS password TEXT;

CREATE INDEX IF NOT EXISTS upload_sessions_last_progress_at_idx ON upload_sessions (last_progress_at);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'public' AND table_name = 'files'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'public' AND table_name = 'completed'
    ) THEN
        ALTER TABLE files ADD COLUMN IF NOT EXISTS id UUID;
        UPDATE files SET id = gen_random_uuid() WHERE id IS NULL;
        ALTER TABLE files ALTER COLUMN id SET NOT NULL;
        CREATE UNIQUE INDEX IF NOT EXISTS files_id_uidx ON files (id);
        ALTER TABLE files DROP COLUMN IF EXISTS status;
        ALTER TABLE files RENAME TO completed;
        IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'files_expires_at_idx') THEN
            ALTER INDEX files_expires_at_idx RENAME TO completed_expires_at_idx;
        END IF;
        IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'files_id_uidx') THEN
            ALTER INDEX files_id_uidx RENAME TO completed_id_uidx;
        END IF;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = 'public' AND table_name = 'completed'
    ) THEN
        ALTER TABLE completed ADD COLUMN IF NOT EXISTS id UUID;
        UPDATE completed SET id = gen_random_uuid() WHERE id IS NULL;
        IF NOT EXISTS (SELECT 1 FROM completed WHERE id IS NULL) THEN
            ALTER TABLE completed ALTER COLUMN id SET NOT NULL;
        END IF;
        ALTER TABLE completed DROP COLUMN IF EXISTS status;
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS completed_id_uidx ON completed (id);
CREATE INDEX IF NOT EXISTS completed_expires_at_idx ON completed (expires_at);

CREATE TABLE IF NOT EXISTS failed_sessions (
    id UUID PRIMARY KEY,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    total_size BIGINT NOT NULL,
    chunk_size INTEGER NOT NULL,
    total_chunks INTEGER NOT NULL,
    received_chunks BOOLEAN[] NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    password TEXT,
    user_cancelled BOOLEAN NOT NULL DEFAULT false,
    reason_code TEXT NOT NULL,
    http_status INTEGER,
    reason_detail TEXT,
    failed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS failed_sessions_failed_at_idx ON failed_sessions (failed_at);
CREATE INDEX IF NOT EXISTS failed_sessions_user_cancelled_idx ON failed_sessions (user_cancelled);

CREATE TABLE IF NOT EXISTS expired (
    id UUID PRIMARY KEY,
    code VARCHAR(16) NOT NULL UNIQUE,
    filename TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    password TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    expired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    seaweed_path TEXT
);

CREATE INDEX IF NOT EXISTS expired_expired_at_idx ON expired (expired_at);
