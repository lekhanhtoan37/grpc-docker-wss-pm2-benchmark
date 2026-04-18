# Phase 5: Integration Test + Results Collection

**Prerequisites**: Phases 1-4 all complete and individually verified

---

## Tasks

### 5.1 Startup Sequence

```bash
# Step 1: Ensure Kafka running
kafka-topics.sh --describe --topic benchmark-messages --bootstrap-server localhost:9092

# Step 2: Start gRPC servers (Docker)
cd grpc-server && docker compose up -d --build
# Wait ~10s for containers + Kafka connection
docker compose logs --tail=5  # verify "gRPC server on 0.0.0.0:50051"

# Step 3: Start WS servers (PM2)
cd ws-server && pm2 start ecosystem.config.js
# Wait ~5s for workers + Kafka connection
pm2 logs ws-benchmark --lines 5  # verify "Worker ready"

# Step 4: Start benchmark client (triggers warmup)
cd benchmark-client && node client.js --warmup 30 --duration 180

# Step 5: Start producer (in separate terminal, during warmup)
cd producer && node producer.js

# Wait for benchmark to complete (~210 seconds total)
```

**Timing**: Warmup + measurement = 30s + 180s = 210s. Producer should start during warmup so messages are flowing before measurement begins.

### 5.2 Health Check Script

Quick script to verify all components before running benchmark:

```bash
#!/bin/bash
echo "=== Health Check ==="

# Kafka
kafka-topics.sh --describe --topic benchmark-messages --bootstrap-server localhost:9092 &>/dev/null && echo "✓ Kafka topic" || echo "✗ Kafka topic"

# gRPC containers
for port in 50051 50052 50053; do
  nc -z localhost $port && echo "✓ gRPC :$port" || echo "✗ gRPC :$port"
done

# PM2 WS
pm2 describe ws-benchmark &>/dev/null && echo "✓ PM2 WS (${pm2 describe ws-benchmark | grep 'instances'})" || echo "✗ PM2 WS"

# WS connectivity
node -e "const ws=new(require('ws')('ws://localhost:8080'));ws.on('open',()=>{console.log('✓ WS :8080');process.exit(0)});setTimeout(()=>{console.log('✗ WS :8080');process.exit(1)},3000)"
```

### 5.3 Benchmark Execution Plan

**Run 3 repetitions** for statistical rigor:

| Run | Warmup | Duration | Notes |
|-----|--------|----------|-------|
| 1   | 30s    | 180s     | First run, expect higher variance |
| 2   | 30s    | 180s     | Stable run |
| 3   | 30s    | 180s     | Confirm reproducibility |

**Between runs**:
```bash
# Reset histograms (restart client)
# No need to restart servers — keep them warm

# Optional: force GC
cd benchmark-client && node --expose-gc client.js --warmup 30 --duration 180
```

### 5.4 Results Collection

**Output directory**: `results/`

```
results/
├── run-1.log
├── run-2.log
├── run-3.log
└── summary.md    (manually written after analysis)
```

**Collect per run**:
1. Full console output (redirect to file)
2. PM2 metrics: `pm2 show ws-benchmark`
3. Docker stats: `docker stats --no-stream grpc-server-1 grpc-server-2 grpc-server-3`
4. System info: `uname -a`, `node --version`, `docker --version`, `pm2 --version`

### 5.5 Expected Results Format

**Per-run summary** (extracted from output):

```
Run 1 (2026-04-18):
  WS combined:    p50=Xms  p99=Yms  (N msgs)
  gRPC combined:  p50=Xms  p99=Yms  (N msgs)
  Delta p50:      +Zms     Delta p99: +Wms
  EL lag:         p50=Ams  p99=Bms
  Docker overhead estimate: ~0.1-0.5ms (bridge NAT)

Run 2: ...
Run 3: ...

Overall (median of 3 runs):
  WS p50:     Xms ± Ems
  WS p99:     Yms ± Ems
  gRPC p50:   Xms ± Ems
  gRPC p99:   Yms ± Ems
  Delta p50:  +Zms (gRPC slower by N%)
  Delta p99:  +Wms (gRPC slower by M%)
```

### 5.6 Shutdown Sequence

```bash
# Stop producer (Ctrl+C)

# Stop benchmark client (auto-exits after duration)

# Stop WS servers
cd ws-server && pm2 stop ws-benchmark && pm2 delete ws-benchmark

# Stop gRPC containers
cd grpc-server && docker compose down

# Verify clean state
pm2 list   # empty
docker ps  # no grpc containers
```

### 5.7 Troubleshooting

| Issue | Diagnosis | Fix |
|-------|-----------|-----|
| No messages received | Check producer running, Kafka topic exists | Start producer, verify topic |
| WS receives but gRPC doesn't | Check `docker compose logs` | Rebuild containers, verify `host.docker.internal` |
| gRPC receives but WS doesn't | Check `pm2 logs ws-benchmark` | Verify consumer group uniqueness, restart PM2 |
| Very high latency (>100ms) | Check event loop lag, GC pauses | Run with `--expose-gc`, check system load |
| Uneven message counts | Partition skew or consumer rebalancing | Ensure 1 partition, unique consumer groups |
| Connection refused | Server not started or wrong port | Check port mappings, verify services running |
| Docker networking error | `host.docker.internal` not resolving | Add `extra_hosts` to compose, or use `host-gateway` |

---

## Acceptance Criteria

- [ ] All 6 endpoints receive messages (3 WS + 3 gRPC)
- [ ] Each endpoint receives ~same number of messages (±5%)
- [ ] 3 benchmark runs completed successfully
- [ ] Results logged to `results/` directory
- [ ] Percentile comparison table generated for each run
- [ ] Event loop lag < 5ms at p99 during measurement
- [ ] Clean shutdown of all components after benchmark
