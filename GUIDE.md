# NetWatch

A local network-health application written in Go. Redis Streams carry telemetry and incidents, PostgreSQL preserves history, and a browser dashboard shows device health and incident investigation. Ollama provides advisory analysis using a local model.

## Start the full application

In Ubuntu / WSL:

```bash
cd /home/gurveer/netwatch
./scripts/setup.sh
docker compose up -d --build
```

Open http://localhost:8080. Sign in with NETWATCH_API_TOKEN from the private .env file. To see it locally, run:

```bash
grep '^NETWATCH_API_TOKEN=' .env
```

The first startup downloads the application images and a small local language model. The server waits for the model download. Data survives ordinary container restarts in named volumes. No cloud AI account or paid key is used.

The simulator emits a reading every five seconds and a CPU spike every sixth reading. Initially, the detector also consumes retained Phase 1 history. An incident is created for each reading strictly above 90% CPU. The dashboard refreshes every five seconds. Open an incident to see evidence, tentative causes, suggested checks, and missing information. You can resolve/reopen incidents and request another analysis.

## Collect actual Linux / WSL metrics

The collector reads CPU deltas from /proc/stat and memory from /proc/meminfo. It measures ICMP packet loss with five ping probes. Run it on the host you want to observe:

```bash
./scripts/collect-linux.sh -device wsl-host -target 127.0.0.1
```

For a finite check:

```bash
./scripts/collect-linux.sh -device wsl-host -count 2 -interval 1s
```

This measures the real Ubuntu WSL host, not the Windows host. The script obtains a Linux collector binary from the running server container if needed, loads the API token without displaying it, and publishes to the authenticated ingestion API. Linux requires /proc with MemAvailable and iputils ping. The default target is loopback; supply an authorized reachable device IP to measure the path to that device.

Packet loss here means loss on five ICMP probes from the collector to the target. It is not the packet loss of all application traffic or an interface discard rate. Blocking ICMP can produce measured 100% probe loss even when other services work. If probing or metric parsing fails, the collector reports the failure and does not invent a reading.

## Collect the Windows host running WSL

Run the native PowerShell collector on Windows. Keep its source in scripts/collect-windows.ps1, but copy it to a local Windows folder before execution: Windows RemoteSigned policy can reject unsigned scripts on the WSL network share. No execution-policy change is needed for the approved local copy used in this session.

From a Windows PowerShell terminal, copy the script and run it:

```powershell
$collectorFolder = Join-Path $env:USERPROFILE 'netwatch-collector'
New-Item -ItemType Directory -Path $collectorFolder -Force | Out-Null
Copy-Item -LiteralPath '\\wsl.localhost\Ubuntu\home\gurveer\netwatch\scripts\collect-windows.ps1' -Destination $collectorFolder
& (Join-Path $collectorFolder 'collect-windows.ps1') -SettingsPath '\\wsl.localhost\Ubuntu\home\gurveer\netwatch\.env'
```

It publishes as windows-host with source windows. The dashboard labels it Windows · live, separately from the Ubuntu WSL host. CPU is Windows' aggregate PercentProcessorTime counter. Memory utilization is calculated from the operating system's visible total and free physical memory. These measurements include the Windows host's workloads, including WSL and Docker. Their sampling and definitions can differ slightly from Task Manager's displayed values.

Five .NET ICMP probes measure loss from Windows to the selected target. The default target is 127.0.0.1 (loopback), not the internet. For another authorized destination, pass -PingTarget ADDRESS. No reading is fabricated when collection or publishing fails.

The script reads .env as key/value data and never logs the API token. It uses built-in Windows CIM and .NET; no extra module or Windows Go installation is required. Press Ctrl+C to stop a foreground collector. For a finite check, append -Count 3 -IntervalSeconds 1 to the command. Parameters: DeviceId, ApiUrl, PingTarget, SettingsPath, IntervalSeconds (delay between completed cycles), and Count (zero means continuous).

The collector started during this session uses the local copy at C:\Users\singh\Documents\Codex\2026-10-13\h\outputs\windows-collector\collect-windows.ps1. Its log and PID files are alongside it. This is a background process for the current Windows session; it does not install a service or start automatically after reboot. Restart it using the foreground command above after reboot. Stop the existing background collector before starting another copy for the same device.

## Collect a real router or switch with SNMP

Use read-only SNMP access to an authorized device. SNMP v3 authPriv is the default, with SHA-256 authentication and AES privacy. Your configured device user must support those protocols. Supply three scalar numeric OIDs supported by your device:

- CPU utilization expressed as 0-100 percent.
- Used memory.
- Total memory, in the same units as used memory.

Different platforms expose different OIDs. This collector deliberately requires your device's OIDs instead of assuming one vendor's MIB applies everywhere. Memory utilization is calculated as used / total * 100. Some models expose free memory rather than total; choose the correct total OID rather than passing free memory as total.

Copy the collector binary locally if you have not used it yet:

```bash
mkdir -p bin
docker compose cp server:/app/collector bin/collector
chmod +x bin/collector
```

Set credentials in your shell without committing them. The API token is read from .env; SNMP secrets are entered privately:

```bash
export NETWATCH_API_TOKEN="$(sed -n 's/^NETWATCH_API_TOKEN=//p' .env)"
read -rp 'SNMP username: ' SNMP_USER
read -rsp 'SNMP authentication password: ' SNMP_AUTH_PASSWORD; printf '\n'
read -rsp 'SNMP privacy password: ' SNMP_PRIV_PASSWORD; printf '\n'
export SNMP_USER SNMP_AUTH_PASSWORD SNMP_PRIV_PASSWORD
```

Replace the example placeholders with your actual IP and numeric scalar OIDs, including their instance suffix:

```bash
./bin/collector -mode snmp -device lab-router -target DEVICE_IP \
  -cpu-oid CPU_OID -memory-used-oid USED_MEMORY_OID \
  -memory-total-oid TOTAL_MEMORY_OID
```

For a lab that only supports SNMP v2c, set SNMP_COMMUNITY and pass -snmp-version 2c. This sends the community in plaintext; prefer v3 where available. UDP 161 and ICMP must be reachable from the collector. Physical-router interoperability cannot be verified without access to your device and its credentials. Automated tests check decoding and missing-OID behavior using controlled SNMP responses.

## Architecture

```text
Simulator ───────┐
                ├─→ telemetry:raw ─→ detector group ─→ incidents:raw
Real collector ─┘       │                                 │
  via POST API          └─→ storage group → PostgreSQL ← incident storage group
                                                │
                                         durable analysis queue
                                                │
                                              Ollama
                                                │
                                      authenticated API → dashboard
```

Source labels are carried end-to-end: simulated, linux, windows, snmp, external, or unknown for old events without a source. A source label is collector-provided metadata, not cryptographic device identity.

The detector acknowledges only after saving its Redis outcome. PostgreSQL consumers acknowledge only after a successful idempotent insert. Abandoned Redis deliveries are reclaimed after thirty seconds. Malformed telemetry goes to a dead-letter stream. PostgreSQL stores incident IDs uniquely, so repeated delivery does not create duplicate rows. Redis delivery is at least once; this is not an end-to-end exactly-once guarantee.

AI jobs are stored in PostgreSQL. A ninety-second lease recovers jobs after a server stops. Each AI request has a sixty-second timeout. Failed requests retry with a delay, up to three normal attempts, then remain visible as errors. The dashboard can queue another attempt. Evidence text is assembled from actual input by Go; the language model supplies tentative explanations and read-only checks. Nothing executes the suggestions. The small default model is for a local demonstration; stronger models can improve explanations.

## API

Browser sessions use an HttpOnly SameSite cookie. Collectors and CLI clients use Authorization: Bearer TOKEN. Mutation endpoints reject a mismatched browser Origin. The default deployment binds exposed ports to localhost and keeps PostgreSQL and Ollama on the internal Docker network.

| Method | Endpoint | Purpose |
| --- | --- | --- |
| POST | /api/login | Exchange access token for browser cookie |
| POST | /api/logout | Clear browser cookie |
| GET | /api/config | AI settings |
| GET | /api/devices | Latest reading per device |
| GET | /api/devices/{device}/history?limit=120 | Device history, newest first |
| GET | /api/incidents?status=open&limit=100 | Incident inbox |
| PATCH | /api/incidents/{id} | Set status to open or resolved |
| POST | /api/incidents/{id}/analyze | Queue another AI attempt |
| POST | /api/telemetry | Validate and enqueue a complete telemetry reading |
| GET | /health/live | Process health |
| GET | /health/ready | PostgreSQL and Redis readiness |

POST telemetry requires device_id, timestamp, cpu_percent, memory_percent, and packet_loss_pct. source is optional for legacy compatibility. Percentages must be 0-100; timestamps must be nonzero and cannot be more than five minutes in the future. HTTP 202 means accepted into Redis, not yet persisted. Each publish creates a new event ID. Requests are bounded to 16 KiB; query limits are capped at 500.

## Code reading order

1. internal/telemetry: shared event shape.
2. cmd/simulator: generate and publish a reading.
3. internal/monitor: decode, detect, save, acknowledge, and recover.
4. internal/store: PostgreSQL schema and idempotent inserts.
5. internal/pipeline: separate consumer groups save telemetry and incidents.
6. internal/ai: local model request, response validation, and durable job runner.
7. internal/api: authenticated HTTP handlers and embedded dashboard.
8. internal/collect and cmd/collector: actual Linux and SNMP measurements.
9. cmd/server: wire services together and handle shutdown.

Go uses standard gofmt formatting. Browser assets use Prettier formatting. Keep functions focused on one stage of the flow; the comments explain acknowledgment and failure behavior where mistakes would lose or duplicate work.

## Build and test

```bash
./build.sh
./scripts/test.sh
```

build.sh formats Go, runs unit tests and go vet, and builds four commands. It falls back to a Go Docker container when Go is absent from PATH. test.sh runs the race detector and integration tests against the Compose services. Tests use isolated Redis keys and a temporary PostgreSQL schema, preserving application data. CI repeats these checks using dedicated services.

Coverage includes CPU counter math, memory parsing, ICMP summaries, SNMP response handling, authenticated ingestion client, token/cookie authentication, cross-origin mutation rejection, strict JSON parsing, stream deduplication, dead letters, failed Redis output, pending-message recovery, PostgreSQL deduplication, and analysis leases. AI unit tests use an HTTP model stub; an actual local model is additionally checked during the application smoke test.

## Operations and scope

```bash
docker compose ps
docker compose logs --tail 50 server worker
docker compose down
```

Ordinary down preserves named volumes. Streams, deduplication hashes, and database history are unbounded in this development project; set coordinated retention and backups before long-running operation. No destructive data-reset command is part of normal setup.

To run without local AI, set AI_ENABLED=false and COMPOSE_PROFILES= in .env, then recreate the server. For another Ollama model, change OLLAMA_MODEL and rerun model-init before recreating server. Pin the Ollama image digest for a repeatable deployment after selecting your runtime.

This is a single-node local application with shared-token access, one implemented high-CPU rule, and polling in the UI. It is ready for a local project demonstration. Production multi-user access, TLS termination, scale testing, retention, and platform-specific collectors remain separate work.

References: Redis Streams https://redis.io/docs/latest/develop/data-types/streams/ ; Ollama API https://docs.ollama.com/api ; pgx https://github.com/jackc/pgx
