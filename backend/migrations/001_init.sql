CREATE TABLE IF NOT EXISTS upload_sessions (
    id UUID PRIMARY KEY,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    total_size BIGINT NOT NULL,
    chunk_size INTEGER NOT NULL,
    total_chunks INTEGER NOT NULL,
    received_chunks BOOLEAN[] NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS files (
    code VARCHAR(16) PRIMARY KEY,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    size_bytes BIGINT NOT NULL,
    seaweed_path TEXT NOT NULL,
    password TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'ready'
);

CREATE INDEX IF NOT EXISTS files_expires_at_idx ON files (expires_at);
CREATE INDEX IF NOT EXISTS upload_sessions_created_at_idx ON upload_sessions (created_at);
