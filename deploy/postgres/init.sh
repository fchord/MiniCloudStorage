#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PASS_FILE="$ROOT/deploy/postgres/.app_password"
SQL_FILE="$ROOT/deploy/postgres/.init.local.sql"

mkdir -p "$ROOT/deploy/postgres"
if [[ ! -f "$PASS_FILE" ]]; then
  openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 24 > "$PASS_FILE"
  chmod 600 "$PASS_FILE"
fi
PASS="$(cat "$PASS_FILE")"

python3 - "$SQL_FILE" "$PASS" <<'PY'
import sys
path, password = sys.argv[1], sys.argv[2]
# Escape single quotes for SQL string literals.
escaped = password.replace("'", "''")
sql = f"""
SELECT version();

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'minicloudstorage') THEN
    CREATE ROLE minicloudstorage LOGIN PASSWORD '{escaped}';
  ELSE
    ALTER ROLE minicloudstorage WITH LOGIN PASSWORD '{escaped}';
  END IF;
END$$;

SELECT 'role_ok' AS status;

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_database WHERE datname = 'minicloudstorage') THEN
    PERFORM 1;
  END IF;
END$$;
"""
open(path, "w").write(sql)
PY

# CREATE DATABASE cannot run inside a DO block. Handle it separately.
docker run --rm --user 113:121 \
  -v /var/run/postgresql:/var/run/postgresql \
  -v "$SQL_FILE":/sql/role.sql:ro \
  postgres:18 \
  psql -h /var/run/postgresql -U postgres -d postgres -v ON_ERROR_STOP=1 -f /sql/role.sql

docker run --rm --user 113:121 \
  -v /var/run/postgresql:/var/run/postgresql \
  postgres:18 \
  psql -h /var/run/postgresql -U postgres -d postgres -v ON_ERROR_STOP=1 \
  -c "SELECT 'exists' FROM pg_database WHERE datname = 'minicloudstorage'" | grep -q exists \
  || docker run --rm --user 113:121 \
       -v /var/run/postgresql:/var/run/postgresql \
       postgres:18 \
       psql -h /var/run/postgresql -U postgres -d postgres -v ON_ERROR_STOP=1 \
       -c "CREATE DATABASE minicloudstorage OWNER minicloudstorage ENCODING 'UTF8' TEMPLATE template0;"

docker run --rm --user 113:121 \
  -v /var/run/postgresql:/var/run/postgresql \
  postgres:18 \
  psql -h /var/run/postgresql -U postgres -d postgres -v ON_ERROR_STOP=1 <<'SQL'
REVOKE ALL ON DATABASE minicloudstorage FROM PUBLIC;
GRANT CONNECT, TEMP ON DATABASE minicloudstorage TO minicloudstorage;
ALTER DATABASE minicloudstorage OWNER TO minicloudstorage;
SQL

docker run --rm --user 113:121 \
  -v /var/run/postgresql:/var/run/postgresql \
  postgres:18 \
  psql -h /var/run/postgresql -U postgres -d minicloudstorage -v ON_ERROR_STOP=1 <<'SQL'
GRANT ALL ON SCHEMA public TO minicloudstorage;
ALTER SCHEMA public OWNER TO minicloudstorage;
SQL

echo "PostgreSQL database minicloudstorage is ready."
