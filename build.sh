#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")"
mkdir -p bin
if command -v go >/dev/null 2>&1; then
  gofmt -w cmd internal
  go test ./...
  go vet ./...
  for app in simulator worker server collector; do go build -o "bin/$app" "./cmd/$app"; done
else
  docker run --rm --user "$(id -u):$(id -g)" \
    -e GOCACHE=/tmp/go-cache -e GOPATH=/tmp/go \
    -v "$PWD:/app" -w /app golang:1.26.4 \
    sh -c 'gofmt -w cmd internal && go test ./... && go vet ./... && for app in simulator worker server collector; do go build -o bin/$app ./cmd/$app; done'
fi
