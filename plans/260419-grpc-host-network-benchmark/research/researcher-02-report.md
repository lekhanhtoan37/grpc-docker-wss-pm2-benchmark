# Research Report: gRPC Latency Benchmarking Best Practices

**Researcher:** researcher-02  
**Date:** 2026-04-19  
**Task:** Extend benchmark client to compare 3 groups — WS (host/PM2), gRPC-bridge (Docker bridge), gRPC-host (Docker host networking)

---

## 1. Current Architecture Analysis

### What Exists

The current `benchmark-client/client.js` benchmarks **2 groups** of 3 endpoints each:

| Group | Transport | Runtime | Network | Ports |
|-------|-----------|---------|---------|-------|
| WS | WebSocket | PM2 cluster (host) | Host `localhost:8080` | 8080 (3 instances via cluster) |
| gRPC-bridge | gRPC stream | Docker (bridge network) | Port-mapped `50051-50053` | 50051, 50052, 50053 |

**Key architecture details:**
- Producer writes to Kafka; servers consume Kafka and stream to benchmark client
- Client records round-trip latency using `performance.timeOrigin + performance.now()` vs server-embedded timestamps
- Uses `hdr-histogram-js` per-endpoint (3 histograms per group), merged for group-level reporting
- Runs warmup (60s default) then measurement phase (300s default)
- `run-benchmark.sh` orchestrates 3 runs with cooldown periods

### What Needs Adding

A **third group**: 3 gRPC servers running in Docker with `network_mode: host`, eliminating Docker's bridge/NAT overhead.

---

## 2. Benchmark Client Architecture for Multiple gRPC Groups

### Recommended Structure: Group-Based Configuration

Replace hardcoded endpoint arrays with a declarative group config:

```javascript
const GROUPS = [
  {
    name: "WS (host/PM2)",
    type: "ws",
    endpoints: ["ws://localhost:8080", "ws://localhost:8081", "ws://localhost:8082"],
  },
  {
    name: "gRPC bridge",
    type: "grpc",
    endpoints: ["localhost:50051", "localhost:50052", "localhost:50053"],
  },
  {
    name: "gRPC host",
    type: "grpc",
    endpoints: ["localhost:60051", "localhost:60052", "localhost:60053"],
  },
];
```

**Benefits:**
- Single connection loop that iterates over groups, creating per-endpoint histograms
- Easy to add/remove groups without modifying connection logic
- Group names carry through to output tables

### Connection Factory Pattern

Use a factory function that dispatches to WS or gRPC connect based on `type`:

```javascript
function connectEndpoint(group, idx) {
  return group.type === "ws" ? connectWS(group.endpoints[idx], idx)
                             : connectGRPC(group.endpoints[idx], idx);
}
```

The current code already does this implicitly — the refactoring is to unify it into a data-driven loop rather than hardcoding two separate connection phases.

---

## 3. hdr-histogram Best Practices for Multiple Test Groups

### Per-Endpoint + Per-Group Hierarchy

The existing pattern is correct. Maintain:

| Level | Purpose | Count |
|-------|---------|-------|
| Per-endpoint | Detect outliers, verify uniform load distribution | 9 (3 per group × 3 groups) |
| Per-group (merged) | Primary comparison metric | 3 |
| Global (all merged) | Overall system health check | 1 |

**Recommendation:** Keep the existing `mergeHistograms()` pattern. hdr-histogram supports lossless addition via `histogram.add(other)`, so per-endpoint histograms merge exactly into group histograms with zero precision loss (given identical `numberOfSignificantValueDigits`).

### Critical: Consistent Histogram Parameters

All histograms **must** use identical configuration:

```javascript
const HISTOGRAM_CONFIG = {
  lowestDiscernibleValue: 1,        // 1 microsecond
  highestTrackableValue: 60000000,  // 60 seconds
  numberOfSignificantValueDigits: 3, // ~0.1% precision
};
```

The current code already does this correctly. When adding the new group, ensure its histograms use `createHistogram()` from the same factory function.

### Memory Considerations

hdr-histogram-js uses ~180KB per histogram at these settings. With 9 per-endpoint + 3 merged = 12 histograms, total memory is ~2.2MB — negligible. No concerns here.

---

## 4. Results Presentation for 3-Group Comparison

### Recommended Table Format

Extend the existing `printTable()` to a 3-column comparison with delta columns:

```
╔══════════╦══════════════╦════════════════╦════════════════╦════════════════╦════════════════╗
║ Pctl     ║ WS host (ms) ║ gRPC bridge    ║ gRPC host (ms) ║ bridge-WS Δ    ║ host-WS Δ      ║
╠══════════╬══════════════╬════════════════╬════════════════╬════════════════╬════════════════╣
║ p50      ║      0.123   ║      0.156     ║      0.134     ║     +0.033     ║     +0.011     ║
║ ...
╚══════════╩══════════════╩════════════════╩════════════════╩════════════════╩════════════════╝
```

### Additional Metrics to Report

1. **Throughput (msgs/sec)** per group — helps identify if throughput differences explain latency differences
2. **Message count** per endpoint — verifies even distribution across Kafka consumer groups
3. **Standard deviation** — hdr-histogram doesn't directly provide this, but you can compute it from `getMean()` and `getValueAtPercentile()` spread
4. **Coefficient of variation** (stdev/mean) — normalized dispersion metric for cross-group comparison

### Per-Endpoint Breakdown

Keep the existing per-endpoint breakdown but extend it to 9 endpoints. This is essential for detecting:
- Uneven Kafka partition assignment
- A single slow container
- Network path issues to specific ports

---

## 5. Statistical Significance Considerations

### Minimum Sample Size

For latency percentile estimation with hdr-histogram at 3 significant digits (0.1% error):

| Percentile | Minimum samples for reliable estimate |
|-----------|--------------------------------------|
| p50 | ~1,000 |
| p99 | ~10,000 |
| p99.9 | ~100,000 |

The current 300s measurement duration should easily produce enough samples (producer sends ~1000 msgs/sec across all consumers). Verify by checking the total message counts in output.

### Multiple Runs for Confidence

The current `run-benchmark.sh` runs 3 iterations — this is good. For statistical rigor:

1. **Report mean and range** across runs (not just individual run results)
2. **Discard first run** if it shows systematically higher latency (JVM/Docker warmup effects)
3. **Use Mann-Whitney U test** if you need to prove statistically significant difference between groups (non-parametric, appropriate for skewed latency distributions)

### Practical Recommendation

For this benchmark (comparing networking modes), the differences will likely be large enough (bridge NAT adds measurable overhead) that formal statistical tests are unnecessary. Focus on:
- Consistent results across 3 runs
- Tight percentile spreads (p50 close to mean)
- Low event loop lag (confirms client isn't bottleneck)

---

## 6. Avoiding Client-Side Bottlenecks with 9+ Endpoints

### Current Bottleneck Risk Analysis

The existing client already connects to 6 endpoints (3 WS + 3 gRPC) in a single Node.js process. Adding 3 more gRPC endpoints brings it to **9 concurrent connections**. Key concerns:

| Risk | Current Mitigation | Assessment |
|------|-------------------|------------|
| Event loop blocking | `monitorEventLoopDelay` tracking | Good — but monitor it closely with 9 endpoints |
| JSON.parse per message | Inline parsing in WS handler | Consider `JSON.parse` optimization below |
| hdr-histogram `recordValue` | Per-endpoint histograms | Fine — `recordValue` is O(1) |
| GC pressure | Optional `--expose-gc` flag | May need periodic GC with 50% more data |

### Specific Recommendations

#### 1. Use `--max-old-space-size=512` and `--expose-gc`

```bash
node --expose-gc --max-old-space-size=512 client.js
```

With `--expose-gc`, add periodic GC during warmup (not measurement) to prevent GC pauses during measurement:

```javascript
if (typeof global.gc === 'function' && !measuring) {
  global.gc();
}
```

#### 2. Batch Histogram Recording

hdr-histogram `recordValue` is already O(1) and lock-free, so no batching needed. The library handles this well.

#### 3. Connection Staggering

Don't connect all 9 endpoints simultaneously — the burst of `connect()` syscalls and TLS/DNS resolution can cause initial event loop stalls. Stagger connections by ~200ms:

```javascript
for (const group of GROUPS) {
  for (let i = 0; i < group.endpoints.length; i++) {
    await connectEndpoint(group, i);
    await sleep(50); // 50ms stagger
  }
}
```

Note: The current code uses `Promise.all` which connects everything simultaneously. This works but staggering is more robust.

#### 4. Separate gRPC Channel Per Group

`@grpc/grpc-js` shares channels to the same target. Since bridge and host groups use different ports, they'll naturally use separate channels. But ensure you're not reusing the same `grpc.Client` instance across groups.

#### 5. Monitor File Descriptor Limits

9 connections × 2 (WS/gRPC bidirectional) = ~18 file descriptors, plus Kafka connections. Well within default limits (256 on macOS, 1024 on Linux). Not a concern.

### Scaling Beyond 9

If you later scale to 20+ endpoints, consider:
- Worker threads (`worker_threads`) — one per group
- Separate processes per group (using PM2 cluster mode for the client itself)
- For this 9-endpoint scenario, a single Node.js process is sufficient

---

## 7. PM2 and Docker Coexistence in Benchmarks

### Current Setup

| Component | Runtime | Network Mode |
|-----------|---------|-------------|
| WS servers (3) | PM2 cluster on host | Host network |
| gRPC servers (3) | Docker Compose | Bridge network + port mapping |
| Kafka/Zookeeper | Docker Compose | Bridge network |
| Producer | Node.js on host | Host → localhost:9092 |
| Benchmark client | Node.js on host | Host → all endpoints |

### Adding gRPC-Host Group

Create a **separate Docker Compose file** (`grpc-server/docker-compose.host.yml`):

```yaml
services:
  grpc-host-1:
    build: ..
    container_name: grpc-host-1
    network_mode: host
    environment:
      CONTAINER_ID: "host-1"
      KAFKA_BROKER: "localhost:9092"
      KAFKA_TOPIC: "benchmark-messages"
      PORT: "60051"
    # No ports mapping needed — host network shares host's network stack
```

**Key considerations:**

1. **Port conflicts**: Host-networked containers share the host's port space. The bridge group uses 50051-50053; the host group must use different ports (e.g., 60051-60053). The gRPC server code currently hardcodes `PORT = 50051` — it must be made configurable via `process.env.PORT`.

2. **Kafka connectivity**: Host-networked containers access Kafka at `localhost:9092` (same as PM2 processes), while bridge-networked containers use `benchmark-kafka:29092`. This is a deliberate difference being measured.

3. **Resource isolation**: Docker with `network_mode: host` still provides process isolation but no network isolation. CPU/memory limits (`cpus`, `memory`) still apply via cgroups. For fair comparison, apply identical resource constraints to both gRPC groups.

4. **PM2 interference**: PM2's cluster master and 3 WS workers run on the host. During measurement, they consume CPU. For fairest comparison:
   - Pin PM2 workers to specific CPUs: `exec_interpreter: "node", instance_var: "NODE_INSTANCE"`
   - Or accept that WS has PM2 overhead as a real-world trade-off

### Recommended Deployment Matrix

```
Host OS
├── PM2 (cluster mode)
│   ├── ws-worker-1 (port 8080)     ← WS group
│   ├── ws-worker-2 (port 8081)     ← WS group  
│   └── ws-worker-3 (port 8082)     ← WS group
├── Docker (host network)
│   ├── grpc-host-1 (port 60051)    ← gRPC host group
│   ├── grpc-host-2 (port 60052)    ← gRPC host group
│   └── grpc-host-3 (port 60053)    ← gRPC host group
├── Docker (bridge network "grpc-net")
│   ├── grpc-bridge-1 (50051→50051) ← gRPC bridge group
│   ├── grpc-bridge-2 (50052→50051) ← gRPC bridge group
│   └── grpc-bridge-3 (50053→50051) ← gRPC bridge group
└── Docker (bridge network "kafka-net")
    ├── kafka
    └── zookeeper
```

---

## 8. Implementation Recommendations Summary

### Changes Required in `client.js`

| Area | Change | Effort |
|------|--------|--------|
| Endpoint config | Replace hardcoded arrays with `GROUPS` data structure | Low |
| Connection logic | Generic `connectEndpoint()` dispatcher | Low |
| Histogram arrays | 3 groups × 3 endpoints = 9 per-endpoint histograms | Low |
| Results table | 3-column comparison with delta columns | Medium |
| Per-endpoint breakdown | Loop over groups instead of hardcoding | Low |

### Changes Required in `grpc-server/server.js`

| Area | Change | Effort |
|------|--------|--------|
| Port | Make configurable via `process.env.PORT` | Trivial |

### New Files Needed

| File | Purpose |
|------|---------|
| `grpc-server/docker-compose.host.yml` | Host-networked gRPC servers on ports 60051-60053 |

### Changes in `run-benchmark.sh`

| Area | Change |
|------|--------|
| Step 3 | Also start `docker compose -f docker-compose.host.yml up -d` |
| Step 8 | Include host-networked containers in `docker stats` |
| Health check | Add host-networked endpoints |

---

## 9. Key Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|-----------|
| Host-networked gRPC ports conflict with existing services | Benchmark fails to start | Use 60051-60053 range, verify with `netstat` before starting |
| Bridge vs host latency difference is smaller than measurement noise | Inconclusive results | Increase duration to 300s, run 5 iterations, check event loop lag |
| WS cluster mode shares single port (8080) vs gRPC using 3 ports | Unfair comparison | PM2 cluster mode already load-balances across workers — functionally equivalent |
| Docker DNS resolution adds latency to bridge group | Measures DNS not networking | Bridge containers use direct container names on shared network — minimal DNS impact |
| Host networking not available on macOS Docker Desktop | Cannot test on macOS | Run benchmark on Linux; macOS Docker uses a VM anyway so results aren't representative |

**Critical note on macOS:** Docker Desktop on macOS runs containers inside a Linux VM. `network_mode: host` on macOS Docker Desktop does **not** provide true host networking — it provides VM-host networking. The benchmark should be run on a Linux host for meaningful results. If macOS-only testing is needed, compare only the bridge gRPC group against WS, and note the platform limitation.

---

## 10. References

- hdr-histogram-js: https://github.com/HdrHistogram/HdrHistogramJS (lossless histogram addition via `add()`)
- gRPC Node.js performance: `@grpc/grpc-js` uses pure JS implementation, no native bindings — consistent cross-platform behavior
- Docker networking benchmark: Bridge networking adds ~0.05-0.2ms latency per hop vs host networking on Linux
- PM2 cluster mode: Uses Node.js `cluster` module, shares the server socket via `SO_REUSEPORT`
