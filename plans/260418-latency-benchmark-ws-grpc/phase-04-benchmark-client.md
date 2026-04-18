# Phase 4: Benchmark Client

**Prerequisites**: Phases 1-3 complete (producer, WS server, gRPC server all running)

---

## Tasks

### 4.1 Create Benchmark Client App

**Directory**: `benchmark-client/`

**package.json**:
```json
{
  "name": "benchmark-client",
  "version": "1.0.0",
  "private": true,
  "scripts": {
    "start": "node client.js",
    "start:gc": "node --expose-gc client.js"
  },
  "dependencies": {
    "ws": "^8.18.0",
    "@grpc/grpc-js": "^1.12.0",
    "@grpc/proto-loader": "^0.7.13",
    "hdr-histogram-js": "^3.0.1"
  }
}
```

### 4.2 Architecture

```
┌──────────────────────────────────────────────────────┐
│  Benchmark Client (single Node process on host)      │
│                                                      │
│  WS Connections:          gRPC Connections:           │
│  ┌──────────┐            ┌──────────────┐            │
│  │ ws://    │            │ localhost:   │            │
│  │ :8080 #1 │─► hist_ws1 │ :50051      │─► hist_g1  │
│  │ :8080 #2 │─► hist_ws2 │ :50052      │─► hist_g2  │
│  │ :8080 #3 │─► hist_ws3 │ :50053      │─► hist_g3  │
│  └──────────┘            └──────────────┘            │
│                                                      │
│  ┌──────────────────────────────────────┐            │
│  │ Aggregated histograms:               │            │
│  │  - ws_combined (merge ws1+ws2+ws3)   │            │
│  │  - grpc_combined (merge g1+g2+g3)    │            │
│  │  - event loop lag monitor            │            │
│  └──────────────────────────────────────┘            │
└──────────────────────────────────────────────────────┘
```

### 4.3 Client Implementation (`client.js`)

**Flow**:
1. Parse CLI args: `--duration 180` (seconds), `--warmup 30`
2. Connect 3 WS clients to `ws://localhost:8080`
3. Connect 3 gRPC clients to `localhost:50051/50052/50053`
4. Create 6 individual `hdr-histogram` instances
5. Create 2 aggregated histograms (WS combined, gRPC combined)
6. Wait for all connections to establish
7. **Warmup phase** (default 30s): discard measurements, let connections stabilize
8. **Measurement phase** (default 180s): record latency per message
9. Print results table
10. Clean exit

**Per-message latency calculation**:
```js
// WS
ws.on('message', (raw) => {
  const now = Date.now()
  const msg = JSON.parse(raw)
  const latencyMs = now - msg.timestamp
  histogram.recordValue(latencyMs)
})

// gRPC
stream.on('data', (resp) => {
  const now = Date.now()
  const latencyMs = now - Number(resp.timestamp)
  histogram.recordValue(latencyMs)
})
```

### 4.4 Event Loop Lag Monitor

```js
const { monitorEventLoopDelay } = require('node:perf_hooks')
const elMonitor = monitorEventLoopDelay({ resolution: 20 })
elMonitor.enable()

// Log every 5 seconds
setInterval(() => {
  console.log(`[EL] p50=${(elMonitor.percentile(50)/1e6).toFixed(2)}ms ` +
    `p99=${(elMonitor.percentile(99)/1e6).toFixed(2)}ms ` +
    `max=${(elMonitor.max/1e6).toFixed(2)}ms`)
  elMonitor.reset()
}, 5000)
```

### 4.5 Results Output Format

```
╔══════════╦══════════════╦══════════════╦════════════╗
║ Pctl     ║ WS (ms)      ║ gRPC (ms)    ║ Delta (ms) ║
╠══════════╬══════════════╬══════════════╬════════════╣
║ p50      ║        2.340 ║        3.120 ║     +0.780 ║
║ p75      ║        3.100 ║        4.250 ║     +1.150 ║
║ p90      ║        4.890 ║        6.340 ║     +1.450 ║
║ p95      ║        6.230 ║        8.100 ║     +1.870 ║
║ p99      ║       12.450 ║       18.300 ║     +5.850 ║
║ p99.9    ║       28.100 ║       42.700 ║    +14.600 ║
╚══════════╩══════════════╩══════════════╩════════════╝

Per-endpoint breakdown:
  WS #1:    12450 msgs, p50=2.3ms, p99=12.1ms
  WS #2:    12450 msgs, p50=2.4ms, p99=12.8ms
  WS #3:    12450 msgs, p50=2.3ms, p99=12.5ms
  gRPC #1:  12450 msgs, p50=3.1ms, p99=18.2ms
  gRPC #2:  12450 msgs, p50=3.2ms, p99=18.5ms
  gRPC #3:  12450 msgs, p50=3.0ms, p99=18.1ms

Event loop lag: p50=0.12ms, p99=1.45ms, max=8.20ms
Total messages: 74700 (12450 per endpoint × 6)
```

### 4.6 CLI Usage

```bash
# Default: 30s warmup, 3min measurement
node client.js

# Custom durations
node client.js --warmup 60 --duration 300

# With GC control
node --expose-gc client.js --warmup 60 --duration 300

# Output to file
node client.js 2>&1 | tee results/run-1.log
```

### 4.7 Histogram Merge

```js
const hdr = require('hdr-histogram-js')

function mergeHistograms(histograms) {
  const merged = hdr.build()
  for (const h of histograms) {
    hdr.add(merged, h)
  }
  return merged
}

// Usage
const wsCombined = mergeHistograms([hist_ws1, hist_ws2, hist_ws3])
const grpcCombined = mergeHistograms([hist_g1, hist_g2, hist_g3])
```

### 4.8 Proto File

Copy `proto/benchmark.proto` to `benchmark-client/proto/benchmark.proto` (or use relative path).

---

## Gotchas

- **Warmup is critical**: First 30s have JIT compilation, Kafka rebalancing, TCP window scaling. Discard warmup data.
- **`Date.now()` precision**: 1ms resolution. For sub-ms differences, consider `process.hrtime.bigint()` — but `Date.now()` is sufficient for p50/p99 at the expected latency range (1-50ms).
- **Single event loop**: All 6 connections + JSON parsing + histogram recording on one event loop. Monitor EL lag. If p99 EL lag > 10ms, results may be affected.
- **`console.log` during measurement**: Adds latency. Only log periodic checkpoints (every 5s), not per-message.
- **Message count verification**: Each endpoint should receive ~18000 messages at 100 msg/s for 3 minutes. If counts differ significantly, something is wrong.
- **Histogram reset**: Don't reset during measurement. Let it accumulate. Reset only between runs.

---

## Acceptance Criteria

- [ ] Client connects to 3 WS endpoints + 3 gRPC endpoints
- [ ] Per-message latency recorded in separate histograms
- [ ] Warmup phase discards data
- [ ] Results table printed with p50/p75/p90/p95/p99/p99.9
- [ ] Per-endpoint breakdown shown
- [ ] Event loop lag monitored and reported
- [ ] WS combined and gRPC combined histograms compared
- [ ] CLI args for warmup/duration configurable
