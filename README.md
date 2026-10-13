# NetWatch - Phase 1

A Go simulator publishes fake router readings into Redis Streams. This phase covers generating telemetry, JSON encoding, publishing with XADD, and inspecting events with redis-cli.

## Run in Ubuntu / WSL

Prerequisites: Go compatible with go.mod, Docker with Compose, and a running Docker daemon.

```bash
cd /home/gurveer/netwatch
docker compose up -d
docker compose exec redis redis-cli PING
go run ./cmd/simulator
```

Expected: PONG, followed by a published event every five seconds. The first event is emitted after five seconds. Every sixth reading has CPU usage in the 92-98% range. Press Ctrl+C to stop the simulator.

### If Go is not on your PATH

The initial setup also builds bin/simulator, so you can run it immediately:

```bash
./bin/simulator
```

After editing the Go source, rebuild and test using Docker:

```bash
docker run --rm --user "$(id -u):$(id -g)" \
  -e GOCACHE=/tmp/go-cache -e GOPATH=/tmp/go \
  -v "$PWD:/app" -w /app golang:1.26.4 \
  sh -c 'gofmt -w cmd/simulator/*.go && go test ./... && go vet ./... && go build -o bin/simulator ./cmd/simulator'
./bin/simulator
```

This container build needs network access to fetch dependencies. Go commands elsewhere in this README assume Go is available locally; otherwise use the build command above and run ./bin/simulator with the same flags.

Open a second terminal to inspect the stream:

```bash
cd /home/gurveer/netwatch
docker compose exec redis redis-cli XLEN telemetry:raw
docker compose exec redis redis-cli XREVRANGE telemetry:raw + - COUNT 5
```

Each entry contains a Redis-generated ID and a payload field with JSON:

```json
{"device_id":"router-01","timestamp":"2026-10-13T12:00:00Z","cpu_percent":35.2,"memory_percent":47.6,"packet_loss_pct":0.25}
```

All metrics are simulated percentages. Timestamp is UTC.

## Read the code in this order

1. Telemetry: the event schema and JSON field names.
2. generateTelemetry: normal ranges and the every-sixth-reading fault.
3. run: flags, Redis connection and initial PING.
4. The ticker loop: create a reading, marshal JSON, and call XAdd.
5. Context cancellation and deferred cleanup: Ctrl+C stops the loop and closes resources.

## Redis concepts to practice

- A key names a value in Redis. Here telemetry:raw is the key for a Stream.
- XADD appends an entry. Its generated ID is separate from the timestamp in your payload.
- XLEN counts retained entries.
- XRANGE reads oldest to newest; XREVRANGE reads newest to oldest.
- Streams retain entries for later readers. Consumer groups and acknowledgments will come in Phase 2.

Try these in the interactive CLI:

```bash
docker compose exec redis redis-cli
```

```text
TYPE telemetry:raw
XLEN telemetry:raw
XRANGE telemetry:raw - + COUNT 3
```

## Faster demo and checks

```bash
go run ./cmd/simulator -interval 500ms -count 6 -stream telemetry:demo
docker compose exec redis redis-cli XRANGE telemetry:demo - +
go test ./...
go vet ./...
```

The sixth event should have high CPU. Count limits reading attempts, not successful writes.

Other flags:

```bash
go run ./cmd/simulator -device router-02 -redis-addr localhost:6380
```

## Persistence and failure behavior

Compose enables Redis AOF persistence and a named volume. Default AOF fsync behavior can still lose recent writes after an abrupt host failure; it is not a promise of lossless delivery.

If Redis is unavailable at startup, the simulator exits with an actionable error. If a later publish fails, it logs the error, skips that reading, and continues. It does not buffer missed readings. Automatic write retries are disabled because a lost response can make the outcome of XADD uncertain. Later phases should still handle duplicates.

The stream is intentionally untrimmed for this small local exercise. Do not run it indefinitely without a retention policy.

Stop Redis while preserving its data:

```bash
docker compose down
```

Redis is exposed on host port 6380 to avoid conflicts with an existing Redis on 6379. Inside the container it still listens on 6379. If 6380 is occupied, choose another host port in compose.yaml and pass the matching -redis-addr.

## Phase 1 checkpoint

- Events appear at the configured interval.
- JSON includes device ID, UTC timestamp, CPU, memory, and packet loss.
- Every sixth generated reading has high CPU.
- Failed publishes are logged, and Ctrl+C stops cleanly.

Learning exercises: change the interval; simulate router-02; explain why json.Marshal runs before XAdd; stop and restart Redis to observe failure and recovery.

Reference: https://redis.io/docs/latest/develop/data-types/streams/
