# Research Report: uWebSockets.js Docker & PM2 Compatibility

**Date:** 2026-04-22
**Scope:** Deployment feasibility of uWebSockets.js in PM2, Docker bridge, and Docker host-network modes

---

## Executive Summary

uWebSockets.js (uWS) ships **prebuilt native binaries** for Linux x64, Linux ARM64, and macOS — no build tools needed at `npm install` time. It works in Docker with `node:20-bookworm-slim` (Debian-based) with zero extra dependencies. PM2 cluster mode works on Linux via `SO_REUSEPORT` but is **not supported on macOS**. The official recommendation for multi-core is **Worker Threads** with an acceptor pattern, not Node.js `cluster` module. Performance benchmarks show uWS at **2-10x throughput** vs `ws` with **5x lower memory** per connection. uWS has `maxPayloadLength` (equivalent to `ws`'s `maxPayload`) and built-in backpressure management.

---

## 1. Current Setup Analysis

### ws-server (existing)

- **Library:** `ws@^8.18.0` + `kafkajs@^2.2.4`
- **PM2 config:** 3 cluster instances, `--max-old-space-size=16384`, port 8090
- **Key pattern:** `maxPayload: 0` (unlimited), Kafka consumer per worker, `eachBatch` broadcast to all WS clients
- **No Dockerfile** for ws-server exists currently; only grpc-server has a Dockerfile using `node:20-bookworm-slim`
- **docker-compose.yml** defines Kafka/Zookeeper infra only

### grpc-server Dockerfile (reference)

```dockerfile
FROM node:20-bookworm-slim
WORKDIR /app
COPY package*.json ./
RUN npm install --omit=dev
COPY server.js .
COPY proto/ ./proto/
CMD ["node", "--max-old-space-size=16384", "server.js"]
```

---

## 2. uWebSockets.js npm Install Requirements

### Prebuilt binaries — no compilation needed

uWS is **not on the npm registry**. Install via GitHub:

```bash
npm install uNetworking/uWebSockets.js#v20.64.0
```

**Key facts:**
- Ships prebuilt `.node` binaries for Linux x64 (glibc), Linux ARM64, and macOS
- Downloads all platform binaries in one package (~few MB each)
- **No Python, no C++ compiler, no `node-gyp`, no `make` required**
- Works identically in Docker as on bare metal — maintainer confirmed: *"Docker is completely irrelevant to uWS. There is no difference, whether you install on Ubuntu in Docker or Ubuntu elsewhere."*

### package.json dependency format

```json
{
  "dependencies": {
    "uWebSockets.js": "github:uNetworking/uWebSockets.js#v20.64.0"
  }
}
```

> **Note:** `npm install` from GitHub requires git in the build context. In Docker, either:
> 1. Run `npm install` on the host and copy `node_modules` in, OR
> 2. Ensure `git` is available during `npm install` (it's included in `node:20-bookworm-slim`)

---

## 3. PM2 (Bare Metal) Compatibility

### Verdict: Works on Linux with caveats

| Concern | Status |
|---------|--------|
| Native addon under PM2 | ✅ Works — PM2 spawns Node.js processes; uWS loads as a V8 addon |
| Cluster mode (`exec_mode: "cluster"`) | ⚠️ **Linux only** — requires `SO_REUSEPORT` (kernel 3.9+) |
| Cluster mode on macOS | ❌ **Broken** — macOS `SO_REUSEPORT` only supports UDP, not TCP |
| `NODE_APP_INSTANCE` env var | ✅ Available — can use for Kafka group ID differentiation |
| `process.send("ready")` | ✅ Works with PM2 `wait_ready: true` |

### Critical: PM2 cluster mode vs Worker Threads

PM2 cluster mode uses Node.js `cluster` module (fork-based). With uWS:
- **On Linux:** Multiple workers CAN bind the same port via `SO_REUSEPORT` — the kernel load-balances
- **On macOS:** Only the first worker succeeds; others fail to bind
- **Official uWS recommendation:** Use **Worker Threads** with acceptor pattern, not `cluster`

### Migration path from current PM2 setup

Current `ecosystem.config.js` uses `instances: 3, exec_mode: "cluster"`. Two options:

**Option A: Keep PM2 cluster (Linux production only)**
```javascript
// ecosystem.config.js — works on Linux deployment target
module.exports = {
  apps: [{
    name: "uws-benchmark",
    script: "./server.js",
    instances: 3,
    exec_mode: "cluster",
    // ... rest same as current
  }]
};
```

**Option B: PM2 fork + Worker Threads (cross-platform, recommended by uWS)**
```javascript
module.exports = {
  apps: [{
    name: "uws-benchmark",
    script: "./server.js",
    instances: 1,          // single PM2 process
    exec_mode: "fork",     // fork mode, not cluster
    node_args: "--max-old-space-size=16384",
  }]
};
// server.js internally spawns Worker Threads
```

> **For this project:** Since the target is Linux (Docker/bare metal), **Option A is simpler** and matches the existing pattern. The current 3-instance Kafka consumer pattern (each with unique group ID) maps directly.

---

## 4. Docker Bridge Network

### Verdict: Fully compatible, no extra dependencies

```dockerfile
FROM node:20-bookworm-slim

WORKDIR /app

# git is included in node:20-bookworm-slim for npm install from GitHub
COPY package*.json ./
RUN npm install --omit=dev

COPY server.js .

ENV PORT=8090
ENV KAFKA_BROKER=192.168.0.9:9091

CMD ["node", "--max-old-space-size=16384", "server.js"]
```

**No additional build steps needed.** The uWS prebuilt Linux x64 binary matches `node:20-bookworm-slim` (Debian/glibc).

### Docker Compose (bridge mode)

```yaml
services:
  ws-server:
    build: ../ws-server
    ports:
      - "8090:8090"
    environment:
      - KAFKA_BROKER=benchmark-kafka:29091
      - KAFKA_TOPIC=benchmark-messages
      - PORT=8090
    depends_on:
      - kafka
    networks:
      - benchmark-net
```

### Does `node:20-bookworm-slim` work?

**Yes.** Confirmed by:
- uWS maintainer: install is just a file copy, no compilation
- The glibc-based prebuilt binary matches Debian bookworm
- `node:20-alpine` also works (uWS ships musl-compatible binaries for Alpine)
- No issues reported with any Debian-based node images

---

## 5. Docker Host Network

### Verdict: Same as bridge, just `network_mode: host`

```yaml
services:
  ws-server:
    build: ../ws-server
    network_mode: host
    environment:
      - KAFKA_BROKER=192.168.0.9:9091
      - KAFKA_TOPIC=benchmark-messages
      - PORT=8090
```

**No uWS-specific differences** between bridge and host network modes. Host network is preferred for benchmark scenarios:
- Eliminates NAT overhead
- Direct access to host network interface
- Lower latency, higher throughput

### Potential advantage for uWS

uWS already operates at near-kernel efficiency. With `network_mode: host`:
- No Docker bridge NAT layer = maximum throughput
- uWS can fully utilize kernel's `SO_REUSEPORT` for multi-process binding
- Best configuration for benchmarking raw WS performance

---

## 6. Performance: uWS vs ws

### Published benchmarks (2026 sources)

| Metric | ws | uWebSockets.js | Ratio |
|--------|-----|----------------|-------|
| Throughput (msg/s) | ~400K | ~2M+ | **5x** |
| Connections/sec | ~10K | ~100K | **10x** |
| Memory per 1K clients | ~300MB | ~60MB | **5x less** |
| Latency p99 | ~5ms | ~1ms | **5x** |
| Memory per 10K clients | ~200MB | ~40MB | **5x less** |

### For this project's broadcast scenario

Current ws-server broadcasts Kafka messages to all connected WS clients:
- `ws.send()` is called per-client per-message — O(clients × messages)
- uWS has **built-in pub/sub** via `ws.subscribe()` / `app.publish()` — zero-copy broadcast at C++ level
- For 10K clients with high-throughput Kafka feeds, uWS pub/sub could be **10-50x more efficient** than per-client `ws.send()` loops

### Key uWS advantage: backpressure-aware broadcasting

```javascript
// ws (current) — manual per-client send, no backpressure awareness
for (const ws of clients) {
  ws.send(data);  // can silently fail or buffer infinitely
}

// uWS — built-in pub/sub with automatic backpressure handling
app.publish('topic', data);  // skips slow receivers, respects maxBackpressure
```

---

## 7. maxPayload Equivalent

### Direct mapping

| ws option | uWS equivalent | Default |
|-----------|----------------|---------|
| `maxPayload` | `maxPayloadLength` | ws: 100MB, uWS: 16KB |
| (none) | `maxBackpressure` | uWS: 64KB |
| (none) | `closeOnBackpressureLimit` | uWS: false |

### Current ws-server config

```javascript
const wss = new WebSocketServer({ server, maxPayload: 0 }); // unlimited
```

### uWS equivalent

```javascript
uWS.App().ws('/*', {
  maxPayloadLength: 0,          // 0 = unlimited (same as ws maxPayload: 0)
  maxBackpressure: 1024 * 1024, // 1MB backpressure limit
  // Note: maxPayloadLength: 0 means unlimited in uWS
}).listen(8090, (token) => { ... });
```

**⚠️ Important difference:** uWS default `maxPayloadLength` is **16KB** (vs ws's effectively unlimited). Must explicitly set for large messages.

---

## 8. API Migration Considerations

### Key differences when porting from ws → uWS

| Aspect | ws | uWS |
|--------|-----|-----|
| Server creation | `new WebSocketServer({ server })` wraps http.Server | `uWS.App().ws()` — creates its own server |
| Message format | `Buffer` / `String` | `ArrayBuffer` only — must use `Buffer.from(message)` or `TextDecoder` |
| Send method | `ws.send(data)` | `ws.send(data)` — but data must be `ArrayBuffer`/`Uint8Array`/`string` |
| Connection state | `ws.readyState === 1` | No readyState — ws is valid from `open` to `close` |
| Error handling | `ws.on('error', ...)` | No error handler — errors surface as `close` |
| HTTP server | Separate `http.createServer()` | Integrated — `app.get()`, `app.post()` etc. |

### No http.Server wrapping

uWS does **not** wrap an existing `http.Server`. This is the biggest architectural change:

```javascript
// ws pattern (current) — uWS CANNOT do this
const server = http.createServer();
const wss = new WebSocketServer({ server });
server.listen(8090);

// uWS pattern — server is created internally
const app = uWS.App().ws('/*', { /* handlers */ }).listen(8090, (token) => {});
```

This means the `server` variable and `http.createServer()` cannot be reused. Not a problem for this project since the ws-server only handles WebSocket traffic.

---

## 9. Risk Assessment

| Risk | Severity | Mitigation |
|------|----------|------------|
| PM2 cluster fails on macOS dev | Low | Use `fork` mode in dev, `cluster` in prod (Linux) |
| npm install from GitHub fails in Docker | Medium | Pre-install on host & copy `node_modules`, or ensure git in image |
| ArrayBuffer API differences | Medium | Use `Buffer.from()` wrapper or `TextDecoder` |
| Default 16KB maxPayloadLength | High | Explicitly set `maxPayloadLength` to match current `maxPayload: 0` |
| Built-in pub/sub replaces Kafka broadcast pattern | Low | Can keep Kafka pattern; pub/sub is optional optimization |
| uWS not on npm registry | Low | GitHub install is stable; used by Bun in production |

---

## 10. Recommendations

1. **Docker base image:** `node:20-bookworm-slim` — works with zero modifications
2. **PM2 mode:** Keep `cluster` mode with `instances: 3` for Linux deployment (matches existing pattern)
3. **Host network mode:** Preferred for benchmarking — eliminates Docker NAT overhead
4. **Must-set options:** `maxPayloadLength: 0` (to match current unlimited config), `maxBackpressure` for production safety
5. **Optional optimization:** Use uWS built-in `publish()`/`subscribe()` instead of per-client send loop for broadcast scenarios
6. **npm install strategy:** Run `npm install` during Docker build (git included in bookworm-slim) OR install on host and copy

---

## References

- [uWebSockets.js GitHub](https://github.com/uNetworking/uWebSockets.js) — v20.64.0 (latest as of 2026-04)
- [WebSocketBehavior API docs](https://unetworking.github.io/uWebSockets.js/generated/interfaces/WebSocketBehavior.html) — `maxPayloadLength`, `maxBackpressure` etc.
- [WorkerThreads.js example](https://github.com/uNetworking/uWebSockets.js/blob/master/examples/WorkerThreads.js) — official multi-core pattern
- [Discussion #304 — Clustering](https://github.com/uNetworking/uWebSockets.js/discussions/304) — SO_REUSEPORT Linux-only behavior
- [Issue #787 — Cluster performance](https://github.com/uNetworking/uWebSockets.js/issues/787) — maintainer guidance on scaling
- [Discussion #984 — Docker install](https://github.com/uNetworking/uWebSockets.js/discussions/984) — Docker is irrelevant to uWS
- [PkgPulse benchmark comparison](https://www.pkgpulse.com/blog/socketio-vs-ws-vs-uwebsockets-websocket-servers-nodejs-2026) — ws vs uWS performance data
