# Validation results

These checks were run against the local application during implementation.

## Passed

- Standard Go formatting check, go vet, and compilation of server, detector, simulator, and collector.
- Unit tests for telemetry, Linux CPU/memory parsing, ping-loss parsing, SNMP response decoding, authentication, strict JSON input, and AI response validation.
- go test -race ./... with live Redis and PostgreSQL integration tests enabled.
- Redis duplicate-delivery handling, dead-letter routing, failed-output recovery, acknowledgment, and abandoned-message reclaim.
- PostgreSQL duplicate inserts, device history, incident persistence, status changes, and analysis lease recovery.
- Full-stack smoke test: authenticated ingestion -> Redis -> detector -> PostgreSQL -> API.
- Real Linux / WSL host collection: 6.4% CPU, 21.9% memory utilization, and 0% loss on five ICMP probes to loopback at the tested instant. Values are observations, not performance benchmarks or measurements of Windows.
- Native Windows host collection: three real readings (CPU 23%, 86%, and 81%; physical memory about 94.1%; five loopback probes with 0% loss) were accepted and returned by persisted device history with source windows. PowerShell syntax validation and Redis source-preservation integration tests passed. These values describe the tested moments. A separate background collector was started for continued live updates.
- Actual local Ollama generation with qwen2.5:0.5b, structured analysis, and application-generated evidence.
- JavaScript syntax / Prettier formatting and a clean git diff whitespace check.

## Limits of verification

- SNMP parsing and missing-OID handling were tested with controlled responses. No physical router or switch was available, so vendor-specific OIDs, network reachability, and SNMP credentials require validation on your device.
- Browser preview of localhost:8080 was denied by the browser permission policy. Dashboard visual layout and browser interactions were not verified in this session. API behavior and the served application build were tested.
- The smoke test adds explicitly simulated test readings and retains them as evidence. It does not delete existing application data.

## Reproduce

```bash
cd /home/gurveer/netwatch
./scripts/test.sh
python3 scripts/smoke.py
```

Redis tests use unique temporary keys. PostgreSQL tests use a unique temporary schema. The live smoke test uses a unique simulated device label and collects a real reading from wsl-host.

Docker's existing data disk was migrated to D:\DockerDesktopData\disk with byte-for-byte SHA-256 verification while held read-only. Its original C: folder is a directory junction to that location. Existing Docker image names and volumes were checked after restart, including the pre-existing non-NetWatch resources.
