# Phase 05: Integration Test + macOS Documentation

**Effort:** 45m  
**Prerequisite:** Phase 01-04

## Objective

End-to-end integration test. Document macOS limitation. Verify all 3 groups produce valid results.

## Tasks

### 1. Full Integration Test

Run complete benchmark and validate results:

```bash
# Ensure clean state
docker compose -f infra/docker-compose.yml down -v
cd grpc-server && docker compose down && docker compose -f docker-compose.host.yml down
pm2 delete ws-benchmark 2>/dev/null || true

# Full run
WARMUP=30 DURATION=60 RUNS=2 bash run-benchmark.sh
```

**Validation checklist:**
- [ ] 9 endpoints connected (3 WS + 3 gRPC-bridge + 3 gRPC-host)
- [ ] Each group receives messages (non-zero counts)
- [ ] Results table shows 3 groups + 2 delta columns
- [ ] Event loop lag < 50ms (no client bottleneck)
- [ ] No errors in logs
- [ ] System info includes 6 containers in docker stats

### 2. macOS-Specific Testing

On macOS, verify:

```bash
# Check Docker Desktop host networking enabled
docker info 2>/dev/null | grep -i "host" || echo "WARNING: host networking may not be enabled"

# Test gRPC-host connectivity
nc -z localhost 60051 && echo "host gRPC reachable" || echo "host gRPC NOT reachable"
```

If host networking doesn't work on macOS:
- Check Docker Desktop → Settings → Resources → Network → Enable host networking
- Try binding to `127.0.0.1` instead of `0.0.0.0` by setting `GRPC_HOST=127.0.0.1` in compose

### 3. Update README.md

Add section to README documenting:

```markdown
## Network Mode Comparison

The benchmark compares three deployment modes:

| Mode | Runtime | Network | Purpose |
|------|---------|---------|---------|
| WS (host/PM2) | PM2 cluster | Host | Baseline - no Docker overhead |
| gRPC bridge | Docker | Bridge + port mapping | Standard Docker deployment |
| gRPC host | Docker | Host (`network_mode: host`) | Zero network overhead |

### macOS Limitation

On macOS Docker Desktop, `network_mode: host` shares the Linux VM network,
not the macOS host network. The VM boundary (~0.3-1ms overhead) masks most
of the host networking benefit. For production-relevant results, run on Linux.
```

### 4. Verify Fairness of Comparison

Check that all 3 groups have comparable conditions:
- Same gRPC server code (only port/env differs)
- Same Kafka topic and consumer group pattern
- Same message rate from producer
- Same measurement duration
- Same histogram configuration

## Expected Results

### On Linux
- gRPC-host should have lower latency than gRPC-bridge (by ~0.02-0.05ms at p50)
- WS baseline should be close to gRPC-host (both on host network)
- gRPC-bridge adds measurable NAT overhead at p95/p99

### On macOS
- gRPC-host ≈ gRPC-bridge (VM boundary dominates)
- WS may appear faster due to no Docker overhead at all
- Delta between gRPC-host and gRPC-bridge will be small/inconclusive

## Rollback

- Remove README section
- No code changes in this phase (documentation only)
