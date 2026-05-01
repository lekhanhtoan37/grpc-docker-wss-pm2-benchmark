# Research Report — NATS Subscription Modes & Deployment Patterns

## Subscription Modes for Benchmarking

### Core NATS Push (recommended)
- **Async callback**: `nc.Subscribe("foo", handler)` — fastest
- **Channel**: `nc.ChanSubscribe("foo", ch)` — backpressure control
- **Sync**: `nc.SubscribeSync("foo")` + `sub.NextMsg(timeout)` — simpler but slower

### JetStream Pull (not recommended for throughput benchmark)
- Requires explicit `Ack()` — adds overhead
- Better for durability/reliability benchmarks

**Verdict**: Core NATS push + queue groups for raw throughput.

## Queue Groups
```go
nc.QueueSubscribe("bench.subject", "worker-group", handler)
```
- NATS round-robins msgs across subscribers in same queue
- Each msg delivered to exactly one subscriber
- Workers can be on different machines
- For benchmark: N workers = N-way parallelism

## Core NATS vs JetStream
| Aspect | Core NATS | JetStream |
|--------|-----------|-----------|
| Latency | ~100µs, sub-ms | Higher (disk I/O) |
| Throughput | 10M+ msg/s | Lower (ack overhead) |
| Delivery | At-most-once | At-least-once |
| Persistence | None | File/DB |
| Ack | Not required | Required |

## Docker Compose Patterns

### Single server
```yaml
services:
  nats:
    image: nats:2-alpine
    ports: ["4222:4222", "8222:8222"]
    command: --http_port 8222 --max_payload 1048576
```

### 3-node cluster
```yaml
services:
  nats:
    image: nats:latest
    command: --cluster_name NATS --cluster nats://0.0.0.0:6222
    ports: ["4222:4222", "8222:8222"]
  nats-1:
    image: nats:latest
    command: --cluster_name NATS --cluster nats://0.0.0.0:6222 --routes=nats://nats:6222
  nats-2:
    image: nats:latest
    command: --cluster_name NATS --cluster nats://0.0.0.0:6222 --routes=nats://nats:6222
```

## HDR Histogram Integration
```go
h := hdrhistogram.New(0, 60_000_000_000, 3) // 0-60s, 3 sig digits
h.RecordValue(latencyNs)
p50 := h.ValueAtPercentile(50)
p99 := h.ValueAtPercentile(99)
```

## Key Takeaways
1. Use Core NATS (not JetStream) for apples-to-apples throughput comparison
2. Timestamp-in-payload approach matches existing Kafka→WS/gRPC benchmark pattern
3. Queue groups enable N-worker parallelism without custom load balancing
4. NATS monitoring endpoint (8222) provides built-in throughput stats
5. Docker deployment trivially simple vs WebSocket/gRPC servers
