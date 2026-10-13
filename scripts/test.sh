#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
# Real integration checks use unique Redis keys and a temporary PostgreSQL schema.
# The application's tables and events are not deleted by the tests.
if [[ ! -f .env ]]; then printf 'Run scripts/setup.sh and start the stack first.\n' >&2; exit 1; fi
database_password=''
while IFS='=' read -r key value; do
  if [[ "$key" == POSTGRES_PASSWORD ]]; then database_password="$value"; fi
done < .env
if [[ -z "$database_password" ]]; then printf 'POSTGRES_PASSWORD missing.\n' >&2; exit 1; fi
export NETWATCH_TEST_DATABASE_URL="postgres://netwatch:${database_password}@postgres:5432/netwatch?sslmode=disable"
export NETWATCH_TEST_REDIS_ADDR=redis:6379
docker run --rm --network netwatch_default --user "$(id -u):$(id -g)" \
  -e GOCACHE=/tmp/go-cache -e GOPATH=/tmp/go \
  -e NETWATCH_TEST_DATABASE_URL -e NETWATCH_TEST_REDIS_ADDR \
  -v "$PWD:/app" -w /app golang:1.26.4 \
  sh -c 'test -z "$(gofmt -l cmd internal)" && go vet ./... && go test -race ./...'
