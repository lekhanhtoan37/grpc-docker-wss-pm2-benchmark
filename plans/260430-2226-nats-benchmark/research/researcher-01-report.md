# Research Report — NATS Benchmarking (Client Libraries & Performance)

## NATS Go Client (nats.go)
- `github.com/nats-io/nats.go` — official Go client
- Core API: `nc.Connect()`, `nc.Publish(subject, data)`, `nc.Subscribe(subject, handler)`
- Channel sub: `nc.ChanSubscribe(subject, ch)` — best for high throughput + backpressure
- Queue groups: `nc.QueueSubscribe(subject, queueName, handler)` — load-balanced across workers
- Flush: `nc.Flush()` / `nc.FlushTimeout()` — ensure all msgs sent
- Connection options: `RetryOnFailedConnect`, `MaxReconnects`, `ReconnectWait`, `ReconnectHandler`

## Latency Measurement Approaches
### A: Timestamp in payload (recommended — matches existing benchmark)
```go
type BenchMsg struct {
    Timestamp int64  `json:"timestamp"` // UnixNano() or UnixMilli()
    Seq       int64  `json:"seq"`
    Data      string `json:"data"`
}
```
Subscriber: `latency = time.Now().UnixNano() - msg.Timestamp`

### B: Request-Reply round-trip
`nc.Request()` measures RTT. Not suitable for throughput benchmark.

## NATS Performance Numbers (public benchmarks)
| Protocol | Throughput (msg/s) | Latency p99 |
|----------|-------------------|-------------|
| NATS Core | 15-18M | <0.5ms |
| NATS JetStream | 1-3M | 1-5ms |
| WebSocket (raw) | 100K-500K | 1-10ms |
| gRPC stream | 500K-2M | 0.5-3ms |

NATS Core is 10-100x faster than WebSocket, 5-10x faster than gRPC.

## NATS Server Docker
- Image: `nats:2-alpine` or `nats:latest`
- Ports: 4222 (client), 6222 (cluster), 8222 (monitoring)
- Flags: `--js` for JetStream, `--max_payload`, `--write_deadline`
- Monitoring: `http://host:8222/varz` — real-time stats (connections, msgs/s, bytes/s)

## Server Config for Max Throughput
```conf
max_connections: 64K
max_payload: 1MB
write_deadline: "10s"
ping_interval: "2m"
```

## Client Tuning
```go
nc, _ := nats.Connect(url,
    nats.PendingLimits(-1, -1),    // unlimited pending
    nats.ReconnectBufSize(-1),     // unlimited reconnect buffer
    nats.NoEcho(),                 // don't receive own msgs
)
```
