# Phase 03: Extend Benchmark Client for 3-Group Comparison

**Effort:** 1.5h  
**Prerequisite:** Phase 01, Phase 02

## Objective

Refactor `benchmark-client/client.js` from hardcoded 2-group to data-driven 3-group config. Add gRPC-host group. Extend results table to 3 columns with deltas.

## Files to Modify

### `benchmark-client/client.js`

#### Change 1: Replace hardcoded endpoint arrays with GROUPS config (lines 10-11)

**Before:**
```javascript
const WS_ENDPOINTS = ["ws://localhost:8080", "ws://localhost:8080", "ws://localhost:8080"];
const GRPC_ENDPOINTS = ["localhost:50051", "localhost:50052", "localhost:50053"];
```

**After:**
```javascript
const GROUPS = [
  {
    name: "WS (host/PM2)",
    type: "ws",
    endpoints: ["ws://localhost:8080", "ws://localhost:8080", "ws://localhost:8080"],
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

#### Change 2: Refactor histogram/count arrays (lines 81-84)

**Before:**
```javascript
const wsHistograms = [createHistogram(), createHistogram(), createHistogram()];
const grpcHistograms = [createHistogram(), createHistogram(), createHistogram()];
const wsCounts = [0, 0, 0];
const grpcCounts = [0, 0, 0];
```

**After:**
```javascript
const histograms = GROUPS.map(() =>
  [0, 1, 2].map(() => createHistogram())
);
const counts = GROUPS.map(() => [0, 0, 0]);
```

#### Change 3: Generic connectEndpoint dispatcher

Replace `connectWS(idx)` and `connectGRPC(idx)` with group-aware versions:

```javascript
function connectEndpoint(groupIdx, endpointIdx) {
  const group = GROUPS[groupIdx];
  if (group.type === "ws") return connectWS(groupIdx, endpointIdx);
  return connectGRPC(groupIdx, endpointIdx);
}

function connectWS(gi, ei) {
  return new Promise((resolve) => {
    const ws = new WebSocket(GROUPS[gi].endpoints[ei]);
    ws.on("open", () => {
      console.log(`[client] ${GROUPS[gi].name} #${ei + 1} connected`);
      resolve();
    });
    ws.on("message", (raw) => {
      if (!measuring) return;
      const now = performance.timeOrigin + performance.now();
      const msg = JSON.parse(raw.toString());
      const latencyMicros = Math.round((now - msg.timestamp) * 1000);
      if (latencyMicros > 0) histograms[gi][ei].recordValue(latencyMicros);
      counts[gi][ei]++;
    });
    ws.on("error", (err) =>
      console.error(`[client] ${GROUPS[gi].name} #${ei + 1} error: ${err.message}`)
    );
  });
}

function connectGRPC(gi, ei) {
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(
      () => reject(new Error(`${GROUPS[gi].name} #${ei + 1} connect timeout`)),
      CONNECT_TIMEOUT
    );
    let resolved = false;
    const done = () => {
      if (!resolved) { resolved = true; clearTimeout(timeout); resolve(); }
    };
    const client = new benchmarkProto.BenchmarkService(
      GROUPS[gi].endpoints[ei], creds,
      { "grpc.keepalive_time_ms": 30000, "grpc.keepalive_timeout_ms": 10000 }
    );
    const stream = client.StreamMessages({ client_id: `bench-${gi}-${ei}` });
    stream.on("data", (resp) => {
      if (!resolved) {
        console.log(`[client] ${GROUPS[gi].name} #${ei + 1} connected`);
        done();
      }
      if (!measuring) return;
      const now = performance.timeOrigin + performance.now();
      const latencyMicros = Math.round((now - Number(resp.timestamp)) * 1000);
      if (latencyMicros > 0) histograms[gi][ei].recordValue(latencyMicros);
      counts[gi][ei]++;
    });
    stream.on("error", (err) => {
      if (!resolved) { clearTimeout(timeout); reject(err); }
      else console.error(`[client] ${GROUPS[gi].name} #${ei + 1} error: ${err.message}`);
    });
  });
}
```

#### Change 4: Connection loop (line 152)

**Before:**
```javascript
await Promise.all([
  ...WS_ENDPOINTS.map((_, i) => connectWS(i)),
  ...GRPC_ENDPOINTS.map((_, i) => connectGRPC(i)),
]);
```

**After:**
```javascript
const connectPromises = [];
for (let gi = 0; gi < GROUPS.length; gi++) {
  for (let ei = 0; ei < GROUPS[gi].endpoints.length; ei++) {
    connectPromises.push(connectEndpoint(gi, ei));
  }
}
await Promise.all(connectPromises);
```

#### Change 5: New 3-group results table (replace `printTable`)

```javascript
function printTable(groupHists) {
  const percentiles = [50, 75, 90, 95, 99, 99.9];
  const labels = ["p50", "p75", "p90", "p95", "p99", "p99.9"];
  const names = GROUPS.map(g => g.name);
  const pad = (s, w) => s.padStart(w);

  const col1 = 12, col2 = 14, col3 = 14, col4 = 12, col5 = 12;
  console.log("");
  console.log(`╔══════════╦${"═".repeat(col1)}╦${"═".repeat(col2)}╦${"═".repeat(col3)}╦${"═".repeat(col4)}╦${"═".repeat(col5)}╗`);
  console.log(`║ Pctl     ║${pad(names[0], col1)}║${pad(names[1], col2)}║${pad(names[2], col3)}║${pad("bridge-WS Δ", col4)}║${pad("host-WS Δ", col5)}║`);
  console.log(`╠══════════╬${"═".repeat(col1)}╬${"═".repeat(col2)}╬${"═".repeat(col3)}╬${"═".repeat(col4)}╬${"═".repeat(col5)}╣`);

  for (let i = 0; i < percentiles.length; i++) {
    const p = percentiles[i];
    const vals = groupHists.map(h => h.getValueAtPercentile(p) / 1e6);
    const d1 = vals[1] - vals[0];
    const d2 = vals[2] - vals[0];
    console.log(
      `║ ${pad(labels[i], 8)} ║${pad(vals[0].toFixed(3), col1)}║${pad(vals[1].toFixed(3), col2)}║${pad(vals[2].toFixed(3), col3)}║${pad((d1 >= 0 ? "+" : "") + d1.toFixed(3), col4)}║${pad((d2 >= 0 ? "+" : "") + d2.toFixed(3), col5)}║`
    );
  }
  console.log(`╚══════════╩${"═".repeat(col1)}╩${"═".repeat(col2)}╩${"═".repeat(col3)}╩${"═".repeat(col4)}╩${"═".repeat(col5)}╝`);
  console.log("");
}
```

#### Change 6: Results output (lines 168-183)

Replace hardcoded WS/gRPC merged histograms with group loop:

```javascript
const groupMerged = GROUPS.map((_, gi) => mergeHistograms(histograms[gi]));

printTable(groupMerged);

console.log("Per-endpoint breakdown:");
for (let gi = 0; gi < GROUPS.length; gi++) {
  for (let ei = 0; ei < GROUPS[gi].endpoints.length; ei++) {
    console.log(
      `  ${GROUPS[gi].name} #${ei + 1}: ${counts[gi][ei]} msgs, ${printPercentile(histograms[gi][ei])}`
    );
  }
}

const totalMsgs = counts.flat().reduce((a, b) => a + b, 0);
console.log(`\nEvent loop lag: p50=${(elMonitor.percentile(50) / 1e6).toFixed(2)}ms, p99=${(elMonitor.percentile(99) / 1e6).toFixed(2)}ms, max=${(elMonitor.max / 1e6).toFixed(2)}ms`);
console.log(`Total messages: ${totalMsgs}`);
console.log(`\nPlatform: ${process.platform} (${process.platform === "darwin" ? "macOS Docker Desktop host networking uses VM, not true host" : "Linux"})`);
```

## Verification

1. Start all services (bridge + host gRPC + WS)
2. Run client: `node client.js --warmup 10 --duration 30`
3. Verify output has 3-group table with delta columns
4. Verify all 9 endpoints report message counts
5. Verify "Platform: darwin" warning appears on macOS

## Rollback

Revert client.js to hardcoded 2-group version.
