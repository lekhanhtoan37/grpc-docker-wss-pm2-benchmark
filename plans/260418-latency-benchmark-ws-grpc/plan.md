---
title: "WS vs gRPC Latency Benchmark"
description: "Latency p50/p99 benchmark: PM2 WS cluster vs gRPC Docker bridge"
status: pending
priority: P2
effort: 8h
branch: main
tags: [benchmark, websocket, grpc, kafka, docker, pm2]
created: 2026-04-18
---

# WS vs gRPC Latency Benchmark

## Overview

Measure p50/p99 end-to-end latency of:
- **WS path**: PM2 cluster (3 workers) + `ws` library on host
- **gRPC path**: 3 Docker containers (bridge network) + `@grpc/grpc-js`

Both consume from same Kafka topic. Benchmark client on host connects to all 6 endpoints simultaneously.

**Goal**: Quantify latency difference between PM2 WS cluster (host) vs gRPC Docker bridge under identical message load (100 msg/s, 1KB payload).

---

## Architecture

```
                    ┌──────────────────────────────────────────────┐
                    │  HOST                                        │
                    │                                              │
                    │  ┌─────────────────┐  ┌──────────────────┐   │
                    │  │ Kafka Producer  │  │ Benchmark Client │   │
                    │  │ 100 msg/s, 1KB  │  │ 3 WS + 3 gRPC   │   │
                    │  └────────┬────────┘  │ hdr-histogram    │   │
                    │           │           └──┬───┬───┬──┬──┬─┘   │
                    │           ▼              │   │   │  │  │     │
                    │    ┌─────────────┐       │   │   │  │  │     │
                    │    │ Kafka :9092 │       │   │   │  │  │     │
                    │    └──────┬──────┘       │   │   │  │  │     │
                    │           │              │   │   │  │  │     │
                    │  ┌────────┴────────┐     │   │   │  │  │     │
                    │  │ PM2 WS Cluster  │◄────┘   │   │  │  │     │
                    │  │ 3 workers :8080 │         │   │  │  │     │
                    │  │ (host network)  │         │   │  │  │     │
                    │  └─────────────────┘         │   │  │  │     │
                    │           8080 (shared port)─┘   │  │  │     │
                    │                                    │  │  │     │
                    │    ┌────────────────── Docker ─────┘  │  │     │
                    │    │  Bridge: grpc-net                │  │     │
                    │    │                                  │  │     │
                    │    │  ┌─────┐ ┌─────┐ ┌─────┐       │  │     │
                    │    │  │ctr-1│ │ctr-2│ │ctr-3│       │  │     │
                    │    │  │:510│ │:510│ │:510│       │  │     │
                    │    │  └──┬──┘ └──┬──┘ └──┬──┘       │  │     │
                    │    │     └───────┴───────┘           │  │     │
                    │    │            │ host.docker.int     │  │     │
                    │    └────────────│─────────────────────┘  │     │
                    │          :50051 :50052 :50053◄────────────┘     │
                    └──────────────────────────────────────────────────┘
```

---

## Approach A: Shared Consumer Group for WS + Unique Groups for gRPC

### Design

- Kafka topic `benchmark-messages` with **3 partitions**
- **WS cluster**: All 3 PM2 workers in same consumer group `ws-benchmark-group`. Kafka partitions messages across workers (1 partition each). Each worker only sees 1/3 of messages.
- **gRPC containers**: Each container uses unique consumer group `grpc-benchmark-{1,2,3}`. Each container gets ALL messages.
- **Benchmark client**: Connects to all 6 endpoints. Measures latency per message received.
- **No Redis needed** for WS cross-worker broadcast (each worker consumes its own partition independently).

### Latency Measurement

- Producer embeds `Date.now()` in payload
- Benchmark client receives message → `Date.now() - msg.timestamp` = latency
- Separate `hdr-histogram` per endpoint (6 histograms)
- WS histograms show latency for 1/3 of messages each
- gRPC histograms show latency for ALL messages each

### Pros

- **Simplest setup**. No Redis, no cross-worker broadcast
- **Realistic Kafka consumer pattern** (consumer groups = production usage)
- **Fewer moving parts** = fewer failure modes
- **Lower resource usage** (no Redis, fewer Kafka connections)

### Cons

- **WS endpoints receive different messages** than gRPC endpoints. Can't compare latency for same message across technologies
- **WS benchmark measures partition-level latency**, gRPC measures full-topic latency. Not apples-to-apple
- **Partition skew** possible if Kafka partitioner doesn't distribute evenly
- **3 WS clients combined see all messages**, but individually only 1/3. Must aggregate WS histograms for total picture

### Verdict

Fast to build. Useful for "what's the latency distribution of each technology under load" but NOT for "how much slower is gRPC than WS for the same message."

---

## Approach B: Unique Consumer Groups + Redis Fan-out (Accurate Comparison)

### Design

- Kafka topic `benchmark-messages` with **3 partitions**
- **WS cluster**: Each PM2 worker uses unique consumer group `ws-benchmark-worker-{0,1,2}`. Each worker gets ALL messages. Redis pub/sub for cross-worker broadcast NOT needed — each worker independently gets all messages.
  - Actually: with 3 workers × unique group = each worker subscribes to all 3 partitions = each gets all messages
  - BUT: 3 workers × 3 unique groups = 9 consumer instances total competing for 3 partitions. Kafka assigns partitions to consumers in same group. With unique groups, each group has 1 consumer → gets all partitions.
- **gRPC containers**: Same as Approach A. Unique group per container.
- **Benchmark client**: Each of 6 endpoints receives ALL messages. Can compare latency for same `msg.seq` across WS and gRPC.
- **Message matching**: Use `msg.seq` (sequential ID) to match same message across endpoints.

### Wait — Revised Design (Correct Consumer Group Behavior)

- Topic `benchmark-messages` with **1 partition** (simplifies everything)
- All 6 consumers (3 WS workers + 3 gRPC containers) use **unique consumer groups**
- Each consumer gets ALL 100 messages/sec
- No partitioning complexity
- No Redis needed

### Latency Measurement

- Producer embeds `{ timestamp: Date.now(), seq: incrementing_counter }` in payload
- Benchmark client receives message from each endpoint
- Per-endpoint histogram: `Date.now() - msg.timestamp`
- Can also compare: for same `msg.seq`, WS latency vs gRPC latency (message-level comparison)

### Pros

- **Apples-to-apple comparison**: every endpoint receives every message
- **Can compare latency for same message** across WS and gRPC
- **No partitioning complexity** (1 partition)
- **No Redis needed** (unique consumer groups = each consumer gets all messages)
- **Statistical rigor**: same message set, same time window

### Cons

- **6 Kafka consumers** reading same topic = 6× Kafka bandwidth
- **More Kafka connections** (6 consumers + 1 producer)
- **Single partition** = no parallelism in Kafka consumption (fine for 100 msg/s)
- **Higher resource usage** than Approach A

### Verdict

More accurate comparison. Same message set for all endpoints. Recommended for benchmarking.

---

## Recommended Approach: **Approach B**

**Why**: The goal is comparing WS vs gRPC latency. Approach B guarantees every endpoint receives every message, enabling message-level latency comparison. The extra Kafka connections are negligible at 100 msg/s. No Redis needed with unique consumer groups.

**Key simplification**: Use **1 partition, unique consumer groups** for all 6 consumers. Each consumer sees all messages. No cross-worker broadcast needed.

---

## Phases

| Phase | File | Description | Est. |
|-------|------|-------------|------|
| 1 | [phase-01-kafka-setup.md](phase-01-kafka-setup.md) | Kafka topic + producer app (100 msg/s, 1KB) | 1.5h |
| 2 | [phase-02-ws-server.md](phase-02-ws-server.md) | PM2 cluster WS server (3 workers, Kafka consumer) | 1.5h |
| 3 | [phase-03-grpc-server.md](phase-03-grpc-server.md) | gRPC server Docker containers (3×, bridge network) | 2h |
| 4 | [phase-04-benchmark-client.md](phase-04-benchmark-client.md) | Benchmark client (6 connections, hdr-histogram, output) | 2h |
| 5 | [phase-05-integration-test.md](phase-05-integration-test.md) | Full integration test + results collection | 1h |

**Total**: ~8h

---

## Dependencies

```
phase-01 → phase-02, phase-03, phase-04
phase-02 + phase-03 + phase-04 → phase-05
```

Phase 1 (Kafka + producer) must be done first. Phases 2-4 can be done in parallel. Phase 5 requires all.

---

## Tech Stack

| Component | Tech | Version |
|-----------|------|---------|
| WS server | `ws` | ^8.x |
| gRPC server | `@grpc/grpc-js` | ^1.12 |
| gRPC proto loader | `@grpc/proto-loader` | ^0.7 |
| Kafka client | `kafkajs` | ^2.x |
| Histogram | `hdr-histogram-js` | ^3.x |
| Process manager | PM2 | ^5.x |
| Containerization | Docker + Compose | v2+ |
| Node.js | node | 20 LTS |

---

## Project Structure

```
grpc-docker-wss-pm2/
├── proto/
│   └── benchmark.proto
├── producer/
│   ├── package.json
│   └── producer.js
├── ws-server/
│   ├── package.json
│   ├── server.js
│   └── ecosystem.config.js
├── grpc-server/
│   ├── package.json
│   ├── server.js
│   ├── Dockerfile
│   └── docker-compose.yml
├── benchmark-client/
│   ├── package.json
│   ├── client.js
│   └── proto/
│       └── benchmark.proto    (copy or symlink)
└── plans/
    └── 260418-latency-benchmark-ws-grpc/
        ├── plan.md
        ├── phase-01-kafka-setup.md
        ├── phase-02-ws-server.md
        ├── phase-03-grpc-server.md
        ├── phase-04-benchmark-client.md
        └── phase-05-integration-test.md
```

---

## Unresolved Questions

1. **Kafka on host**: Is Kafka already running on host? If not, add Kafka setup step (Docker or local install)
2. **Node 20 vs 22**: Research assumes Node 20 LTS. Confirm target version
3. **macOS vs Linux**: Docker networking differs (`host.docker.internal` vs `host-gateway`). Plan assumes macOS. Adjust `extra_hosts` for Linux
4. **PM2 global install**: Is PM2 already installed globally? If not, `npm i -g pm2`
5. **Warmup duration**: 30s or 60s? Depends on Kafka consumer group rebalancing time
6. **Measurement duration**: 3 min or 5 min? Longer = more stable p99, but more resource usage
7. **Kafka topic config**: Retention, cleanup policy? Suggest `delete` with 1-hour retention for benchmark
