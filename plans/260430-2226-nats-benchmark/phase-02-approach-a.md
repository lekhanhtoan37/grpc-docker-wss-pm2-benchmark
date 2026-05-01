---
title: "Phase 02: Approach A — Kafka→NATS Worker"
phase: 2-of-5
status: pending
priority: P1
effort: 3h
blocked-by: [phase-01]
blocks: [phase-04]
approach: A
---

# Phase 02: Approach A — Kafka→NATS Worker

## Context

- Existing pattern: `go-ws-server/main.go` — Kafka consumer → batch → fanout to WS clients
- NATS worker mirrors this: Kafka consumer → batch → `nc.Publish()` to NATS subject
- Message format unchanged: `{"timestamp":..., "seq":..., "data":"..."}`
- Same linger-batch: BATCH_MAX=20, LINGER_MS=5ms

## Overview

New Go app `nats-worker/`. Consumes Kafka, batches messages, publishes to NATS subject `benchmark.messages`. Three deployment modes: Docker bridge, Docker host, PM2.

## Requirements

### Functional
- Consume from Kafka topic `benchmark-messages`
- Publish each message to NATS subject `benchmark.messages`
- Support batch publishing (join with `\n`, same as WS)
- Support linger-batch (BATCH_MAX, LINGER_MS)
- Multiple instances via consumer group (load-balanced Kafka partitions)
- Health endpoint `/health`
- Graceful shutdown on SIGINT/SIGTERM

### Non-Functional
- Same throughput target as go-ws-server
- Zero message loss during normal operation
- Minimal allocation — reuse buffers
- Stats logging every 5s (same pattern as go-ws-server)

## Architecture

```
nats-worker/main.go
├── Kafka Consumer (sarama, same config as go-ws-server)
│   ├── Consumer group: nats-benchmark-worker-{INSTANCE}
│   ├── Same drain pattern
│   └── Same batch accumulation
├── NATS Publisher
│   ├── nc.Publish("benchmark.messages", payload)
│   ├── Payload = messages joined by \n (same as WS)
│   └── Single connection, synchronous publish
├── Health Server (:PORT/health)
└── Stats Logger (5s interval)
```

### Data Flow

```
Kafka → sarama ConsumerClaim → entries [][]byte
  → appendBatch(entries)
  → flushToNATS()
    → join entries with \n
    → nc.Publish("benchmark.messages", payload)
```

### Deployment Modes

| Mode | Docker Compose | Network | Port |
|------|---------------|---------|------|
| bridge | `nats-worker/docker-compose.yml` | bridge + nginx | 50081 |
| host | `nats-worker/docker-compose.host.yml` | host | 60081-60083 |
| PM2 | `nats-worker/ecosystem.config.js` | host | 8095-8097 |

Note: NATS worker doesn't serve HTTP to clients. The health port is for monitoring only. Benchmark client connects to NATS server (4222), not to the worker.

## Implementation Steps

1. **Create `nats-worker/` directory structure**

   ```
   nats-worker/
   ├── main.go
   ├── go.mod
   ├── Dockerfile
   ├── docker-compose.yml      (bridge mode)
   ├── docker-compose.host.yml (host mode)
   └── ecosystem.config.js     (PM2 mode)
   ```

2. **Create `nats-worker/go.mod`**

   ```
   module nats-worker
   go 1.22
   require (
       github.com/IBM/sarama v1.45.1
       github.com/nats-io/nats.go v1.39.1
   )
   ```

3. **Create `nats-worker/main.go`**

   Structure mirrors `go-ws-server/main.go`:
   - Same env vars: PORT, KAFKA_BROKER, KAFKA_TOPIC, INSTANCE, BATCH_MAX, LINGER_MS
   - New env vars: NATS_URL (default `nats://localhost:4222`), NATS_SUBJECT (default `benchmark.messages`)
   - `Server` struct replaces WS client tracking with NATS connection
   - `flushToNATS()` instead of `flushToClients()` — single `nc.Publish(subject, payload)`
   - Same `consumerHandler` with drain loop
   - Same `printStats()` with 5s interval
   - Health endpoint on `:PORT/health`

   Key differences from go-ws-server:
   - No WS client management (clients, clientsMu, sendCh)
   - `flushToNATS()` publishes one message to NATS subject (not N messages to N clients)
   - NATS connection established at startup, reconnect on failure

4. **Create `nats-worker/Dockerfile`**

   ```dockerfile
   FROM golang:1.22-alpine AS build
   WORKDIR /app
   COPY go.mod go.sum ./
   RUN go mod download
   COPY . .
   RUN CGO_ENABLED=0 go build -o nats-worker .

   FROM alpine:3.19
   COPY --from=build /app/nats-worker /usr/local/bin/
   EXPOSE 8095
   CMD ["nats-worker"]
   ```

5. **Create `nats-worker/docker-compose.yml`** (bridge mode)

   ```yaml
   services:
     nats-worker-1:
       build: .
       environment:
         CONTAINER_ID: "nats-bridge-1"
         KAFKA_BROKER: "192.168.0.9:9091"
         KAFKA_TOPIC: "benchmark-messages"
         NATS_URL: "nats://benchmark-nats:4222"
         NATS_SUBJECT: "benchmark.messages"
         PORT: "8095"
       networks:
         - backend

     nats-worker-2:
       build: .
       environment:
         CONTAINER_ID: "nats-bridge-2"
         # ... same pattern, CONTAINER_ID differs
       networks:
         - backend

     nats-worker-3:
       build: .
       environment:
         CONTAINER_ID: "nats-bridge-3"
       networks:
         - backend

   networks:
     backend:
       external: true
   ```

6. **Create `nats-worker/docker-compose.host.yml`** (host mode)

   Same pattern as `grpc-server/docker-compose.host.yml` — `network_mode: host`, different ports for health endpoints.

7. **Create `nats-worker/ecosystem.config.js`** (PM2 mode)

   ```javascript
   module.exports = {
     apps: [{
       name: "nats-benchmark",
       script: "nats-worker",  // compiled binary
       instances: 3,
       exec_mode: "fork",
       env: {
         KAFKA_BROKER: "192.168.0.9:9091",
         KAFKA_TOPIC: "benchmark-messages",
         NATS_URL: "nats://localhost:4222",
         NATS_SUBJECT: "benchmark.messages",
         PORT: 8095,
       }
     }]
   };
   ```

8. **Key NATS connection details**

   ```go
   nc, err := nats.Connect(NATS_URL,
       nats.Name("nats-worker-"+INSTANCE),
       nats.ReconnectWait(2*time.Second),
       nats.MaxReconnects(-1),
       nats.BufferSize(8*1024*1024),  // 8MB send buffer
       nats.FlushInterval(100*time.Millisecond),
   )
   ```

9. **Batch publish strategy**

   Two options:
   - **Option 1: Single batch message** — join entries with `\n`, publish as one NATS message (matches WS pattern). Subscriber splits by `\n`.
   - **Option 2: Individual publishes** — one `nc.Publish()` per message. More NATS-idiomatic but different batching semantics.

   **Choose Option 1** — keeps latency measurement identical to WS/gRPC (batch arrives as one unit). Subscriber sees same format as WS frames.

## Files

| File | Action |
|------|--------|
| `nats-worker/main.go` | CREATE |
| `nats-worker/go.mod` | CREATE |
| `nats-worker/Dockerfile` | CREATE |
| `nats-worker/docker-compose.yml` | CREATE |
| `nats-worker/docker-compose.host.yml` | CREATE |
| `nats-worker/ecosystem.config.js` | CREATE |

## Todo Checklist

- [ ] Create nats-worker/ directory
- [ ] Implement main.go (Kafka consumer + NATS publisher)
- [ ] Create go.mod with sarama + nats.go
- [ ] Create Dockerfile
- [ ] Create docker-compose.yml (bridge)
- [ ] Create docker-compose.host.yml (host)
- [ ] Create ecosystem.config.js (PM2)
- [ ] Test: standalone `go run main.go` with local Kafka + NATS
- [ ] Test: `docker compose up --build` with bridge networking
- [ ] Test: verify messages published to NATS subject

## Success Criteria

- Worker consumes from Kafka, publishes to NATS
- Stats log shows batches/msgs throughput
- 3 instances load-balance via Kafka consumer group
- No memory leaks under 5min sustained load
- Health endpoint responds

## Trade-off Analysis

### Pros
- ✅ Same end-to-end path as WS/gRPC (Kafka→Worker→Transport→Client)
- ✅ Directly comparable latency measurements
- ✅ Reuses existing benchmark client infrastructure
- ✅ Same deployment modes (bridge, host, PM2)

### Cons
- ❌ NATS publish adds minimal overhead vs direct fanout
- ❌ Batch semantics differ from core NATS (one message = batch of entries)
- ❌ NATS at-most-once delivery means potential message loss during reconnect

### Effort: 3h

### Apples-to-apple comparison: ✅ YES
Same Kafka producer, same message format, same timestamp extraction, same HDR histogram pipeline.

### Recommended: ✅ YES
This is the primary approach. It produces results directly comparable with existing WS/gRPC benchmarks.

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| NATS publish blocks under load | Low | High | Use buffered connection, monitor pending |
| Consumer group rebalancing | Medium | Low | Same session timeout config as go-ws-server |
| NATS reconnect loses messages | Medium | Medium | Accept at-most-once; document in results |
