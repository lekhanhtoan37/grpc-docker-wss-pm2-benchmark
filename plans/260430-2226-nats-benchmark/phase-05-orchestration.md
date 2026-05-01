---
title: "Phase 05: Orchestration — Shell Scripts, Docker Compose, Integration"
phase: 5-of-5
status: pending
priority: P1
effort: 3h
blocked-by: [phase-02, phase-04]
blocks: none
---

# Phase 05: Orchestration — Shell Scripts, Docker Compose, Integration

## Context

- `run-benchmark.sh` orchestrates full benchmark: Kafka → servers → producer → client → results
- `docker-compose.benchmark.yml` for distributed mode
- NATS needs startup/teardown integrated into existing flow

## Overview

Wire everything together. Update shell scripts to start NATS + nats-worker. Ensure benchmark client includes NATS groups. Full end-to-end test.

## Implementation Steps

### 1. Update `infra/docker-compose.yml` — NATS auto-starts with Kafka

Already covered in Phase 01. NATS service added alongside Zookeeper/Kafka.

### 2. Update `run-benchmark.sh` — add NATS steps

Insert after Step 2 (Kafka topic creation):

```bash
echo ""
echo "--- Step 2c: Verifying NATS ---"
if ! curl -sf http://localhost:8222/healthz > /dev/null 2>&1; then
  echo "Starting NATS..."
  cd "$BASEDIR/infra" && docker compose up -d nats
  echo "Waiting 5s for NATS startup..."
  sleep 5
fi
```

Insert after Step 5 (Go WS servers):

```bash
echo ""
echo "--- Step 5b: Starting NATS workers (host/PM2) ---"
cd "$BASEDIR/nats-worker"
if ! pm2 describe nats-benchmark &>/dev/null; then
  go build -o nats-worker .
  pm2 start ecosystem.config.js
  echo "Waiting 5s for NATS workers..."
  sleep 5
fi
cd "$BASEDIR"
```

### 3. Create `run-nats-benchmark.sh` — standalone NATS benchmark

Simpler script that only runs NATS benchmarks (Approach A + B):

```bash
#!/bin/bash
set -e

BASEDIR="$(cd "$(dirname "$0")" && pwd)"
NATS_URL="${NATS_URL:-nats://localhost:4222}"
WARMUP="${WARMUP:-30}"
DURATION="${DURATION:-120}"

# Start NATS if not running
if ! curl -sf http://localhost:8222/healthz > /dev/null 2>&1; then
  cd "$BASEDIR/infra" && docker compose up -d nats
  sleep 5
fi

# Run standalone NATS benchmark (Approach B)
cd "$BASEDIR/nats-bench"
go run main.go \
  -nats-url "$NATS_URL" \
  -subject "bench.test" \
  -mode both \
  -warmup "$WARMUP" \
  -duration "$DURATION" \
  -msg-size 1024
```

### 4. Update `docker-compose.benchmark.yml` — add NATS groups

Add NATS groups to coordinator config:

```yaml
# In coordinator command or config, add groups:
#   - {name: "NATS bridge", type: "nats", endpoints: ["nats://benchmark-nats:4222"], subject: "benchmark.messages", connections: 1}
#   - {name: "NATS host", type: "nats", endpoints: ["nats://host.docker.internal:4222"], subject: "benchmark.messages", connections: 3}
```

### 5. Create `nats-worker/docker-compose.yml` integration with infra network

Bridge mode nats-worker needs to reach both Kafka and NATS:

```yaml
services:
  nats-worker-1:
    build: .
    environment:
      CONTAINER_ID: "nats-bridge-1"
      KAFKA_BROKER: "192.168.0.9:9091"
      NATS_URL: "nats://benchmark-nats:4222"
    networks:
      - infra_backend  # external network from infra/docker-compose.yml

networks:
  infra_backend:
    external: true
```

### 6. Update `health-check.sh` — NATS worker health

```bash
echo "--- NATS Workers ---"
for port in 8095 8096 8097; do
  echo -n "  NATS worker :$port: "
  curl -sf "http://localhost:$port/health" && echo "OK" || echo "FAIL"
done
```

### 7. Update `.gitignore` — add nats-worker binary

```
nats-worker/nats-worker
nats-bench/nats-bench
```

### 8. End-to-end test sequence

```bash
# 1. Start infra (Kafka + NATS)
cd infra && docker compose up -d

# 2. Build nats-worker
cd nats-worker && go build -o nats-worker .

# 3. Start nats-worker (host mode — simplest for testing)
KAFKA_BROKER=localhost:9091 NATS_URL=nats://localhost:4222 ./nats-worker

# 4. Start producer
cd producer && node producer.js &

# 5. Run benchmark client with NATS group
cd benchmark-client/go-client && go run main.go -warmup 10 -duration 30 -conns 1
```

### 9. Create Dockerfile.coordinator/Dockerfile.worker updates

If using distributed mode, update the Dockerfiles to include nats.go dependency:

```dockerfile
# benchmark-client/go-client/Dockerfile.coordinator
# (existing) — just needs go.sum updated with nats.go
```

After `go get github.com/nats-io/nats.go`, the existing Dockerfiles will pick up the new dependency automatically.

### 10. Results comparison

After successful run, verify:

```
=== THROUGHPUT RESULTS ===
Group                Conns       Msgs       MB/s       msg/s
------------------------------------------------------------
WS (host/PM2)           1   1234567     123.45     10288
...
NATS bridge             1    987654      98.76      8230
NATS host               3   2345678     234.56     19547

=== LATENCY RESULTS ===
                 NATS bridge   NATS host
p50                   0.045       0.032
p75                   0.067       0.048
p90                   0.089       0.064
p95                   0.123       0.089
p99                   0.234       0.178
p99.9                 0.567       0.423
```

## Files

| File | Action |
|------|--------|
| `run-benchmark.sh` | MODIFY — add NATS worker startup |
| `run-nats-benchmark.sh` | CREATE — standalone NATS benchmark runner |
| `health-check.sh` | MODIFY — add NATS worker checks |
| `health-check-1gb.sh` | MODIFY — add NATS worker checks |
| `docker-compose.benchmark.yml` | MODIFY — add NATS coordinator config |
| `nats-worker/docker-compose.yml` | CREATE — bridge mode (may reference infra network) |
| `.gitignore` | MODIFY — add nats-worker/nats-bench binaries |

## Todo Checklist

- [ ] Update run-benchmark.sh with NATS steps
- [ ] Create run-nats-benchmark.sh
- [ ] Update health-check.sh
- [ ] Update health-check-1gb.sh
- [ ] Update docker-compose.benchmark.yml
- [ ] Ensure nats-worker docker-compose.yml uses correct network
- [ ] Update .gitignore
- [ ] End-to-end test: infra → nats-worker → producer → benchmark client
- [ ] Verify report includes all 12 groups (9 existing + 3 NATS)
- [ ] Run full benchmark with `run-benchmark.sh`
- [ ] Verify no regression to existing WS/gRPC measurements

## Success Criteria

- `run-benchmark.sh` starts NATS + nats-worker automatically
- Benchmark report shows NATS groups alongside WS/gRPC
- All 12 groups produce valid throughput + latency data
- Delta comparison vs WS baseline includes NATS
- `run-nats-benchmark.sh` produces standalone NATS results
- No existing functionality broken

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| NATS worker fails to connect to Kafka | Medium | High | Same retry logic as go-ws-server |
| Bridge networking misconfiguration | Medium | Medium | Test bridge mode carefully, use external network |
| Benchmark client too many groups (12) | Low | Low | Make NATS groups optional via flag |
| Timing issues (NATS starts before Kafka) | Low | Low | NATS independent of Kafka startup order |
