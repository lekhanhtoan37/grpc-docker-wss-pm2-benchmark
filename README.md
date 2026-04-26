# WebSocket vs gRPC Throughput Benchmark

So sánh throughput giữa **9 deployment modes**: WS (PM2 host), uWS (PM2 host, Docker bridge, Docker host), Go WS (PM2 host, Docker bridge, Docker host), gRPC (Docker bridge, Docker host). Tất cả consume từ cùng một Kafka topic.

## Architecture

```
                          SERVER (Ubuntu 24.04)
  ┌─────────────────┐    ┌─────────────────────────────────────────────┐
  │ Producer (x10)  │    │  Go Benchmark Client                        │
  │ node-rdkafka    │    │  9 groups × N conns                         │
  │ ~1GB/s total    │    │  coder/websocket + grpc-go                  │
  └────────┬────────┘    │  hdr-histogram latency per message          │
           │             └──┬────┬────┬────┬────┬────┬────┬────┬──────┘
           ▼                │    │    │    │    │    │    │    │
    ┌─────────────┐        │    │    │    │    │    │    │    │
    │ Kafka KRaft │        │    │    │    │    │    │    │    │
    │ 192.168.0.9 │        │    │    │    │    │    │    │    │
    │ :9091       │        │    │    │    │    │    │    │    │
    │ 12 partitions       │    │    │    │    │    │    │    │
    └──────┬──────┘        │    │    │    │    │    │    │    │
           │               │    │    │    │    │    │    │    │
    ┌──────┴──────────────────────────────────────────────────────┐
    │ Each server: unique Kafka consumer group per instance       │
    │ → receives ALL messages (independent consumers)             │
    └──────┬─────────────────────────────────────────────────────┘
           │
    ┌──────┴──────┐  ┌──────────────┐  ┌────────────────────────────┐  ┌────────────────┐
    │ WS (PM2)    │  │ uWS (PM2)    │  │ uWS/gRPC/GoWS (Docker)    │  │ Go WS (PM2)    │
    │ 3 workers   │  │ 3 workers    │  │                            │  │ 3 workers      │
    │ :8090       │  │ :8091        │  │ Bridge:                    │  │ :8092-94      │
    └─────────────┘  └──────────────┘  │  nginx:50051→3 gRPC reps  │  └────────────────┘
                                       │  nginx:50061→3 uWS reps   │
                                       │  nginx:50071→3 GoWS reps  │
                                       │ Host:                      │
                                       │  60051-53 (gRPC)           │
                                       │  60061-63 (uWS)            │
                                       │  60071-73 (Go WS)          │
                                       └────────────────────────────┘
```

## 9 Benchmark Groups

| # | Group | Runtime | Network | Port | Load Balance |
|---|-------|---------|---------|------|-------------|
| 1 | WS (host/PM2) | PM2 cluster | Host | 8090 | Kernel TCP |
| 2 | uWS (host/PM2) | PM2 cluster | Host | 8091 | Kernel TCP |
| 3 | Go WS (host/PM2) | PM2 fork (Go binary) | Host | 8092-94 | Direct |
| 4 | uWS bridge | Docker | Bridge + nginx | 50061 | nginx upstream |
| 5 | Go WS bridge | Docker | Bridge + nginx | 50071 | nginx upstream |
| 6 | uWS host | Docker | Host | 60061-63 | Direct |
| 7 | Go WS host | Docker | Host | 60071-73 | Direct |
| 8 | gRPC bridge | Docker | Bridge + nginx | 50051 | nginx grpc_pass |
| 9 | gRPC host | Docker | Host | 60051-53 | Direct |

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

1. **Start**: Client connects WebSocket/gRPC streams (N per group × 9 groups)
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
| Go WS worker 0/1/2 | `go-ws-benchmark-worker-{NODE_APP_INSTANCE}` |
| gRPC bridge-1/2/3 | `grpc-benchmark-{CONTAINER_ID}` |
| gRPC host-1/2/3 | `grpc-benchmark-{CONTAINER_ID}` |
| uWS bridge-1/2/3 | `uws-benchmark-worker-{CONTAINER_ID}` |
| uWS host-1/2/3 | `uws-benchmark-worker-{CONTAINER_ID}` |
| Go WS bridge-1/2/3 | `go-ws-benchmark-{CONTAINER_ID}` |
| Go WS host-1/2/3 | `go-ws-benchmark-{CONTAINER_ID}` |

→ 27 consumer groups đọc cùng 1 topic → mỗi group nhận tất cả messages.

## Benchmark Results

**Environment**: Ubuntu 24.04, 12-core, 32GB RAM, Kafka KRaft (1 broker), 10 producers × ~1 GB/s target, 12 partitions, 30s warmup + 120s measurement

### Throughput Summary

| Group | 30 conns MB/s | 90 conns MB/s | 30c vs WS | 90c vs WS |
|-------|--------------|--------------|-----------|-----------|
| WS (host/PM2) | 335.09 | 328.31 | baseline | baseline |
| uWS (host/PM2) | 302.31 | 296.18 | -9.8% | -9.8% |
| **Go WS (host/PM2)** | **431.84** | **437.16** | **+28.9%** | **+33.2%** |
| uWS bridge | 306.06 | 343.60 | -8.7% | +4.7% |
| Go WS bridge | 393.72 | 428.68 | +17.5% | +30.6% |
| uWS host | 338.93 | 301.21 | +1.1% | -8.3% |
| Go WS host | 401.07 | 402.11 | +19.7% | +22.5% |
| gRPC bridge | 140.57 | 123.61 | -58.1% | -62.3% |
| gRPC host | 130.96 | 23.63 | -60.9% | -92.8% |

### 30 Connections/Group (270 total)

#### Throughput

```
Group               Conns         Msgs       MB/s        msg/s
------------------------------------------------------------
WS (host/PM2)          30     40747639     335.09       339490
uWS (host/PM2)         30     36760445     302.31       306271
Go WS (host/PM2)       30     52491319     431.84       437333
uWS bridge             30     37216864     306.06       310073
Go WS bridge           30     47847007     393.72       398639
uWS host               30     41213967     338.93       343375
Go WS host             30     48738075     401.07       406063
gRPC bridge            30     17109949     140.57       142552
gRPC host              30     15940024     130.96       132805
```

#### vs WS Throughput

```
uWS (host/PM2)    -9.8%
Go WS (host/PM2) +28.9%  ← FASTEST
uWS bridge        -8.7%
Go WS bridge     +17.5%
uWS host          +1.1%
Go WS host       +19.7%
gRPC bridge      -58.1%
gRPC host        -60.9%
```

### 90 Connections/Group (810 total)

#### Throughput

```
Group               Conns         Msgs       MB/s        msg/s
------------------------------------------------------------
WS (host/PM2)          90     39876965     328.31       332291
uWS (host/PM2)         90     35979637     296.18       299815
Go WS (host/PM2)       90     53109682     437.16       442559
uWS bridge             90     41747672     343.60       347880
Go WS bridge           90     52068069     428.68       433879
uWS host               90     36600608     301.21       304990
Go WS host             90     48841174     402.11       406989
gRPC bridge            90     15036562     123.61       125298
gRPC host              90      2874782      23.63        23955
```

#### Raw Receive Stats

```
Group              Raw Msgs   Raw MB/s    Raw msg/s     Drop %
------------------------------------------------------------
WS (host/PM2)      39876965     328.31       332291       0.0%
uWS (host/PM2)     35979637     296.18       299815       0.0%
Go WS (host/PM2)   53109682     437.16       442559       0.0%
uWS bridge         41747672     343.60       347880       0.0%
Go WS bridge       52068069     428.68       433879       0.0%
uWS host           36600608     301.21       304990       0.0%
Go WS host         48841174     402.11       406989       0.0%
gRPC bridge        15036562     123.61       125298       0.0%
gRPC host           2874782      23.63        23955       0.0%
```

#### vs WS Throughput

```
uWS (host/PM2)    -9.8%
Go WS (host/PM2) +33.2%  ← FASTEST
uWS bridge        +4.7%
Go WS bridge     +30.6%
uWS host          -8.3%
Go WS host       +22.5%
gRPC bridge      -62.3%
gRPC host        -92.8%
```

#### Latency Percentiles (milliseconds)

```
Pctl        WS(host)   uWS(host)  GoWS(host)  uWSbridge  GoWSbridge  uWShost   GoWShost  gRPCbridge gRPChost
----------------------------------------------------------------------------------------------------------------
p50           246.3      257.9      142.0       227.4      183.1       262.0     172.8     256.0      254.7
p75           269.7      288.9      158.2       271.8      200.7       293.3     189.4     284.4      259.7
p90           292.0      305.9      172.2       288.9      215.4       310.1     202.8     306.4      264.0
p95           309.3      312.5      180.0       296.7      223.0       317.2     210.2     312.2      267.8
p99           339.0      326.6      193.3       307.2      236.6       331.1     223.6     316.7      330.0
p99.9         353.6      339.7      206.4       323.0      247.6       350.7     234.1     320.1      368.1
```

#### Latency Delta vs WS (host/PM2) (milliseconds)

```
Pctl       uWS(host)  GoWS(host)  uWSbridge  GoWSbridge  uWShost  GoWShost  gRPCbridge  gRPChost
------------------------------------------------------------------------------------------------------
p50          +11.7     -104.3       -18.9      -63.2      +15.7    -73.5       +9.7      +8.4
p75          +19.1     -111.5        +2.1      -69.1      +23.6    -80.3      +14.7    -10.1
p90          +13.9     -119.8        -3.1      -76.7      +18.1    -89.3      +14.4    -28.0
p95           +3.1     -129.4       -12.6      -86.4       +7.9    -99.1       +2.9    -41.5
p99          -12.3     -145.6       -31.7     -102.4       -7.9   -115.3      -22.3     -8.9
p99.9        -13.9     -147.2       -30.7     -106.0       -2.9   -119.5      -33.6     +14.4
```

### Scenario 1: 3 Connections/Group (27 total) — Previous Results

#### Throughput

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

#### vs WS Throughput

```
uWS (host/PM2)   -29.7%
uWS bridge       -38.2%
uWS host         -39.3%
gRPC bridge      +466.8%
gRPC host        +453.8%
```

#### Connection Stability

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

### Scenario 2: 90 Connections/Group (540 total) — Previous Results (6 groups)

#### Throughput

```
Group               Conns         Msgs       MB/s        msg/s
------------------------------------------------------------
WS (host/PM2)          90     66338667     545.81       552437
uWS (host/PM2)         90     64789091     533.06       539532
uWS bridge             90     73890399     607.94       615324
uWS host               90     65388333     537.99       544523
gRPC bridge            90     40517063     333.04       337407
gRPC host              90     54442342     447.50       453370
```

#### vs WS Throughput

```
uWS (host/PM2)    -2.3%
uWS bridge       +11.4%
uWS host          -1.4%
gRPC bridge      -39.0%
gRPC host        -18.0%
```

### Key Findings

#### Go WS Dominates at High Concurrency (90 conns)

1. **Go WS is the fastest**: 437 MB/s (host/PM2), 429 MB/s (bridge), 402 MB/s (host) — **+22-33% vs Node.js WS**
2. **Go WS lowest latency**: p50 = 142ms (host/PM2) vs WS 246ms — **42% lower latency**
3. **Go WS bridge ≈ Go WS host/PM2**: 429 vs 437 MB/s → Docker bridge + nginx adds only ~2% overhead
4. **Zero drops**: All Go WS groups maintain 0% message loss

#### WS/uWS Comparison

1. **uWS bridge edges WS**: 344 vs 328 MB/s (+4.7%)
2. **uWS host/PM2 slightly slower than WS**: 296 vs 328 MB/s (-9.8%) — possible PM2 cluster mode overhead
3. **Both WS and uWS maintain 0% drop rate** at 810+ total connections

#### gRPC Doesn't Scale

1. **gRPC bridge**: 124 MB/s — **-62% vs WS**, severely bottlenecked
2. **gRPC host**: 24 MB/s — **-93% vs WS**, catastrophic at 90 connections
3. **gRPC latency competitive with WS** (256ms p50) but throughput collapses — HTTP/2 framing overhead
4. At 3 conns, gRPC was 5.6x faster than WS; at 90 conns, gRPC is 2.7x slower — **scales poorly**

#### Architecture Impact

1. **PM2 fork (Go) > PM2 cluster (Node)**: Go binary runs as fork mode → no cluster IPC overhead
2. **Docker bridge + nginx adds minimal overhead**: Go WS bridge 429 vs host 402 MB/s (bridge is actually faster!)
3. **Language/runtime matters more than deployment mode**: Go WS in any mode beats all Node.js modes

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
├── go-ws-server/
│   ├── main.go                  # Go WS Kafka→WS server (IBM sarama + nhooyr/websocket)
│   ├── go.mod                   # Go 1.22, IBM sarama v1.45.1
│   ├── Dockerfile               # golang:1.23 multi-stage build
│   ├── docker-compose.yml       # Bridge: 3 replicas + nginx (WS proxy)
│   ├── docker-compose.host.yml  # Host: 3 containers, ports 60071-73
│   ├── nginx.conf               # WebSocket reverse proxy config
│   └── ecosystem.config.js      # PM2 fork mode, 3 instances, ports 8092-94
├── ws-server/
│   ├── server.js                # WS (ws lib) Kafka→WS server, batch mode
│   └── ecosystem.config.js      # PM2 cluster config, port 8090
├── benchmark-client/
│   └── go-client/
│       ├── main.go              # Go benchmark client, 9 groups
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
| Go WS server | Go 1.22 + `IBM/sarama` v1.45.1 + `nhooyr.io/websocket` (PM2 fork + Docker) |
| gRPC server | `@grpc/grpc-js` ^1.12 (Docker) |
| Kafka client (WS/uWS/gRPC) | `kafkajs` ^2.x |
| Kafka client (Go WS) | `IBM/sarama` v1.45.1 |
| Kafka client (producer) | `node-rdkafka` (C bindings, zero-copy) |
| Benchmark client | Go 1.22 + `coder/websocket` + `grpc-go` + `hdr-histogram` |
| Reverse proxy | nginx:alpine (gRPC `grpc_pass` + WS `proxy_pass`) |
| Kafka broker | Kafka 3.9.2 KRaft mode (no Zookeeper) |
| Process manager | PM2 ^5.x (cluster mode for Node, fork mode for Go) |
| Containers | Docker Compose v2 |
| Runtime | Node 22 (uWS), Node 20 (gRPC) |
