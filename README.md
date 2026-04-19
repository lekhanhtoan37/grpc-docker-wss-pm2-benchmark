# WS vs gRPC Latency Benchmark

So sánh latency p50/p99 giữa 3 nhóm: PM2 WebSocket cluster (host), gRPC Docker containers (bridge network), và gRPC Docker containers (host network). Cả ba đều consume từ cùng một Kafka topic.

## Architecture

```
                        HOST
  ┌──────────────┐    ┌──────────────────────────┐
  │ Kafka Producer│    │  Benchmark Client        │
  │ 100 msg/s 1KB │    │  3 WS + 3 gRPC-bridge   │
  └──────┬───────┘    │    + 3 gRPC-host          │
         │            │  hdr-histogram            │
         ▼            └──┬───┬──┬──┬──┬──┬──┬────┘
   ┌───────────┐         │   │  │  │  │  │  │
   │ Kafka     │         │   │  │  │  │  │  │
   │ :9092     │         │   │  │  │  │  │  │
   └─────┬─────┘         │   │  │  │  │  │  │
         │               │   │  │  │  │  │  │
  ┌──────┴──────┐       │   │  │  │  │  │  │
  │ PM2 WS      │◄──────┘   │  │  │  │  │  │
  │ 3 workers   │           │  │  │  │  │  │
  │ :8080       │           │  │  │  │  │  │
  └─────────────┘           │  │  │  │  │  │
                            │  │  │  │  │  │
  ┌──── Docker (bridge) ────┘  │  │  │  │  │
  │  grpc-net + kafka-net      │  │  │  │  │
  │  ┌─────┐ ┌─────┐ ┌─────┐ │  │  │  │  │
  │  │ctr-1│ │ctr-2│ │ctr-3│ │  │  │  │  │
  │  │:510 │ │:510 │ │:510 │ │  │  │  │  │
  │  └──┬──┘ └──┬──┘ └──┬──┘ │  │  │  │  │
  └─────┴───────┴───────┘    │  │  │  │  │
       :50051 :50052 :50053 ◄─┘  │  │  │
                               │  │  │
  ┌──── Docker (host) ──────────┘  │  │
  │  network_mode: host             │  │
  │  ┌─────┐ ┌─────┐ ┌─────┐      │  │
  │  │host1│ │host2│ │host3│      │  │
  │  │60051│ │60052│ │60053│      │  │
  │  └─────┘ └─────┘ └─────┘      │  │
  └────────────────────────────────┘  │
       :60051 :60052 :60053 ◄─────────┘
```

## Quick Start

```bash
# Chạy full benchmark (tự động start Kafka, gRPC, WS, producer)
./run-benchmark.sh
```

Script tự động:
1. Start Kafka + Zookeeper (Docker)
2. Tạo topic `benchmark-messages` (1 partition)
3. Start 3 gRPC containers (bridge network)
4. Start 3 gRPC containers (host network)
5. Start 3 PM2 WS workers (cluster mode)
6. Health check tất cả endpoints
7. Chạy 3 lần benchmark (60s warmup + 5min đo mỗi lần)
8. Thu thập kết quả vào `results/`

## Manual Step-by-Step

```bash
# 1. Start Kafka
cd infra && docker compose up -d
sleep 15
docker exec benchmark-kafka kafka-topics --create \
  --topic benchmark-messages --partitions 1 --replication-factor 1 \
  --if-not-exists --bootstrap-server localhost:9092

# 2. Start gRPC servers (bridge)
cd grpc-server && docker compose up -d --build
sleep 10

# 2b. Start gRPC servers (host network)
cd grpc-server && docker compose -f docker-compose.host.yml up -d --build
sleep 5

# 3. Start WS servers
cd ws-server && npm install && pm2 start ecosystem.config.js
sleep 5

# 4. Start producer (background)
cd producer && npm install && KAFKAJS_NO_PARTITIONER_WARNING=1 node producer.js &

# 5. Run benchmark client
cd benchmark-client && npm install
node client.js --warmup 60 --duration 300

# 6. Cleanup
pm2 delete ws-benchmark
cd ../grpc-server && docker compose -f docker-compose.host.yml down
cd ../grpc-server && docker compose down
cd ../infra && docker compose down
```

## Project Structure

```
├── infra/                   # Kafka + Zookeeper (Docker Compose)
│   └── docker-compose.yml
├── proto/                   # Shared gRPC proto
│   └── benchmark.proto
├── producer/                # Kafka producer (100 msg/s, 1KB JSON)
│   ├── package.json
│   └── producer.js
├── ws-server/               # PM2 WebSocket cluster (3 workers)
│   ├── package.json
│   ├── server.js
│   └── ecosystem.config.js
├── grpc-server/             # gRPC Docker containers (3x bridge + 3x host)
│   ├── package.json
│   ├── server.js
│   ├── Dockerfile
│   ├── docker-compose.yml
│   └── docker-compose.host.yml
├── benchmark-client/        # Benchmark client (9 connections, hdr-histogram)
│   ├── package.json
│   ├── client.js
│   └── proto/
│       └── benchmark.proto
├── results/                 # Benchmark output logs
├── health-check.sh          # Verify all services running
└── run-benchmark.sh         # One-command benchmark runner
```

## Approach

**Approach B: Unique Consumer Groups** — Mỗi consumer (3 WS workers + 3 gRPC bridge + 3 gRPC host) dùng unique consumer group trên 1 partition Kafka. Mỗi consumer nhận tất cả messages. So sánh latency cho cùng một message giữa 3 nhóm.

## Network Mode Comparison

Benchmark so sánh 3 deployment modes:

| Mode | Runtime | Network | Purpose |
|------|---------|---------|---------|
| WS (host/PM2) | PM2 cluster | Host | Baseline - no Docker overhead |
| gRPC bridge | Docker | Bridge + port mapping | Standard Docker deployment |
| gRPC host | Docker | Host (`network_mode: host`) | Zero Docker network overhead |

### macOS Limitation

Trên macOS Docker Desktop, `network_mode: host` chia sẻ Linux VM network, không phải macOS host network. VM boundary (~0.3-1ms overhead) che mất lợi ích của host networking. Để có kết quả chính xác cho production, chạy trên Linux.

## Benchmark Results

**Environment**: macOS, Docker Desktop, Node v22.13.0, 100 msg/s, ~1KB JSON, 60s warmup + 300s measurement

```
╔══════════╦══════════════╦══════════════╦════════════╗
║ Pctl     ║ WS (ms)      ║ gRPC (ms)    ║ Delta (ms) ║
╠══════════╬══════════════╬══════════════╬════════════╣
║      p50 ║        0.001 ║        0.002 ║     +0.001 ║
║      p75 ║        0.002 ║        0.003 ║     +0.001 ║
║      p90 ║        0.003 ║        0.004 ║     +0.001 ║
║      p95 ║        0.003 ║        0.004 ║     +0.001 ║
║      p99 ║        0.005 ║        0.006 ║     +0.001 ║
║    p99.9 ║        0.016 ║        0.016 ║     -0.000 ║
╚══════════╩══════════════╩══════════════╩════════════╝

Per-endpoint breakdown:
  WS #1:    29624 msgs, p50=0.001 p75=0.002 p90=0.003 p95=0.003 p99=0.005 p99.9=0.016
  WS #2:    29624 msgs, p50=0.001 p75=0.002 p90=0.003 p95=0.003 p99=0.005 p99.9=0.016
  WS #3:    29624 msgs, p50=0.001 p75=0.002 p90=0.003 p95=0.003 p99=0.005 p99.9=0.016
  gRPC #1:  29624 msgs, p50=0.002 p75=0.003 p90=0.004 p95=0.004 p99=0.006 p99.9=0.015
  gRPC #2:  29624 msgs, p50=0.002 p75=0.003 p90=0.004 p95=0.004 p99=0.006 p99.9=0.016
  gRPC #3:  29624 msgs, p50=0.002 p75=0.003 p90=0.004 p95=0.004 p99=0.006 p99.9=0.016

Event loop lag: p50=0.00ms, p99=0.00ms, max=0.00ms
Total messages: 177744
```

*(Kết quả cũ 2 nhóm. Chạy lại benchmark với `./run-benchmark.sh` để có kết quả 3 nhóm mới.)*

**Key findings**:
- gRPC (Docker bridge) chậm hơn WS (host) khoảng **+0.001ms** ở mọi percentile
- gRPC (Docker host) kết quả phụ thuộc platform — trên macOS ≈ bridge do VM overhead
- Ở p99.9, cả hai gần như bằng nhau (~0.016ms)
- Docker bridge network overhead rất nhỏ ở workload thấp (100 msg/s, 1KB)

## Tech Stack

| Component | Tech |
|-----------|------|
| WS server | `ws` ^8.x |
| gRPC server | `@grpc/grpc-js` ^1.12 |
| Kafka client | `kafkajs` ^2.x |
| Histogram | `hdr-histogram-js` ^3.x |
| Process manager | PM2 ^5.x |
| Containers | Docker Compose v2 |
| Node.js | 20 LTS |
