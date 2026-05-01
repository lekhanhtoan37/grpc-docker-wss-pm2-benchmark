---
title: "NATS Throughput/Latency Benchmark"
description: "Add NATS benchmark to compare with existing WS/gRPC results"
status: pending
priority: P2
effort: 12h
branch: main
tags: [benchmark, nats, go, kafka, feature]
created: 2026-04-30
---

# NATS Throughput/Latency Benchmark

## Goal

Add NATS as a 4th transport option alongside WS, gRPC. Compare throughput (MB/s, msg/s) and latency (p50→p99.9) using same HDR histogram pipeline, same message format, same benchmark client patterns.

## Current State

9 deployment modes benchmarked:

| # | Group | Transport | Deploy |
|---|-------|-----------|--------|
| 1 | WS (host/PM2) | WebSocket | PM2 fork x3 |
| 2 | uWS (host/PM2) | WebSocket | PM2 fork x3 |
| 3 | Go WS (host/PM2) | WebSocket | PM2 fork x3 |
| 4 | uWS bridge | WebSocket | Docker bridge + nginx |
| 5 | Go WS bridge | WebSocket | Docker bridge + nginx |
| 6 | uWS host | WebSocket | Docker host x3 |
| 7 | Go WS host | WebSocket | Docker host x3 |
| 8 | gRPC bridge | gRPC | Docker bridge + nginx |
| 9 | gRPC host | gRPC | Docker host x3 |

## New NATS Modes

| # | Group | Transport | Deploy |
|---|-------|-----------|--------|
| 10 | NATS bridge | NATS | Docker bridge |
| 11 | NATS host | NATS | Docker host |
| 12 | NATS PM2 | NATS | PM2 fork x3 |

## Approaches

### Approach A: Integrated Kafka→NATS Worker (RECOMMENDED)

- New Go app `nats-worker/` — Kafka consumer → NATS publisher
- Mirrors `go-ws-server/` pattern exactly
- Benchmark client adds `ConnectNATS()` worker
- Same message format, same latency measurement
- Apples-to-apples comparison with WS/gRPC groups

### Approach B: Standalone NATS Benchmark App

- New Go app `nats-bench/` — self-contained producer + subscriber + stats
- No Kafka dependency for NATS benchmark
- Measures pure NATS performance
- Cannot directly compare with WS/gRPC (different end-to-end path)

## Trade-off Summary

| Factor | Approach A | Approach B |
|--------|-----------|-----------|
| Comparable results | ✅ Same Kafka→Worker→Client path | ❌ Skips Kafka entirely |
| Complexity | Medium — new worker + client module | Low — single app |
| Effort | 8h | 4h |
| Reuse | Uses existing benchmark-client infra | Standalone |
| Value | Full comparison matrix | NATS-only perf numbers |
| **Recommended** | **✅ YES** | Supplement only |

## Phases

| Phase | File | Description | Effort |
|-------|------|-------------|--------|
| 01 | `phase-01-nats-infra.md` | NATS server Docker deployment | 1h |
| 02 | `phase-02-approach-a.md` | Kafka→NATS worker (Approach A) | 3h |
| 03 | `phase-03-approach-b.md` | Standalone NATS benchmark (Approach B) | 2h |
| 04 | `phase-04-benchmark-client.md` | NATS subscriber in benchmark client | 3h |
| 05 | `phase-05-orchestration.md` | Shell scripts, Docker Compose, integration | 3h |
| **Total** | | | **12h** |

## Architecture

```
Kafka Producer → Kafka Topic → [N nats-workers consume Kafka]
                                     │
                                     ▼
                              NATS Subject
                              "benchmark.messages"
                                     │
                          ┌──────────┼──────────┐
                          ▼          ▼          ▼
                       [benchmark-client NATS subscribers]
                          │          │          │
                          ▼          ▼          ▼
                    HDR Histogram  Stats   Report
```

## Related Code

| File | Action | Purpose |
|------|--------|---------|
| `nats-worker/` | CREATE | Kafka→NATS bridge worker |
| `nats-bench/` | CREATE | Standalone NATS benchmark |
| `benchmark-client/go-client/internal/worker/nats.go` | CREATE | NATS subscriber client |
| `benchmark-client/go-client/main.go` | MODIFY | Add NATS groups |
| `benchmark-client/go-client/go.mod` | MODIFY | Add nats.go dependency |
| `benchmark-client/go-client/internal/worker/runner.go` | MODIFY | Handle "nats" type |
| `infra/docker-compose.yml` | MODIFY | Add NATS service |
| `run-benchmark.sh` | MODIFY | Add NATS startup steps |

## Success Criteria

- [ ] NATS groups appear in benchmark report alongside WS/gRPC
- [ ] Throughput: MB/s, msg/s measured
- [ ] Latency: p50, p75, p90, p95, p99, p99.9 via HDR histogram
- [ ] 3 deployment modes: bridge, host, PM2
- [ ] Same warmup/duration/measurement flow
- [ ] Delta comparison vs WS baseline works
- [ ] No regression to existing WS/gRPC benchmarks
