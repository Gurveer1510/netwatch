#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
if [[ -e .env ]]; then
  printf 'Existing .env preserved. See .env.example for configuration options.\n'
  exit 0
fi
umask 077
python3 - <<'PY'
from pathlib import Path
import secrets
Path('.env').write_text(
    f'NETWATCH_API_TOKEN={secrets.token_hex(32)}\n'
    f'POSTGRES_PASSWORD={secrets.token_hex(24)}\n'
    'HTTP_PORT=8080\nREDIS_PORT=6380\nCOMPOSE_PROFILES=ai\n'
    'AI_ENABLED=true\nOLLAMA_MODEL=qwen2.5:0.5b\nOLLAMA_URL=http://ollama:11434\n'
)
PY
printf 'Created private .env with generated credentials.\n'
printf 'Start the full application with: docker compose up -d --build\n'
