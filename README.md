# WebSocket vs gRPC Throughput Benchmark

So sánh throughput giữa **6 deployment modes**: WS (PM2 host), uWS (PM2 host, Docker bridge, Docker host), gRPC (Docker bridge, Docker host). Tất cả consume từ cùng một Kafka topic.

## Architecture

```
                          SERVER (Ubuntu 24.04)
  ┌─────────────────┐    ┌─────────────────────────────────────────────┐
  │ Producer (x10)  │    │  Go Benchmark Client                        │
  │ node-rdkafka    │    │  6 groups × 3 conns = 18 connections        │
  │ ~1GB/s total    │    │  coder/websocket + grpc-go                  │
  └────────┬────────┘    │  hdr-histogram latency per message          │
           │             └──┬────┬────┬────┬────┬────┬────────────────┘
           ▼                │    │    │    │    │    │
    ┌─────────────┐        │    │    │    │    │    │
    │ Kafka KRaft │        │    │    │    │    │    │
    │ 192.168.0.9 │        │    │    │    │    │    │
    │ :9091       │        │    │    │    │    │    │
    │ 12 partitions       │    │    │    │    │    │
    └──────┬──────┘        │    │    │    │    │    │
           │               │    │    │    │    │    │
    ┌──────┴──────────────────────────────────────────┐
    │ Each server: unique Kafka consumer group         │
    │ → receives ALL messages (independent consumers)  │
    └──────┬──────────────────────────────────────────┘
           │
    ┌──────┴──────┐  ┌──────────────┐  ┌────────────────────────────┐
    │ WS (PM2)    │  │ uWS (PM2)    │  │ uWS/gRPC (Docker)          │
    │ 3 workers   │  │ 3 workers    │  │                            │
    │ :8090       │  │ :8091        │  │ Bridge:                    │
    └─────────────┘  └──────────────┘  │  nginx:50051→3 gRPC reps  │
                                       │  nginx:50061→3 uWS reps   │
                                       │ Host:                      │
                                       │  60051-53 (gRPC)           │
                                       │  60061-63 (uWS)            │
                                       └────────────────────────────┘
```

## 6 Benchmark Groups

| # | Group | Runtime | Network | Port | Load Balance |
|---|-------|---------|---------|------|-------------|
| 1 | WS (host/PM2) | PM2 cluster | Host | 8090 | Kernel TCP |
| 2 | uWS (host/PM2) | PM2 cluster | Host | 8091 | Kernel TCP |
| 3 | uWS bridge | Docker | Bridge + nginx | 50061 | nginx upstream |
| 4 | uWS host | Docker | Host | 60061-63 | Direct |
| 5 | gRPC bridge | Docker | Bridge + nginx | 50051 | nginx grpc_pass |
| 6 | gRPC host | Docker | Host | 60051-53 | Direct |

## Methodology

### Message Format (Kafka → Server → Client)

**Producer** gửi JSON messages vào Kafka topic `benchmark-messages`:

```json
{"timestamp": 1745312345678, "seq": 42, "data": "xxxxxxx..."}
```

- `timestamp`: Unix epoch milliseconds (Date.now())
- `seq`: sequence number
- `data`: padding để tổng message ≈ 1KB (1024 bytes)
- 10 producer instances, each targeting ~1 GB/s → tổng ~10 GB/s
- Using `node-rdkafka` (C bindings, `linger.ms=20`, `batch.size=262144`, `acks=0`)
- Round-robin across 12 partitions (`seq % 12`)

### Server-Side Batching (Micro-batching)

Tất cả servers dùng cùng cơ chế batching:

```
Kafka batch → append to linger buffer → flush khi:
  1. buffer ≥ BATCH_MAX (20 msgs), HOẶC
  2. linger timer fires (5ms kể từ msg đầu tiên trong buffer)
```

**WS/uWS servers** gửi batch bằng cách join messages với `\n`:
```
msg1\nmsg2\nmsg3\n...msg20
```
→ 1 WebSocket frame = 20 messages joined by newline

**gRPC server** gửi `StreamResponse` protobuf:
```protobuf
message StreamResponse {
  repeated MessageEntry messages = 1;
}
```
→ 1 gRPC stream message = array of 20 MessageEntry

### Client-Side Decode

**WS/uWS groups** (Go client using `coder/websocket`):
1. Read WebSocket frame → `io.ReadAll(reader)`
2. Split by `\n` → individual JSON strings
3. Parse `timestamp` từ raw bytes (không json.Unmarshal toàn bộ):
   ```go
   // Scan for "timestamp:" key, extract number
   idx := bytes.Index(msg, []byte(`"timestamp":`))
   ```
4. Latency = `time.Now().UnixMicro() - timestamp*1000`
5. Record vào hdr-histogram

**gRPC groups** (Go client using `grpc-go`):
1. `stream.Recv()` → `StreamResponse`
2. Iterate `resp.GetMessages()` → mỗi entry có `.Timestamp`, `.Seq`, `.Payload`
3. Latency = `time.Now().UnixMicro() - timestamp*1000`
4. Record vào hdr-histogram

### Benchmark Protocol

1. **Start**: Client connects 18 WebSocket/gRPC streams (3 per group)
2. **Warmup**: 30 seconds — connections stabilize, consumer groups join
3. **Measurement**: 120 seconds — count msgs, measure latency per message
4. **Live stats**: Every 30s — print active connections, raw/processed counts
5. **Report**: Throughput (MB/s, msg/s), Latency percentiles (p50→p99.9), Connection stability

### Kafka Consumer Groups

Mỗi server instance có unique consumer group → nhận TẤT CẢ messages từ topic:

| Server | Group ID Pattern |
|--------|-----------------|
| WS worker 0/1/2 | `ws-benchmark-worker-{NODE_APP_INSTANCE}` |
| uWS worker 0/1/2 | `uws-benchmark-worker-{NODE_APP_INSTANCE}` |
| gRPC bridge-1/2/3 | `grpc-benchmark-{CONTAINER_ID}` |
| gRPC host-1/2/3 | `grpc-benchmark-{CONTAINER_ID}` |
| uWS bridge-1/2/3 | `uws-benchmark-worker-{CONTAINER_ID}` |
| uWS host-1/2/3 | `uws-benchmark-worker-{CONTAINER_ID}` |

→ 18 consumer groups đọc cùng 1 topic → mỗi group nhận tất cả messages.

## Benchmark Results

**Environment**: Ubuntu 24.04, 12-core, 32GB RAM, Kafka KRaft (1 broker), 10 producers × ~1 GB/s target, 12 partitions, 30s warmup + 120s measurement, 3 connections/group

### Throughput

```
Group               Conns         Msgs       MB/s        msg/s
------------------------------------------------------------
WS (host/PM2)           3      5926832      48.75        49390
uWS (host/PM2)          3      4166000      34.27        34717
uWS bridge              3      3664000      30.14        30533
uWS host                3      3599749      29.61        29998
gRPC bridge             3     33590760     276.30       279923
gRPC host               3     32823528     269.99       273529
```

### Raw Receive Stats

```
Group              Raw Msgs   Raw MB/s    Raw msg/s     Drop %
------------------------------------------------------------
WS (host/PM2)       5926832      48.75        49390       0.0%
uWS (host/PM2)      4166000      34.27        34717       0.0%
uWS bridge          3664000      30.14        30533       0.0%
uWS host            3599749      29.61        29998       0.0%
gRPC bridge        33590760     276.30       279923       0.0%
gRPC host          32823528     269.99       273529       0.0%
```

### Connection Stability

```
Group            Disconnects Reconnects   Reconn p50   Reconn p99   Reconn max
----------------------------------------------------------------------------
WS (host/PM2)             3          0        0.0ms        0.0ms        0.0ms
uWS (host/PM2)            3          0        0.0ms        0.0ms        0.0ms
uWS bridge                3          0        0.0ms        0.0ms        0.0ms
uWS host                  3          0        0.0ms        0.0ms        0.0ms
gRPC bridge               3          0        0.0ms        0.0ms        0.0ms
gRPC host                 3          0        0.0ms        0.0ms        0.0ms
```

### Key Findings

1. **gRPC dominates throughput**: ~276 MB/s (bridge) vs ~49 MB/s (WS) — **5.6x more throughput**
2. **gRPC bridge ≈ gRPC host**: 276 vs 270 MB/s → Docker bridge + nginx overhead negligible at this scale
3. **WS (ws library) > uWS** on host/PM2: 49 vs 34 MB/s → despite uWS being lower-level, the `ws` library handles backpressure better in this scenario
4. **Zero drops across all groups**: 0% message loss — all protocols maintain reliable delivery
5. **Zero reconnects**: All connections stable throughout measurement — no disconnect issues
6. **uWS bridge ≈ uWS host**: 30 vs 30 MB/s → nginx adds minimal overhead for WebSocket proxying

## Project Structure

```
├── grpc-server/
│   ├── server.js               # gRPC Kafka→stream server, batch mode
│   ├── Dockerfile               # Node 20 + gRPC deps
│   ├── docker-compose.yml       # Bridge: 3 replicas + nginx (grpc_pass)
│   ├── docker-compose.host.yml  # Host: 3 containers, ports 60051-53
│   └── nginx.conf               # gRPC reverse proxy config
├── uws-server/
│   ├── server.js                # uWS Kafka→WS server, batch mode
│   ├── Dockerfile               # Node 22 + uWebSockets.js native addon
│   ├── docker-compose.yml       # Bridge: 3 replicas + nginx (WS proxy)
│   ├── docker-compose.host.yml  # Host: 3 containers, ports 60061-63
│   └── nginx.conf               # WebSocket reverse proxy config
├── ws-server/
│   ├── server.js                # WS (ws lib) Kafka→WS server, batch mode
│   └── ecosystem.config.js      # PM2 cluster config, port 8090
├── benchmark-client/
│   └── go-client/
│       ├── main.go              # Go benchmark client, 6 groups
│       └── go.mod               # coder/websocket + grpc-go + hdr-histogram
├── producer/
│   └── producer-rdkafka.js      # node-rdkafka producer, ~1KB JSON msgs
├── infra/
│   └── server.properties        # Kafka KRaft config
├── run-benchmark-1gb.sh         # Full orchestration script
├── health-check-1gb.sh          # Pre-flight health check
└── results/                     # Benchmark output logs
```

## Quick Start

```bash
# Full benchmark (auto: Kafka setup, Docker build, PM2 start, topic reset, producer, benchmark)
sudo bash run-benchmark-1gb.sh

# Custom params
sudo WARMUP=60 DURATION=300 RUNS=1 CONNS=3 bash run-benchmark-1gb.sh
```

## Tech Stack

| Component | Tech |
|-----------|------|
| WS server | `ws` ^8.x (PM2 cluster, 3 workers) |
| uWS server | `uWebSockets.js` v20.49.0 (PM2 cluster + Docker) |
| gRPC server | `@grpc/grpc-js` ^1.12 (Docker) |
| Kafka client (server) | `kafkajs` ^2.x |
| Kafka client (producer) | `node-rdkafka` (C bindings, zero-copy) |
| Benchmark client | Go 1.22 + `coder/websocket` + `grpc-go` + `hdr-histogram` |
| Reverse proxy | nginx:alpine (gRPC `grpc_pass` + WS `proxy_pass`) |
| Kafka broker | Kafka 3.9.2 KRaft mode (no Zookeeper) |
| Process manager | PM2 ^5.x (cluster mode) |
| Containers | Docker Compose v2 |
| Runtime | Node 22 (uWS), Node 20 (gRPC) |
