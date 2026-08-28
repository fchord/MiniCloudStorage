#!/bin/sh
set -eu
HBA=/pgconf/pg_hba.conf
if grep -q "MiniCloudStorage isolated access" "$HBA"; then
  echo "pg_hba already patched"
  exit 0
fi
# Insert password auth for this DB/role before the catch-all local peer rule.
awk '
  $0 ~ /^local[[:space:]]+all[[:space:]]+all[[:space:]]+peer/ && !done {
    print "local   minicloudstorage   minicloudstorage                    scram-sha-256"
    done=1
  }
  { print }
' "$HBA" > /tmp/pg_hba.conf
cat >> /tmp/pg_hba.conf <<'EOF'

# MiniCloudStorage isolated access
host    minicloudstorage   minicloudstorage   127.0.0.1/32            scram-sha-256
host    minicloudstorage   minicloudstorage   192.168.43.111/32       scram-sha-256
host    minicloudstorage   minicloudstorage   10.244.0.0/16           scram-sha-256
EOF
cp /tmp/pg_hba.conf "$HBA"
echo "pg_hba patched"
