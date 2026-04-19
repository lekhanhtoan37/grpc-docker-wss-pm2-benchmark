---
title: "Kafka Systemd 1GB/s Throughput Benchmark"
description: "Add systemd Kafka on localhost:9092, benchmark 1GB/s throughput comparing Docker bridge vs host network"
status: in-progress
priority: P2
effort: "16-24h (Approach A), 10-16h (Approach B)"
branch: main
tags: [kafka, systemd, benchmark, throughput, docker-networking]
created: 2026-04-19
---

# Kafka Systemd 1GB/s Throughput Benchmark — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace Docker Kafka with systemd KRaft Kafka on localhost:9092, produce at 1GB/s, and measure throughput difference between WS (host/PM2) consumers and gRPC (Docker bridge) consumers.

**Architecture:** Kafka 3.9 KRaft runs as systemd service on Linux host. Producer floods topic at ~1GB/s with batching+LZ4. Three consumer groups (WS-host, gRPC-bridge, gRPC-host) each read independently. Benchmark client measures throughput (MB/s) and per-message latency for each group.

**Tech Stack:** Kafka 3.9 KRaft (systemd), node-rdkafka (native C++ bindings) or kafkajs, PM2 cluster, Docker bridge + host networking, hdr-histogram, LZ4 compression

**Platform:** Linux required (systemd). macOS can use `brew services` for development only.

---

## Current State Analysis

| Component | Current | Problem |
|-----------|---------|---------|
| Kafka | Docker Compose + Zookeeper | No systemd, extra latency through Docker |
| Producer | kafkajs, 100 msg/s, 1KB, no batching | 10,000x too slow, needs batching+compression |
| WS consumer | kafkajs `eachMessage`, PM2 3 workers | No batching, single-partition bottleneck |
| gRPC consumer | kafkajs `eachMessage`, 3 bridge + 3 host | Same bottleneck |
| Topic | 1 partition | Needs 12+ for parallelism |
| Benchmark client | Latency-only (hdr-histogram) | No throughput measurement |
| Docker bridge | kafka-net via container name | Connects to Docker Kafka; must change to host Kafka |
| OS | macOS Docker Desktop | Cannot run systemd; benchmark unreliable |

## Research Summary

1. **KafkaJS ceiling**: ~4-10K msg/s with processing. 1GB/s = 1M msg/s. KafkaJS is 5-10x slower than Java. Needs node-rdkafka or massive parallel kafkajs instances.
2. **Docker bridge overhead**: 12-17% throughput reduction, +20-50μs/packet. docker-proxy adds additional 15%. Total ~28% vs host.
3. **node-rdkafka**: 5-10x faster than kafkajs, supports native linger.ms, batching, LZ4. ~100-200 MB/s per consumer on single broker.
4. **Single-node 1GB/s**: Requires NVMe SSD, 8GB+ RAM, JVM heap 4G+, 12-24 partitions, may need multiple producer instances.
5. **6 consumers × 1GB/s aggregate**: Not feasible on single broker. Realistic target: 100-200 MB/s per node-rdkafka consumer, 600MB-1.2GB/s aggregate.

---

## Approach Comparison

| Criterion | A: node-rdkafka | B: kafkajs Multi-Producer |
|-----------|-----------------|--------------------------|
| **Max producer throughput** | ~500-800 MB/s (single instance) | ~50-100 MB/s per instance × N |
| **Max consumer throughput** | ~100-200 MB/s per consumer | ~20-50 MB/s per consumer |
| **Can reach 1GB/s produce?** | Likely (single instance, tuned) | Yes (10+ parallel producers) |
| **Code invasiveness** | High — rewrite all Kafka code | Low — extend existing kafkajs |
| **Build complexity** | High — native C++ deps, Alpine needs build-base | Low — pure JS |
| **Docker image size** | Larger (build tools in image) | Same as current |
| **Consumer throughput accuracy** | High — native batching | Low — kafkajs bottlenecks dominate |
| **Benchmark validity** | High — measures network, not client bottleneck | Medium — conflates client+network overhead |
| **Risk** | node-rdkafka build failures, Alpine compat | Cannot reach 1GB/s, benchmark ceiling unclear |
| **Effort** | 16-24h | 10-16h |

### Approach A: Systemd Kafka + node-rdkafka Throughput Benchmark (RECOMMENDED)

**Why recommended:** The goal is measuring Docker bridge vs host network overhead. Using kafkajs (which tops out at ~50 MB/s) means the client library is the bottleneck, not the network. node-rdkafka pushes throughput high enough that network differences become visible.

- **Pros:** Valid benchmark, single producer reaches near-1GB/s, measures real network overhead
- **Cons:** node-rdkafka native build complexity, Dockerfile needs build-base + librdkafka-dev, Alpine compatibility risk
- **Risks:** node-rdkafka segfaults on Alpine (mitigated: use node:20-bookworm-slim), LZ4 native lib conflicts
- **Rollback:** Revert to kafkajs, keep systemd Kafka

### Approach B: Systemd Kafka + kafkajs Multi-Producer Scaling

**Why alternative:** Lower risk, pure JS, works on any platform. But kafkajs becomes the bottleneck — you're measuring client library limits, not network differences.

- **Pros:** No native deps, same codebase pattern, works on macOS for dev
- **Cons:** 1GB/s requires 10+ producer processes, kafkajs ceiling hides network overhead, benchmark may show "no difference" because clients are saturated
- **Risks:** Cannot reach 1GB/s, results inconclusive
- **Rollback:** Remove multi-producer scripts, revert to single producer

---

## Phase Breakdown (Approach A — Recommended)

| Phase | Description | Effort | File |
|-------|-------------|--------|------|
| 01 | Systemd Kafka KRaft setup + topic creation | 2-3h | `phase-01-systemd-kafka.md` |
| 02 | Replace kafkajs with node-rdkafka in producer | 2-3h | `phase-02-rdkafka-producer.md` |
| 03 | Replace kafkajs with node-rdkafka in consumers | 2-3h | `phase-03-rdkafka-consumers.md` |
| 04 | Update Docker networking for host Kafka | 1-2h | `phase-04-docker-networking.md` |
| 05 | Add throughput measurement to benchmark client | 2-3h | `phase-05-throughput-benchmark.md` |
| 06 | Orchestration scripts + health check update | 1-2h | `phase-06-orchestration.md` |
| 07 | Run benchmark + collect results | 2-3h | `phase-07-run-benchmark.md` |
| **Total** | | **12-19h** | |

---

## File Impact Summary

### New Files
| File | Purpose |
|------|---------|
| `infra/kafka.service` | Systemd unit file for KRaft Kafka |
| `infra/server.properties` | KRaft broker config (tuned for 1GB/s) |
| `infra/setup-kafka.sh` | Install + configure + start Kafka |
| `infra/create-topic.sh` | Create benchmark topic with 12 partitions |
| `infra/teardown-kafka.sh` | Stop + remove Kafka (rollback) |
| `producer/producer-rdkafka.js` | High-throughput producer using node-rdkafka |
| `benchmark-client/client-throughput.js` | Throughput-aware benchmark client |
| `run-benchmark-1gb.sh` | New orchestration for 1GB/s benchmark |
| `health-check-1gb.sh` | Updated health check (systemd Kafka) |

### Modified Files
| File | Changes |
|------|---------|
| `producer/package.json` | Add `node-rdkafka` dep |
| `producer/producer.js` | Add rdkafka mode (keep kafkajs as fallback) |
| `grpc-server/package.json` | Add `node-rdkafka` dep |
| `grpc-server/Dockerfile` | Switch to node:20-bookworm-slim, add librdkafka-dev |
| `grpc-server/server.js` | Replace kafkajs consumer with node-rdkafka |
| `grpc-server/docker-compose.yml` | KAFKA_BROKER → localhost:9092, extra_hosts |
| `grpc-server/docker-compose.host.yml` | KAFKA_BROKER stays localhost:9092 |
| `ws-server/package.json` | Add `node-rdkafka` dep |
| `ws-server/server.js` | Replace kafkajs consumer with node-rdkafka |
| `ws-server/ecosystem.config.js` | Increase max_memory_restart to 1G |
| `benchmark-client/client.js` | Add throughput counters per group |
| `run-benchmark.sh` | Add systemd Kafka start/stop steps |
| `health-check.sh` | Check systemd Kafka instead of Docker Kafka |

### Unchanged Files
| File | Why |
|------|-----|
| `infra/docker-compose.yml` | Kept as fallback; systemd replaces it |
| `proto/benchmark.proto` | No changes to gRPC schema |
| `benchmark-client/package.json` | No new deps (throughput is computed inline) |

---

## Unresolved Questions

1. **Linux target machine?** — systemd requires Linux. Is there a dedicated benchmark server? What distro? (Affects package manager commands in setup script)
2. **NVMe available?** — 1GB/s sustained produce requires fast disk. SATA SSD maxes ~500 MB/s. Need NVMe for 1GB/s.
3. **RAM available?** — Kafka JVM 4G + OS page cache 4G + 6 Node.js consumers × 512MB = ~11GB minimum. 16GB recommended.
4. **node-rdkafka on Alpine?** — Current Dockerfile uses node:20-alpine. node-rdkafka requires build-base + librdkafka-dev + python3. May need switch to node:20-bookworm-slim. Plan accounts for this.
5. **Realistic throughput target?** — Research shows single-broker single-consumer node-rdkafka ceiling is ~100-200 MB/s. 1GB/s aggregate across 6 consumers is feasible but 1GB/s per consumer is not. Benchmark should report per-consumer MB/s and aggregate MB/s.
6. **Should we keep Docker Kafka as fallback?** — Plan preserves infra/docker-compose.yml for non-Linux dev.
