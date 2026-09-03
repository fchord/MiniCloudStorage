#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

NS=minicloudstorage
PASS_FILE="$ROOT/deploy/postgres/.app_password"
if [[ ! -f "$PASS_FILE" ]]; then
  echo "missing $PASS_FILE ; run deploy/postgres/init.sh first" >&2
  exit 1
fi

ADMIN_PASS_FILE="$ROOT/deploy/k8s/.admin_password"
if [[ ! -f "$ADMIN_PASS_FILE" ]]; then
  umask 077
  openssl rand -base64 24 | tr -d '\n/+=\r' | head -c 24 > "$ADMIN_PASS_FILE"
  chmod 600 "$ADMIN_PASS_FILE"
  echo "wrote $ADMIN_PASS_FILE (gitignore; not printed)"
fi

python3 - <<'PY'
import base64
from pathlib import Path
root = Path("/home/liuzhi/work/mini_cloud_storage")
password = (root / "deploy/postgres/.app_password").read_text().strip()
url = f"postgres://minicloudstorage:{password}@192.168.43.111:5432/minicloudstorage?sslmode=disable"
b64 = base64.b64encode(url.encode()).decode()
(root / "deploy/k8s/secret.local.yaml").write_text(
f"""apiVersion: v1
kind: Secret
metadata:
  name: postgres
  namespace: minicloudstorage
  labels:
    app.kubernetes.io/part-of: minicloudstorage
type: Opaque
data:
  url: {b64}
"""
)
admin = (root / "deploy/k8s/.admin_password").read_text().strip().encode()
admin_b64 = base64.b64encode(admin).decode()
(root / "deploy/k8s/admin.secret.local.yaml").write_text(
f"""apiVersion: v1
kind: Secret
metadata:
  name: admin
  namespace: minicloudstorage
  labels:
    app.kubernetes.io/part-of: minicloudstorage
type: Opaque
data:
  password: {admin_b64}
"""
)
print("secret_yaml_written")
PY

kubectl apply -f deploy/k8s/namespace.yaml
kubectl apply -f deploy/k8s/seaweedfs-filer-svc.yaml
kubectl apply -f "$ROOT/deploy/k8s/secret.local.yaml"
kubectl apply -f "$ROOT/deploy/k8s/admin.secret.local.yaml"

kubectl apply -f deploy/k8s/app.yaml
kubectl -n "$NS" rollout restart deploy/api
kubectl -n "$NS" rollout status deploy/api --timeout=180s
echo "deployed. try: curl -sS https://minicloudstorage.19121122.xyz/api/v1/health"
