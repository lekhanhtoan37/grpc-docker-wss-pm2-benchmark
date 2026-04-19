---
title: "gRPC Docker Host Network Benchmark"
description: "Add gRPC host-network Docker instances and extend benchmark to compare WS vs gRPC-bridge vs gRPC-host latency"
status: pending
priority: P2
effort: 5h
branch: main
tags: [grpc, docker, benchmark, networking]
created: 2026-04-19
---

## Goal

Add 3 gRPC instances in Docker `network_mode: host` and extend benchmark to compare **3 groups**: WS (host/PM2), gRPC-bridge (Docker bridge), gRPC-host (Docker host).

## Current State

| Component | Runtime | Network | Ports | Kafka |
|-----------|---------|---------|-------|-------|
| WS (3 workers) | PM2 cluster on host | Host | 8080 (shared) | `localhost:9092` |
| gRPC-bridge (3) | Docker containers | Bridge + port mapping | 50051-50053 | `benchmark-kafka:29092` |
| Benchmark client | Node.js on host | Host | connects to all | — |
| Kafka/Zookeeper | Docker containers | Bridge | 9092 (host), 29092 (internal) | — |

Previous results: gRPC-bridge ~+0.001ms slower than WS at p50-p99.

## Key Constraint

**macOS Docker Desktop `network_mode: host` shares the Linux VM network, NOT macOS host network.** VM boundary overhead (~0.3-1ms) dominates. Results on macOS won't reflect Linux production performance. Must document this limitation.

---

## Approach A: Docker Host-Network Compose (Standard)

New `docker-compose.host.yml` alongside existing bridge compose. gRPC server port configurable via env var. Benchmark client adds 3rd group.

### Architecture

```
Host OS
├── PM2 (cluster mode)
│   ├── ws-worker-1 (port 8080)      ← WS group
│   ├── ws-worker-2 (port 8080)
│   └── ws-worker-3 (port 8080)
├── Docker (host network)
│   ├── grpc-host-1 (port 60051)     ← gRPC-host group
│   ├── grpc-host-2 (port 60052)
│   └── grpc-host-3 (port 60053)
├── Docker (bridge network "grpc-net")
│   ├── grpc-bridge-1 (50051→50051)  ← gRPC-bridge group
│   ├── grpc-bridge-2 (50052→50051)
│   └── grpc-bridge-3 (50053→50051)
└── Docker (bridge network "kafka-net")
    ├── kafka (:9092 host, :29092 internal)
    └── zookeeper
```

### Changes

| File | Action | Change |
|------|--------|--------|
| `grpc-server/server.js` | **Modify** | `const PORT = process.env.GRPC_PORT \|\| 50051` |
| `grpc-server/Dockerfile` | **Modify** | Remove `EXPOSE 50051` (or generalize) |
| `grpc-server/docker-compose.host.yml` | **Create** | 3 services, `network_mode: host`, ports 60051-60053, `KAFKA_BROKER=localhost:9092` |
| `benchmark-client/client.js` | **Modify** | GROUPS config, 3-group table, 9 histograms |
| `run-benchmark.sh` | **Modify** | Add host compose startup, health checks for 60051-60053 |
| `health-check.sh` | **Modify** | Add host gRPC port checks |

### Effort: ~3h

### Pros
- Minimal changes to existing codebase
- Single benchmark run measures all 3 groups simultaneously
- Direct comparison: same Docker image, only network mode differs
- Easy to understand and maintain

### Cons
- **macOS results are unreliable** — VM boundary overhead masks host networking benefit
- Requires Docker Desktop 4.34+ with host networking enabled in settings
- macOS users may get confusing results (host mode may not be faster than bridge)

### Risks
| Risk | Impact | Mitigation |
|------|--------|-----------|
| macOS host networking bugs | Containers unreachable | Bind to `127.0.0.1` not `0.0.0.0`; test on Linux for real results |
| Port conflicts 60051-60053 | Startup fails | Document port requirements; check with `netstat` |
| Benchmark client overhead with 9 connections | Skewed results | Monitor event loop lag; existing code already tracks this |
| Docker Desktop restart makes host containers unreachable | Wasted benchmark run | Add health check before each run |

---

## Approach B: Separate Compose + Native Process Fallback

Docker host-network compose for Linux. **Node.js native process (no Docker)** as "host baseline" for macOS. Unified benchmark script with `--mode` flag.

### Architecture

```
--mode=linux (Linux only):
  Same as Approach A — docker-compose.host.yml with network_mode: host

--mode=macos (default on macOS):
  Host OS
  ├── PM2 (cluster mode)
  │   ├── ws-worker-1 (port 8080)     ← WS group
  │   ├── ws-worker-2 (port 8080)
  │   └── ws-worker-3 (port 8080)
  ├── Node.js native processes
  │   ├── grpc-native-1 (port 60051)  ← gRPC-native group (true baseline)
  │   ├── grpc-native-2 (port 60052)
  │   └── grpc-native-3 (port 60053)
  └── Docker (bridge)
      ├── grpc-bridge-1 (50051)       ← gRPC-bridge group
      ├── grpc-bridge-2 (50052)
      ├── grpc-bridge-3 (50053)
      ├── kafka
      └── zookeeper
```

### Changes

| File | Action | Change |
|------|--------|--------|
| `grpc-server/server.js` | **Modify** | Configurable PORT, proto path relative for native run |
| `grpc-server/Dockerfile` | **Modify** | Generalize EXPOSE |
| `grpc-server/docker-compose.host.yml` | **Create** | Linux host-network compose |
| `grpc-server/start-native.sh` | **Create** | Start 3 native gRPC server processes with PM2 or shell |
| `benchmark-client/client.js` | **Modify** | GROUPS config, `--mode` flag, 3-group table |
| `run-benchmark.sh` | **Modify** | OS detection, mode flag, conditional startup |
| `health-check.sh` | **Modify** | Mode-aware port checks |

### Effort: ~5h

### Pros
- **macOS gets meaningful results** — native process = true zero-overhead baseline
- Native gRPC process on macOS eliminates Docker entirely for the "host" group
- More portable across macOS and Linux
- macOS results actually answer: "what's the Docker bridge overhead?"

### Cons
- More files to maintain
- OS detection logic in shell scripts
- Native gRPC process needs `npm install` in grpc-server dir
- Two different "host" baselines (Docker host on Linux, native on macOS)

### Risks
| Risk | Impact | Mitigation |
|------|--------|-----------|
| Native process management complexity | Process leak | Use PM2 or trap EXIT |
| Proto path differs native vs Docker | Server fails to start | Use `__dirname`-relative path |
| OS detection wrong | Wrong mode selected | Allow explicit `--mode` override |
| Different results on macOS vs Linux confuse users | Misleading comparisons | Clearly label platform in output |

---

## Recommendation: Approach A

**Justification:**

1. **Simpler** — 1 new file (`docker-compose.host.yml`), 4 modified files. Approach B needs `start-native.sh`, OS detection, mode flag.
2. **Fairer comparison** — All 3 groups use the same Docker image. Only network mode differs. In Approach B, the "native" group eliminates Docker entirely (different binary, different runtime path), making the comparison confounded.
3. **The macOS limitation is acceptable** — The project goal is to measure Docker network overhead. If the macOS results show host ≈ bridge (because VM dominates), that's a valid finding: "on macOS Docker Desktop, host networking provides no measurable benefit."
4. **Phase B can be added later** — If macOS users need a native baseline, add it as an optional mode. Don't over-engineer upfront.
5. **Lower risk** — Fewer moving parts, less to debug, less process management.

The macOS caveat should be prominently documented in benchmark output, not worked around with a fundamentally different measurement approach.

---

## Phases (Approach A)

| Phase | Description | Effort |
|-------|-------------|--------|
| [Phase 01](phase-01-server-config.md) | Make gRPC server port configurable | 30m |
| [Phase 02](phase-02-host-compose.md) | Create docker-compose.host.yml | 30m |
| [Phase 03](phase-03-client-3group.md) | Extend benchmark client for 3-group comparison | 1.5h |
| [Phase 04](phase-04-scripts.md) | Update run-benchmark.sh and health-check.sh | 45m |
| [Phase 05](phase-05-integration.md) | Integration test + macOS documentation | 45m |
| **Total** | | **~4h** |

---

## Unresolved Questions

1. **Port range for host-networked gRPC**: Use 60051-60053 (as proposed) or 50061-50063? 600xx avoids confusion with bridge ports (50051-50053) but is non-standard. 5006x is closer to existing range.
2. **Kafka topic partitions**: Currently 1 partition. With 9 consumers (3 WS + 3 gRPC-bridge + 3 gRPC-host), only one consumer per group gets messages. Should we increase partitions to 3?
3. **Resource constraints**: Should we add `cpus` and `memory` limits to both bridge and host containers for fair comparison?
4. **macOS Docker Desktop version**: Host networking requires 4.34+. Should `run-benchmark.sh` check the version and warn?
5. **Simultaneous or sequential runs**: Run all 3 groups simultaneously (current plan) or sequential per-group runs to avoid client bottleneck? Simultaneous is closer to production but adds client load.
6. **gRPC keepalive**: Host-networked containers may have different keepalive behavior since there's no NAT timeout. Should keepalive settings differ?
