---
title: "NATS Integration into run-benchmark-1gb.sh"
description: "Integrate NATS server (systemd) + nats-worker (PM2 + Docker host) into the production benchmark script"
status: pending
priority: P2
effort: 4h
branch: main
tags: [benchmark, nats, integration, 1gb]
created: 2026-04-30
---

## Goal

Add NATS as a 4th transport to `run-benchmark-1gb.sh` so the benchmark reports throughput/latency for all 12 groups (9 existing + 3 NATS).

## Two Approaches

### Approach A: Full Integration (systemd NATS + Docker host nats-worker + PM2 nats-worker)

Deploys all components matching the existing pattern for gRPC/uWS/Go-WS:
- **Step 1c**: NATS server via systemd (mirrors Kafka systemd setup)
- **Step 4g**: nats-worker PM2 (3 instances, ports 8095-8097)
- **Step 4h**: nats-worker Docker host (3 instances, ports 60081-60083)
- Updates Steps 6b, 6d, 7, 8, cleanup(), and health-check-1gb.sh

| Pros | Cons |
|------|------|
| Mirrors all existing transport patterns | More code changes (~150 lines added) |
| Tests Docker host networking path for NATS | 6 additional nats-worker processes |
| Complete benchmark coverage (12 groups) | Higher resource usage on benchmark server |
| Docker bridge skipped (correct — NATS on host) | — |

### Approach B: Minimal Integration (systemd NATS + PM2 nats-worker only)

Deploys only PM2 nats-worker (no Docker host variant):
- **Step 1c**: NATS server via systemd (same as A)
- **Step 4g**: nats-worker PM2 only (3 instances, ports 8095-8097)
- Skips Docker host nats-worker entirely
- Updates Steps 6b, 6d, 7, 8, cleanup() partially

| Pros | Cons |
|------|------|
| Fewer changes (~80 lines added) | Missing Docker host NATS data point |
| Lower resource usage | Inconsistent with other transports (all have Docker host) |
| Simpler debugging | Benchmark report has only 10 groups, not 12 |

## Recommendation

**Approach A (Full Integration).** The benchmark's purpose is comparing transports across deployment modes. Skipping Docker host for NATS breaks the comparison matrix. The additional complexity is bounded and follows existing patterns exactly.

## Phases

| Phase | File | Description |
|-------|------|-------------|
| 1 | [phase-01-nats-server-systemd.md](phase-01-nats-server-systemd.md) | NATS server systemd service + Step 1c in benchmark script |
| 2 | [phase-02-script-integration.md](phase-02-script-integration.md) | nats-worker deployment + all script updates |
| 3 | [phase-03-testing-evidence.md](phase-03-testing-evidence.md) | Smoke test script + test evidence collection |

## Key Decisions (from research)

- **systemd, not Docker**: NATS on localhost avoids Docker networking overhead; mirrors Kafka pattern
- **Skip Docker bridge**: NATS is on host; bridge workers can't reach it without `extra_hosts` hacks
- **Consumer group**: `nats-benchmark-worker` prefix catches all instances
- **Ports**: PM2 8095-8097, Docker host 60081-60083 (no conflicts)
