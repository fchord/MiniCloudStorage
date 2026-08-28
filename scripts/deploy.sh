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
print("secret_yaml_written")
PY

kubectl apply -f deploy/k8s/namespace.yaml
kubectl apply -f deploy/k8s/seaweedfs-filer-svc.yaml
kubectl apply -f "$ROOT/deploy/k8s/secret.local.yaml"

kubectl apply -f deploy/k8s/app.yaml

kubectl apply -f deploy/nginx/nginx-test-pod.yaml --dry-run=client >/dev/null
if kubectl -n default get pod nginx-test >/dev/null 2>&1; then
  kubectl -n default delete pod nginx-test --wait=true
fi
kubectl apply -f deploy/nginx/nginx-test-pod.yaml

kubectl -n "$NS" rollout status deploy/api --timeout=180s
kubectl -n default wait --for=condition=Ready pod/nginx-test --timeout=120s
echo "deployed. try: curl -sS https://minicloudstorage.19121122.xyz/api/v1/health"
