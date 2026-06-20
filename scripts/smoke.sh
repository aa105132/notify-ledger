#!/usr/bin/env bash
set -euo pipefail
BASE_URL="${1:-http://127.0.0.1:8098}"
echo "[1/1] healthz: ${BASE_URL}/healthz"
curl -fsS "${BASE_URL}/healthz"
echo
echo "smoke ok"
