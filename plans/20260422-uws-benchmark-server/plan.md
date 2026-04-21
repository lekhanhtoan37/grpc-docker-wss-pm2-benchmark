---
title: "Add uWebSockets.js Benchmark Server"
description: "Add a Node.js WebSocket server using uWebSockets.js to the gRPC/WS benchmark project, deployable in PM2, Docker bridge, and Docker host network modes, enabling direct uWS vs ws vs gRPC performance comparison."
status: completed
priority: P2
effort: 2-3 days
branch: main
tags: [benchmark, websocket, uwebsockets, docker, pm2]
created: 2026-04-22
---

# Plan: Add uWebSockets.js Benchmark Server

## Overview

Add a `uws-server/` directory mirroring the existing `ws-server/` pattern, but using `uWebSockets.js` (uWS) instead of the `ws` library. The server consumes from Kafka and broadcasts to WebSocket clients. It runs in 3 deployment modes (PM2 cluster, Docker bridge, Docker host) with unique ports per mode. The Go benchmark client and orchestration script are updated to include uWS endpoints alongside existing ws and gRPC groups.

**Goal:** Measure uWS throughput and latency against ws (pure JS) and gRPC to quantify the performance benefit of a C++ native WebSocket implementation.

## Port Allocation

| Mode | Existing (ws) | New (uWS) |
|------|--------------|-----------|
| PM2 cluster | 8090 | **8091** |
| Docker bridge | — | **50061–50063** |
| Docker host | — | **60061–60063** |

## Kafka Consumer Group Isolation

Each server instance must have its **own** Kafka consumer group so every instance receives ALL messages (fan-out):

- **PM2:** `uws-benchmark-worker-${NODE_APP_INSTANCE}` (3 groups)
- **Docker bridge:** `uws-bridge-${CONTAINER_ID}` (3 groups)
- **Docker host:** `uws-host-${CONTAINER_ID}` (3 groups)

---

## Approach A: Minimal Diff (Clone ws-server Pattern)

Clone the existing `ws-server/server.js` structure and swap `ws` for uWS with minimal changes. Keep JSON.parse validation and manual broadcast loop.

### Server logic (`uws-server/server.js`)

```
1. Replace http.createServer + WebSocketServer → uWS.App().ws()
2. Replace `clients` Set + manual iteration → `clients` Set + manual iteration (same as ws)
3. Keep JSON.parse(msg.value.toString()) before ws.send()
4. Keep yield every 2k messages via setImmediate
5. Keep maxPayload: 0 equivalent → maxPayloadLength: 0
6. Messages are ArrayBuffer — use Buffer.from() on receive, send as Buffer/string
```

### Key characteristics

- **Fair comparison:** Identical processing path to ws-server (JSON.parse, yield loop, manual broadcast)
- **Measures:** Only the WebSocket library difference (C++ vs pure JS)
- **Risk:** Low — well-understood pattern, easy to verify correctness

### Trade-offs

| Aspect | Rating | Notes |
|--------|--------|-------|
| Fairness | ★★★★★ | Identical logic to ws-server; only lib differs |
| Performance | ★★★☆☆ | Manual JS broadcast negates much of uWS advantage |
| Complexity | ★★★★★ | Minimal code, easy to audit |
| Risk | ★★★★★ | Low — close to existing working code |

---

## Approach B: uWS-Optimized (Built-in Pub/Sub)

Use uWS idiomatic features: built-in `app.publish()` for C++-level broadcast, skip JSON.parse on the server (clients parse for latency measurement anyway).

### Server logic (`uws-server/server.js`)

```
1. uWS.App().ws() with ws.subscribe('kafka-broadcast') in open handler
2. Kafka eachBatch → app.publish('kafka-broadcast', msg.value, true) per message
3. No JSON.parse on server — forward raw Kafka bytes (isBinary=true)
4. No manual client tracking — pub/sub handles it
5. No yield loop — app.publish() is O(1) C++ call, non-blocking
6. maxPayloadLength: 0, maxBackpressure: 1MB
```

### Key characteristics

- **Maximum performance:** C++ broadcast, zero JS overhead per client
- **Measures:** Full uWS capability including pub/sub
- **Risk:** Medium — different processing path may confuse comparison

### Trade-offs

| Aspect | Rating | Notes |
|--------|--------|-------|
| Fairness | ★★★☆☆ | Skips JSON.parse, no yield loop — different processing |
| Performance | ★★★★★ | Full uWS speed: C++ pub/sub, no per-client JS loop |
| Complexity | ★★★★☆ | Simpler code but requires explaining differences |
| Risk | ★★★☆☆ | Must document differences to avoid misleading benchmarks |

---

## Comparison Table

| Criterion | Approach A (Minimal Diff) | Approach B (uWS Pub/Sub) |
|-----------|--------------------------|--------------------------|
| **Fairness** | Identical logic to ws-server; isolates lib difference | Different processing; measures uWS ceiling |
| **Throughput** | ~2-3x vs ws (faster send, same loop) | ~10-50x vs ws (C++ broadcast) |
| **Latency** | ~2x lower than ws | ~5x lower than ws |
| **Code complexity** | ~90 lines (mirrors ws-server) | ~60 lines (pub/sub is simpler) |
| **Correctness risk** | Low — proven pattern | Medium — binary mode, no JSON validation |
| **Server CPU** | Higher (JS per-client loop) | Much lower (C++ handles broadcast) |
| **Best answers** | "How much faster is uWS send vs ws send?" | "What's the max WS throughput possible?" |

---

## Recommendation: Approach A (Minimal Diff)

**Rationale:**

1. **Benchmark integrity.** The project's purpose is comparing transport performance. Keeping identical processing logic (JSON.parse, yield, manual broadcast) ensures the only variable is the WebSocket library. Approach B confounds the comparison by also removing server-side processing.

2. **Apples-to-apples.** The ws-server does `JSON.parse` + manual send loop. If uWS skips both, the comparison becomes "optimized uWS vs unoptimized ws" rather than "uWS vs ws."

3. **Incremental path.** Start with Approach A for valid comparison. Later, add Approach B as a separate "uWS-optimized" group to show the ceiling.

4. **Debugging.** Same logic makes it easy to verify both servers behave identically with the same Kafka input.

> **Future work:** After Approach A is validated, add Approach B as a 4th benchmark group ("uWS optimized") to show the maximum uWS performance ceiling.

---

## Phase Breakdown

| Phase | File | Description |
|-------|------|-------------|
| 1 | [phase-01-uws-server-impl.md](phase-01-uws-server-impl.md) | Create `uws-server/` with server.js, package.json, ecosystem.config.js |
| 2 | [phase-02-docker-config.md](phase-02-docker-config.md) | Dockerfile, docker-compose.yml, docker-compose.host.yml |
| 3 | [phase-03-go-client-update.md](phase-03-go-client-update.md) | Add uWS groups to Go benchmark client |
| 4 | [phase-04-benchmark-script-update.md](phase-04-benchmark-script-update.md) | Update run-benchmark-1gb.sh to start/stop uWS servers |

---

## Files to Create

| # | Path | Purpose |
|---|------|---------|
| 1 | `uws-server/server.js` | uWS Kafka→WS broadcast server |
| 2 | `uws-server/package.json` | Dependencies: uWebSockets.js, kafkajs |
| 3 | `uws-server/ecosystem.config.js` | PM2 config: 3 cluster instances, port 8091 |
| 4 | `uws-server/Dockerfile` | Docker image: node:20-bookworm-slim |
| 5 | `uws-server/docker-compose.yml` | 3 containers, bridge network, ports 50061-50063 |
| 6 | `uws-server/docker-compose.host.yml` | 3 containers, host network, ports 60061-60063 |

## Files to Modify

| # | Path | Change |
|---|------|--------|
| 1 | `benchmark-client/go-client/main.go` | Add 3 uWS groups (PM2, bridge, host) to `groups` slice |
| 2 | `run-benchmark-1gb.sh` | Add uWS Docker build/start, PM2 start, container readiness checks |
| 3 | `health-check-1gb.sh` | (Optional) Add uWS health check endpoints |

---

## Edge Cases & Gotchas

1. **`maxPayloadLength` default is 16KB** — must set to `0` (unlimited) to match ws-server's `maxPayload: 0`
2. **ArrayBuffer vs Buffer** — uWS message handler receives `ArrayBuffer`, use `Buffer.from(msg).toString()` for JSON.parse
3. **npm install from GitHub** — `uWebSockets.js` is not on npm; use `"uWebSockets.js": "github:uNetworking/uWebSockets.js#v20.61.0"`. Docker build needs git (included in `node:20-bookworm-slim`)
4. **PM2 cluster on macOS** — SO_REUSEPORT doesn't work for TCP on macOS; PM2 cluster mode is Linux-only (matches deployment target)
5. **No `http.createServer()`** — uWS creates its own server; health endpoint via `app.get('/health', handler)`
6. **Graceful shutdown** — use `uWS.us_listen_socket_close(listenSocket)` instead of `server.close()`
7. **`process.send("ready")`** — call inside `.listen()` callback for PM2 `wait_ready`
8. **Backpressure** — set `maxBackpressure: 1024 * 1024` and check `ws.send()` return value
