#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
if [[ ! -f .env ]]; then printf 'Run ./scripts/setup.sh first.\n' >&2; exit 1; fi
api_port=8080
while IFS='=' read -r key value; do
  case "$key" in
    NETWATCH_API_TOKEN) export NETWATCH_API_TOKEN="$value" ;;
    HTTP_PORT) api_port="$value" ;;
  esac
done < .env
if [[ ! -x bin/collector ]]; then
  mkdir -p bin
  docker compose cp server:/app/collector bin/collector
  chmod +x bin/collector
fi
exec ./bin/collector -mode linux -api-url "http://localhost:${api_port}" "$@"
