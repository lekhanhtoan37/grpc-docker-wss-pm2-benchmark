# Research: NodeJS WS/gRPC Streaming Latency Benchmark Approach

## Key Findings

- **`hdr-histogram-js`** is the best percentile library for NodeJS: accurate (<1% error on all percentiles), fast (1,769 ops/sec recording), 1.6M weekly downloads, supports WebAssembly backend. `native-hdr-histogram` (C bindings) is faster for recording but requires node-gyp. Both support `recordCorrectedValue()` for coordinated omission correction.
- **`process.hrtime.bigint()`** provides nanosecond precision, monotonic, immune to clock drift — ideal for latency measurement. `performance.now()` gives microsecond precision (floating point). `Date.now()` is millisecond-only and subject to system clock adjustments — avoid.
- **Timestamp embedding approach** works well: embed epoch-ms or hrtime in Kafka message payload, compute delta at client receive. Since benchmark runs on host machine, `Date.now()` ms precision may suffice, but `performance.now()` or `process.hrtime.bigint()` preferred.
- **gRPC server-streaming** uses `@grpc/grpc-js` (pure JS, recommended). Client calls `call.on('data', msg => ...)` to receive streamed messages. Per-message latency = `receiveTime - msg.timestamp`.
- **WebSocket** uses `ws` library. Client listens `ws.on('message', data => ...)`. Per-message latency same pattern.
- **Concurrent benchmarks**: Run all 6 connections (3 WS + 3 gRPC) in single Node process. Each connection independent, event loop handles all concurrently. Use separate `hdr-histogram` instances per endpoint.
- **NodeJS event loop lag** is the critical metric to monitor alongside latency — use `monitorEventLoopDelay()` from `node:perf_hooks`.

## Recommended Benchmark Tool Architecture

```
┌─────────────────────────────────────────────┐
│           Benchmark Runner (host)            │
│                                             │
│  ┌─────────┐  ┌─────────┐  ┌─────────┐    │
│  │ WS      │  │ WS      │  │ WS      │    │
│  │ Client  │  │ Client  │  │ Client  │    │
│  │ :8081   │  │ :8082   │  │ :8083   │    │
│  │ (PM2-1) │  │ (PM2-2) │  │ (PM2-3) │    │
│  └────┬────┘  └────┬────┘  └────┬────┘    │
│       │            │            │           │
│  ┌────┴────┐  ┌────┴────┐  ┌────┴────┐    │
│  │ gRPC    │  │ gRPC    │  │ gRPC    │    │
│  │ Client  │  │ Client  │  │ Client  │    │
│  │ :50051  │  │ :50052  │  │ :50053  │    │
│  │ (ctr-1) │  │ (ctr-2) │  │ (ctr-3) │    │
│  └────┬────┘  └────┬────┘  └────┬────┘    │
│       │            │            │           │
│  ┌────▼────────────▼────────────▼────┐     │
│  │      Results Aggregator           │     │
│  │  6x hdr-histogram instances       │     │
│  │  Percentile table output          │     │
│  │  Event loop lag monitoring        │     │
│  └───────────────────────────────────┘     │
└─────────────────────────────────────────────┘
         │
         │ (publishes test messages with embedded timestamp)
         ▼
    Kafka → Service → WS/gRPC servers
```

### Architecture Notes

- Single benchmark process manages all 6 client connections
- Each client maintains its own `hdr-histogram` instance
- Aggregator collects results after measurement window, prints percentile table
- Event loop lag monitored via `monitorEventLoopDelay()` to detect interference

## Percentile Calculation Approach

Use `hdr-histogram-js` v3.x (1.6M weekly downloads, TypeScript, WASM option):

```js
import * as hdr from 'hdr-histogram-js'

const WS_HISTOGRAM = hdr.build({ lowestDiscernibleValue: 1, highestTrackableValue: 60000000, numberOfSignificantValueDigits: 3 })
const GRPC_HISTOGRAM = hdr.build({ lowestDiscernibleValue: 1, highestTrackableValue: 60000000, numberOfSignificantValueDigits: 3 })

// Record per-message latency (microseconds)
const receiveNs = process.hrtime.bigint()
const latencyUs = Number(receiveNs - BigInt(msg.timestamp)) / 1000
WS_HISTOGRAM.recordValue(Math.round(latencyUs))

// Output percentiles
function printPercentiles(h, label) {
  console.log(`\n=== ${label} ===`)
  for (const p of [50, 75, 90, 95, 99, 99.9, 99.99]) {
    const us = h.getValueAtPercentile(p)
    console.log(`  p${p}: ${(us / 1000).toFixed(3)} ms`)
  }
  console.log(`  Summary: ${JSON.stringify(h.summary)}`)
}
```

### Why HDR Histogram

- **Accuracy**: <1% error on all percentiles (vs metrics library 4-35% error)
- **Performance**: 1,769 ops/sec for recording 10K values (fastest JS impl)
- **Coordinated omission**: `recordCorrectedValue(value, expectedInterval)` fills gaps from GC/network stalls
- **Memory**: O(1) per bucket, configurable precision (3 significant digits = default)
- **Built-in summary**: `h.summary` returns `{ p50, p75, p90, p97_5, p99, p99_9, p99_99, p99_999, max }`

### Alternative: `native-hdr-histogram`

- C bindings via N-API, fastest recording
- Requires node-gyp / prebuild
- Use if native compilation acceptable

### Alternative: Manual sorted array

```js
const latencies = []
latencies.push(latencyUs)
latencies.sort((a, b) => a - b)
const p99 = latencies[Math.floor(latencies.length * 0.99)]
```
- Simple but O(n log n), memory grows unbounded, no coordinated omission correction
- Only viable for <100K samples

## Code Snippets

### WS Benchmark Client

```js
import WebSocket from 'ws'
import * as hdr from 'hdr-histogram-js'

const WS_URLS = ['ws://localhost:8081', 'ws://localhost:8082', 'ws://localhost:8083']
const histograms = new Map()

for (const url of WS_URLS) {
  const h = hdr.build({ lowestDiscernibleValue: 1, highestTrackableValue: 60000000, numberOfSignificantValueDigits: 3 })
  histograms.set(url, h)

  const ws = new WebSocket(url)
  ws.on('open', () => console.log(`Connected to ${url}`))
  ws.on('message', (raw) => {
    const receiveNs = process.hrtime.bigint()
    const msg = JSON.parse(raw)
    const latencyUs = Number(receiveNs / 1000n) - msg.timestamp
    h.recordValue(Math.max(1, Math.round(latencyUs)))
  })
  ws.on('error', (err) => console.error(`WS ${url} error:`, err.message))
}

// After measurement window...
setTimeout(() => {
  for (const [url, h] of histograms) {
    printPercentiles(h, `WS ${url}`)
  }
}, MEASUREMENT_DURATION_MS)
```

### gRPC Benchmark Client

```js
import grpc from '@grpc/grpc-js'
import protoLoader from '@grpc/proto-loader'
import * as hdr from 'hdr-histogram-js'

const GRPC_ENDPOINTS = ['localhost:50051', 'localhost:50052', 'localhost:50053']
const packageDef = protoLoader.loadSync('./proto/benchmark.proto', { keepCase: true, longs: String, enums: String, defaults: true, oneofs: true })
const proto = grpc.loadPackageDefinition(packageDef).benchmark

const histograms = new Map()

for (const endpoint of GRPC_ENDPOINTS) {
  const h = hdr.build({ lowestDiscernibleValue: 1, highestTrackableValue: 60000000, numberOfSignificantValueDigits: 3 })
  histograms.set(endpoint, h)

  const client = new proto.BenchmarkService(endpoint, grpc.credentials.createInsecure())
  const stream = client.streamMessages({})

  stream.on('data', (msg) => {
    const receiveNs = process.hrtime.bigint()
    const latencyUs = Number(receiveNs / 1000n) - Number(msg.timestamp)
    h.recordValue(Math.max(1, Math.round(latencyUs)))
  })
  stream.on('error', (err) => console.error(`gRPC ${endpoint} error:`, err.message))
}

setTimeout(() => {
  for (const [ep, h] of histograms) {
    printPercentiles(h, `gRPC ${ep}`)
  }
}, MEASUREMENT_DURATION_MS)
```

### Timestamp Embedding (Publisher Side)

```js
// When publishing to Kafka, embed high-res timestamp
const timestamp = Date.now() // epoch ms — works if same machine
// OR for higher precision across process boundaries:
// Store performance.timeOrigin + performance.now() as epoch-ms-float
const timestamp = performance.timeOrigin + performance.now()

await producer.send({
  topic: 'benchmark-topic',
  messages: [{
    value: JSON.stringify({
      payload: 'test-data',
      timestamp: Math.round(timestamp), // epoch ms
      timestampNs: Number(process.hrtime.bigint()) // nanoseconds since epoch ( BigInt → Number loses precision above 2^53, use string for safety)
    })
  }]
})
```

### Event Loop Lag Monitor

```js
import { monitorEventLoopDelay } from 'node:perf_hooks'

const h = monitorEventLoopDelay({ resolution: 20 })
h.enable()

setInterval(() => {
  const mem = process.memoryUsage()
  console.log(JSON.stringify({
    rssMB: Math.round(mem.rss / 1024 / 1024),
    heapUsedMB: Math.round(mem.heapUsed / 1024 / 1024),
    elP50ms: (h.percentile(50) / 1e6).toFixed(2),
    elP99ms: (h.percentile(99) / 1e6).toFixed(2),
    elMaxms: (h.max / 1e6).toFixed(2),
  }))
  h.reset()
}, 2000).unref()
```

### Full Percentile Table Output

```js
function printComparisonTable(wsHist, grpcHist) {
  const percentiles = [50, 75, 90, 95, 99, 99.9, 99.99]
  console.log('\n╔══════════╦═══════════╦═══════════╦══════════╗')
  console.log('║ Pctl     ║ WS (ms)   ║ gRPC (ms) ║ Delta    ║')
  console.log('╠══════════╬═══════════╬═══════════╬══════════╣')
  for (const p of percentiles) {
    const ws = wsHist.getValueAtPercentile(p) / 1000
    const grpc = grpcHist.getValueAtPercentile(p) / 1000
    const delta = grpc - ws
    console.log(`║ p${String(p).padEnd(6)} ║ ${ws.toFixed(3).padStart(7)}   ║ ${grpc.toFixed(3).padStart(7)}   ║ ${delta >= 0 ? '+' : ''}${delta.toFixed(3).padStart(6)}  ║`)
  }
  console.log('╚══════════╩═══════════╩═══════════╩══════════╝')
}
```

## Gotchas

### Warmup

- V8 JIT warmup: first 30-120 seconds produce unreliable (often better) numbers
- **Rule**: warmup 30-60s (discard data), measure 2-10 min, repeat 3-5 runs
- Connection establishment, DNS resolution, TLS handshake inflate early measurements
- Kafka consumer group rebalancing adds initial latency spikes

### GC (Garbage Collection)

- GC pauses cause latency spikes visible at p99+
- Run with `--trace-gc` to observe GC frequency during benchmark
- `global.gc()` available with `--expose-gc` flag — call before measurement window
- HDR histogram's `recordCorrectedValue(latency, expectedIntervalMs)` corrects for coordinated omission from GC stalls
- Object pooling / buffer reuse in benchmark client reduces GC pressure

### Event Loop Interference

- Single-threaded event loop: heavy JSON.parse in benchmark client adds latency to all connections
- `JSON.parse()` on large messages blocks event loop — use streaming parser or limit payload size
- `console.log` during measurement adds latency — buffer logs, print after measurement window
- Event loop lag >50ms at p99 indicates benchmark client is interfering with results
- Monitor with `monitorEventLoopDelay()` from `node:perf_hooks`

### Clock Precision

- **Same machine**: `process.hrtime.bigint()` nanosecond precision, monotonic. Best option.
- **Cross-machine** (host → Docker): clocks not synchronized. Use `Date.now()` (ms) but accept ~1-15ms NTP skew.
- **Mitigation**: run benchmark client on same host as WS/gRPC servers, or use NTP sync
- `performance.now()` has microsecond precision but may have clock drift
- `Date.now()` has 1ms resolution on most systems, can jump on NTP adjustment
- **Recommendation**: embed `Date.now()` epoch-ms in Kafka message (cross-process portable), compute delta with `Date.now()` at receive. For sub-ms precision on same host, use `process.hrtime.bigint()`

### Connection Backpressure

- WS/gRPC servers under load may buffer messages → observed latency includes queue wait
- This is intentional (measuring real end-to-end latency), but document it
- gRPC flow control: `call.pause()` / `call.resume()` for backpressure; don't pause during benchmark

### Statistical Rigor

- Run 3-5 repetitions, report median + min/max
- Don't compare single runs
- Significant digits: 3 (hdr-histogram default) = ~0.1% quantization error
- Sample size: need >1,000 messages minimum for stable p99, >10,000 for p99.9

## Recommendations

1. **Use `hdr-histogram-js`** for percentile computation. It's accurate, fast, well-maintained, supports coordinated omission correction.

2. **Use `process.hrtime.bigint()`** for same-host timing. Use `Date.now()` epoch-ms for cross-process (Kafka → host) timestamping.

3. **Single benchmark process** managing all 6 connections (3 WS + 3 gRPC) is fine — Node event loop handles concurrent I/O well. Monitor event loop lag to ensure client isn't bottleneck.

4. **Warmup 60s, measure 3-5 min, repeat 3+ times.** Discard warmup data. Report median across runs.

5. **Run with `--expose-gc`**, call `global.gc()` before measurement window. Track GC with `--trace-gc` in a separate diagnostic run.

6. **Timestamp format**: embed `Date.now()` (epoch ms integer) in Kafka message payload. At client receive, compute `Date.now() - msg.timestamp`. For sub-ms precision, embed `performance.timeOrigin + performance.now()` as float.

7. **gRPC library**: use `@grpc/grpc-js` (pure JS, maintained by grpc-node team). Avoid legacy `grpc` native package.

8. **Output format**: percentile comparison table (p50/p75/p90/p95/p99/p99.9) with WS vs gRPC columns, delta, event loop lag, GC stats, memory RSS.

9. **Dependencies**: `ws`, `@grpc/grpc-js`, `@grpc/proto-loader`, `hdr-histogram-js`, `kafkajs` (or existing Kafka client). No native deps needed unless using `native-hdr-histogram`.

10. **PM2 cluster vs gRPC container comparison**: run both simultaneously to eliminate system-level variance. Same Kafka topic, same messages, same time window. Compare per-percentile.
