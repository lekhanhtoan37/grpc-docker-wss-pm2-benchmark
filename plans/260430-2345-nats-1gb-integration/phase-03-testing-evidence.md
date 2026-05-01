---
phase: 3
name: "Testing & Evidence Collection"
status: pending
priority: P1
effort: 1h
blocked-by: [phase-01, phase-02]
blocks: []
---

## Overview

Create `test-nats-integration.sh` smoke test for local macOS validation, run Go unit tests, and document the evidence needed to confirm NATS integration works before deploying to the production Linux server.

## Requirements

1. Smoke test script runs on macOS with zero infrastructure (no Kafka needed for core tests)
2. Verifies Go builds compile for both benchmark-client and nats-worker
3. Verifies NATS Docker container starts healthy
4. Verifies benchmark client connects to NATS and subscribes
5. Documents evidence tiers (macOS local, production Linux)

## Implementation Steps

### Step 1: Create `test-nats-integration.sh`

Script at project root, runnable on macOS:

```
Tests:
1. go test ./... in benchmark-client/go-client (29+ tests)
2. go build benchmark-client
3. go build nats-worker
4. Start NATS via Docker (infra/docker-compose.yml nats service)
5. Verify NATS /healthz endpoint
6. (Optional) Benchmark client NATS connectivity (3s quick run)
7. (Optional) nats-worker health if Kafka available
Cleanup: docker compose down nats
```

Each test prints PASS/FAIL with a final summary count.

### Step 2: Run and capture evidence on macOS

```bash
chmod +x test-nats-integration.sh
bash test-nats-integration.sh 2>&1 | tee test-nats-evidence.log
```

Expected results:
- Tests 1-3: PASS (builds + unit tests)
- Test 4-5: PASS (NATS Docker + health)
- Test 6: INFO or PASS (client connects to NATS)
- Test 7: SKIP (Kafka unavailable on macOS — expected)

### Step 3: Document production evidence checklist

Create evidence checklist for the Linux server run:

| # | Evidence | Command | Machine |
|---|----------|---------|---------|
| 1 | Go unit tests pass | `go test ./...` | macOS |
| 2 | Both binaries build | `go build` | macOS |
| 3 | NATS Docker healthy | `curl localhost:8222/healthz` | macOS |
| 4 | Benchmark client NATS connection logs | `go run main.go -warmup 2 -duration 3` | macOS |
| 5 | health-check-1gb.sh passes | `bash health-check-1gb.sh` | Linux |
| 6 | Short benchmark completes | `WARMUP=10 DURATION=30 RUNS=1 SCENARIOS="3" sudo bash run-benchmark-1gb.sh` | Linux |
| 7 | 12 groups in results | `grep "NATS" results/client-*.log` | Linux |

## Files Affected

| File | Change |
|------|--------|
| `test-nats-integration.sh` | New file — macOS smoke test script |

## Todo

- [ ] Create `test-nats-integration.sh` at project root
- [ ] Run smoke test on macOS and capture output
- [ ] Verify NATS Docker health check works
- [ ] Verify benchmark client shows NATS connection logs
- [ ] Document production evidence checklist in this phase file

## Success Criteria

- `test-nats-integration.sh` exits 0 on macOS (all non-Kafka tests PASS)
- `go test ./...` passes with 29+ tests
- Both `benchmark-client` and `nats-worker` binaries build
- NATS Docker container reports healthy via `/healthz`
- Benchmark client logs show `NATS bridge/host/PM2 connected to NATS`
- Evidence log captured for review before production deploy

## Risk Assessment

| Risk | Impact | Mitigation |
|------|--------|-----------|
| macOS Docker not running | Tests 4-5 skip | Print clear "Docker not running" message; not a blocker |
| Go version mismatch | Build fails | Script checks `go version` and reports requirement |
| NATS Docker image pull fails | Test 4 fails | Retry logic or manual pull instruction in output |
| Kafka unavailable on macOS | Test 7 skips | Expected; documented as SKIP with reason |
