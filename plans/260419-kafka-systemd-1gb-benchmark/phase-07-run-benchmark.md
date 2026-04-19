# Phase 07: Run Benchmark + Collect Results

**Goal:** Execute the full benchmark, collect results, analyze throughput delta between Docker bridge and host network consumers.

**Effort:** 2-3h (mostly wait time)

---

## Task 7.1: Pre-flight checks

- [ ] **Step 1: Verify hardware meets requirements**

```bash
# Check RAM (need 16GB+)
free -h

# Check disk (need NVMe for 1GB/s)
lsblk -d -o name,rota,type | grep disk
# rota=0 means SSD/NVMe

# Check disk speed
sudo hdparm -Tt /dev/nvme0n1 2>/dev/null || echo "Not NVMe or hdparm not available"
```

- [ ] **Step 2: Tune OS for benchmark**

```bash
# Increase file descriptor limit
ulimit -n 100000

# Increase conntrack (if using bridge)
sudo sysctl -w net.netfilter.nf_conntrack_max=524288

# Increase network buffers
sudo sysctl -w net.core.rmem_max=16777216
sudo sysctl -w net.core.wmem_max=16777216
sudo sysctl -w net.ipv4.tcp_rmem="4096 87380 16777216"
sudo sysctl -w net.ipv4.tcp_wmem="4096 65536 16777216"
```

- [ ] **Step 3: Verify Kafka is producing to all 12 partitions**

```bash
/opt/kafka/bin/kafka-run-class.sh kafka.tools.GetOffsetShell \
  --broker-list localhost:9092 \
  --topic benchmark-messages
```

Expected: 12 lines, one per partition

---

## Task 7.2: Short validation run

- [ ] **Step 1: Run 30-second validation at 100 MB/s**

```bash
TARGET_MBPS=100 WARMUP=10 DURATION=30 RUNS=1 bash run-benchmark-1gb.sh
```

Expected: All 3 groups show throughput ~100 MB/s (limited by producer). Latency table shows reasonable values.

- [ ] **Step 2: Check results**

```bash
cat results/1gb-run-1-*.log
```

Verify:
- All 9 endpoints connected
- Throughput reported for each group
- Latency percentiles reasonable (<10ms p99)

---

## Task 7.3: Full benchmark at 1GB/s target

- [ ] **Step 1: Run 3 × 120s benchmark runs**

```bash
TARGET_MBPS=1000 WARMUP=30 DURATION=120 RUNS=3 bash run-benchmark-1gb.sh 2>&1 | tee results/full-benchmark-$(date +%Y%m%d-%H%M%S).log
```

Expected runtime: ~8 minutes per run (30s warmup + 120s measure + 15s cooldown) × 3 = ~24 min

- [ ] **Step 2: Monitor during run**

In a separate terminal:
```bash
# Kafka throughput
watch -n 2 '/opt/kafka/bin/kafka-run-class.sh kafka.tools.GetOffsetShell --broker-list localhost:9092 --topic benchmark-messages'

# Docker CPU/mem
watch -n 2 'docker stats --no-stream'

# PM2 status
watch -n 2 'pm2 status'

# System resources
watch -n 2 'free -h && echo && uptime'
```

- [ ] **Step 3: Collect results**

```bash
ls -la results/1gb-run-*.log
cat results/system-info-*.log
```

---

## Task 7.4: Analyze results

- [ ] **Step 1: Extract throughput comparison from logs**

Expected result format:
```
Group                  Msgs      MB/s       msg/s
------------------------------------------------------------
WS (host/PM2)        360000    200.50      300000
gRPC bridge          310000    172.22      258333
gRPC host            355000    197.78      295833
------------------------------------------------------------
gRPC bridge        -14.2% vs WS throughput
gRPC host          -1.4% vs WS throughput
```

**Expected findings based on research:**
- **gRPC bridge vs WS-host**: ~12-17% lower throughput (Docker bridge overhead)
- **gRPC host vs WS-host**: ~0-5% difference (both use host network)
- **Latency**: bridge adds ~50-200μs per message at high throughput

- [ ] **Step 2: Create results summary**

```bash
{
  echo "# Benchmark Results Summary"
  echo "Date: $(date)"
  echo "Producer target: ${TARGET_MBPS:-1000} MB/s"
  echo ""
  echo "## Per-Run Results"
  for f in results/1gb-run-*.log; do
    echo ""
    echo "### $(basename $f)"
    grep -A 10 "THROUGHPUT RESULTS" "$f" || true
    grep -A 15 "LATENCY RESULTS" "$f" || true
  done
} > results/SUMMARY.md
```

- [ ] **Step 3: Commit results (if desired)**

```bash
git add results/
git commit -m "bench: 1GB/s throughput benchmark results"
```

---

## Task 7.5: Rollback (if needed)

If results are invalid or benchmark fails:

```bash
# Stop all services
bash infra/teardown-kafka.sh
cd grpc-server && docker compose down && docker compose -f docker-compose.host.yml down
pm2 delete ws-benchmark

# Revert to Docker Kafka
cd infra && docker compose up -d

# Use original scripts
bash run-benchmark.sh
```
