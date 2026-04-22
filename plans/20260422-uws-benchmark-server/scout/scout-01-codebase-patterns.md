# Scout Report: Codebase Patterns

## File Listing

| # | Path | Purpose |
|---|------|---------|
| 1 | `ws-server/server.js` | WebSocket server (ws lib) — Kafka consumer → WS broadcast |
| 2 | `ws-server/package.json` | WS server deps: `ws@^8.18.0`, `kafkajs@^2.2.4` |
| 3 | `ws-server/ecosystem.config.js` | PM2 cluster config for WS server |
| 4 | `grpc-server/server.js` | gRPC server — Kafka consumer → server-streaming with linger batching |
| 5 | `grpc-server/Dockerfile` | Docker image for gRPC server (node:20-bookworm-slim) |
| 6 | `grpc-server/docker-compose.yml` | 3 gRPC containers with bridge networking |
| 7 | `grpc-server/docker-compose.host.yml` | 3 gRPC containers with host networking |
| 8 | `grpc-server/proto/benchmark.proto` | Protobuf service definition |
| 9 | `benchmark-client/go-client/main.go` | Go benchmark client (WS + gRPC connections, HDRHistogram) |
| 10 | `producer/producer-rdkafka.js` | Kafka producer using node-rdkafka |
| 11 | `run-benchmark-1gb.sh` | Full benchmark orchestration script |

---

## WS Server: Kafka → Client Broadcast

**File:** `ws-server/server.js` (99 lines)

### Architecture
- Uses `ws` library (`WebSocketServer`) on top of `http.createServer()`
- Kafka consumer (kafkajs) subscribes to `benchmark-messages` topic
- Each PM2 instance gets its own Kafka consumer group: `ws-benchmark-worker-${NODE_APP_INSTANCE}`
- `clients` is a `Set<WebSocket>` — all connected WS clients per process

### Broadcast Loop (line 40–51)
```
eachBatch → for each ws in clients:
  if ws.readyState !== 1 → skip
  for each kafka message in batch:
    JSON.parse(msg.value)           // validates JSON
    ws.send(msg.value.toString())   // forwards raw Kafka value as-is
    if i % 2000 === 0 → yieldLoop   // setImmediate to avoid blocking
```

### Key Settings
- `maxPayload: 0` — no WS frame size limit
- `YIELD_EVERY = 2000` — yields event loop every 2k messages via `setImmediate`
- `maxBytesPerPartition: 10485760` (10 MB)
- `maxBytes: 52428800` (50 MB total per fetch)
- `maxWaitTimeInMs: 500`
- `sessionTimeout: 30000`
- `fromBeginning: false`
- Dead clients (send errors) are collected and removed after the loop

### Startup / Shutdown
- Sends `"ready"` via `process.send()` for PM2 `wait_ready`
- Graceful: closes all WS, disconnects Kafka consumer
- Consumer auto-retries on error (5s delay)

---

## gRPC Server: Kafka → Server Streaming

**File:** `grpc-server/server.js` (213 lines)

### Architecture
- Uses `@grpc/grpc-js` + `@grpc/proto-loader`
- Kafka consumer feeds a **linger buffer** that flushes to all active gRPC streams
- Each stream is a `StreamMessages` server-streaming RPC

### Linger / Batching (lines 67–118)
- Messages accumulate in `lingerBuffer[]`
- Flush triggers: buffer reaches `BATCH_MAX` (default 500) OR `LINGER_MS` timer (default 5ms)
- On flush: wraps entries into `{ messages: entries }` (proto `StreamResponse`)
- Handles backpressure: if `call.write()` returns false, awaits `"drain"` event
- Only one flush runs at a time (`flushing` flag)

### Kafka Message → Proto Entry Mapping (lines 134–141)
```
entry.timestamp = parsed.timestamp    (from JSON)
entry.seq       = parsed.seq || 0
entry.payload   = raw Kafka bytes
```

### Key Settings
- `LINGER_MS = 5`, `BATCH_MAX = 500`
- Same Kafka consumer config as WS server
- gRPC server tuned for high throughput:
  - `max_receive_message_length`: 100 MB
  - `max_send_message_length`: 100 MB
  - `http2.max_frame_size`: 16 MB
  - `http2.initial_window_size`: 640 MB
  - `http2.initial_connection_window_size`: 1.28 GB
- Stats printed every 5s (batches, in/out rates, streams, buffer)

---

## Protobuf Definition

**File:** `grpc-server/proto/benchmark.proto`

```protobuf
service BenchmarkService {
  rpc StreamMessages(StreamRequest) returns (stream StreamResponse);
}

message StreamRequest { string client_id = 1; }
message MessageEntry { uint64 timestamp = 1; uint64 seq = 2; bytes payload = 3; }
message StreamResponse { repeated MessageEntry messages = 1; }
```

---

## PM2 Config Structure

**File:** `ws-server/ecosystem.config.js`

```javascript
{
  name: "ws-benchmark",
  script: "./server.js",
  instances: 3,                    // 3 cluster workers
  exec_mode: "cluster",           // PM2 cluster mode (shared port)
  max_memory_restart: "16G",
  node_args: "--max-old-space-size=16384",
  env: {
    PORT: 8090,                   // single shared port (cluster mode)
    KAFKA_BROKER: "192.168.0.9:9091",
    KAFKA_TOPIC: "benchmark-messages",
    UV_THREADPOOL_SIZE: "16",
  },
  kill_timeout: 10000,
  listen_timeout: 10000,
  wait_ready: true,               // waits for process.send("ready")
  max_restarts: 10,
  restart_delay: 4000,
}
```

**WS Port:** `8090` (all 3 PM2 instances share via cluster mode)

---

## Docker Compose Patterns

### Bridge Network (`docker-compose.yml`)

| Service | Container | Host Port → Container Port | CONTAINER_ID |
|---------|-----------|---------------------------|-------------|
| grpc-server-1 | grpc-server-1 | 50051 → 50051 | "1" |
| grpc-server-2 | grpc-server-2 | 50052 → 50051 | "2" |
| grpc-server-3 | grpc-server-3 | 50053 → 50051 | "3" |

- Uses default bridge networking (Docker-managed)
- Port mapping required to expose gRPC from container
- Each gets unique `CONTAINER_ID` → unique Kafka consumer group
- No volumes, no custom networks

### Host Network (`docker-compose.host.yml`)

| Service | Container | GRPC_PORT | CONTAINER_ID |
|---------|-----------|-----------|-------------|
| grpc-host-1 | grpc-host-1 | 60051 | "host-1" |
| grpc-host-2 | grpc-host-2 | 60052 | "host-2" |
| grpc-host-3 | grpc-host-3 | 60053 | "host-3" |

- Uses `network_mode: host` — no port mapping needed
- Unique port per container via `GRPC_PORT` env var
- No volumes, no custom networks

### Dockerfile (`grpc-server/Dockerfile`)

```dockerfile
FROM node:20-bookworm-slim
WORKDIR /app
COPY package*.json ./
RUN npm install --omit=dev
COPY server.js .
COPY proto/ ./proto/
ENV GRPC_PORT=50051
ENV CONTAINER_ID=default
CMD ["node", "--max-old-space-size=16384", "server.js"]
```

### Common Environment Variables (both compose files)
- `KAFKA_BROKER: "192.168.0.9:9091"`
- `KAFKA_TOPIC: "benchmark-messages"`
- `CONTAINER_ID` — unique per container
- `GRPC_PORT` — only in host mode (defaults to 50051 in Dockerfile)

---

## Port Summary

| Component | Mode | Port(s) |
|-----------|------|---------|
| WS server (PM2) | Host / cluster | **8090** (shared) |
| gRPC server (bridge) | Docker bridge | **50051**, **50052**, **50053** |
| gRPC server (host) | Docker host | **60051**, **60052**, **60053** |
| Kafka broker | Systemd / host | **9091** (192.168.0.9) |
| Kafka controller | Systemd / host | **9093** (127.0.0.1) |

---

## Go Client: Group Configuration

**File:** `benchmark-client/go-client/main.go`

### Group Struct
```go
type Group struct {
    Name      string     // Display name
    Type      string     // "ws" or "grpc"
    Endpoints []string   // Connection endpoints
}
```

### Default Groups (line 47–51)
```go
groups = []Group{
    {Name: "WS (host/PM2)", Type: "ws",
     Endpoints: []string{"ws://127.0.0.1:8090", "ws://127.0.0.1:8090", "ws://127.0.0.1:8090"}},
    {Name: "gRPC bridge", Type: "grpc",
     Endpoints: []string{"localhost:50051", "localhost:50052", "localhost:50053"}},
    {Name: "gRPC host", Type: "grpc",
     Endpoints: []string{"localhost:60051", "localhost:60052", "localhost:60053"}},
}
```

### Connection Distribution
- `-conns N` flag controls connections per group (default 1)
- Connections round-robin across endpoints: `endpoint = Endpoints[ci % len(Endpoints)]`
- Each connection gets its own `ConnStats` (histogram, counters)

### WS Connect Logic (connectWS, line 69–134)
1. `websocket.DefaultDialer` with compression disabled
2. `DialContext(ctx, endpoint, http.Header{})`
3. Read loop: `conn.ReadMessage()` — blocks for next message
4. On error: reconnect after 2s
5. Measures latency from `msg.Timestamp` (float64 seconds → microseconds)
6. HDRHistogram records latency in microseconds (range: 1–60,000,000)

### gRPC Connect Logic (connectGRPC, line 136–226)
1. `grpc.NewClient()` with large window sizes matching server config
2. `client.StreamMessages(ctx, &StreamRequest{ClientId: ...})`
3. Read loop: `stream.Recv()` — gets `StreamResponse` with batch of `MessageEntry`
4. Processes all entries in batch; records per-entry latency
5. Tracks `rawCount`/`rawBytes` (total received) vs `count`/`bytes` (during measurement)

### Measurement Flow
1. Connect all groups, wait 5s
2. Check active connections, wait 10s more if none
3. Warmup phase (`-warmup` seconds, default 30) — messages received but not measured
4. Measurement phase (`-duration` seconds, default 120) — `measuring` flag set to true
5. Print throughput + latency tables + per-connection breakdown

---

## Producer Message Format

**File:** `producer/producer-rdkafka.js`

### JSON Message
```json
{
  "timestamp": 1713782400000,
  "seq": 12345,
  "data": "xxxxx...xxx"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `timestamp` | number | `Date.now()` — milliseconds since epoch |
| `seq` | number | Monotonically increasing sequence number |
| `data` | string | Padding string to reach target `MSG_SIZE` |

### Sizing
- `MSG_SIZE` default: 1024 bytes
- `PAYLOAD_OVERHEAD` = 40 bytes (JSON overhead for timestamp + seq + keys)
- `PADDING` = `"x".repeat(MSG_SIZE - 40)` = 984 chars
- Total JSON ≈ 1024 bytes per message

### Producer Config
- Uses `node-rdkafka` (C bindings, high performance)
- `batch.size`: 262144 (256 KB)
- `linger.ms`: 20
- `acks`: 0 (fire and forget)
- `compression.codec`: "none"
- `queue.buffering.max.messages`: 500,000
- `message.max.bytes`: 10 MB
- Rate limiting: if current throughput > `TARGET_MBPS`, adds 5ms delay
- Partition routing: `seq % NUM_PARTITIONS` (round-robin, 12 partitions)
- Each producer instance targets `TARGET_MBPS` MB/s (default 1000)

---

## Benchmark Orchestration (`run-benchmark-1gb.sh`)

### Step-by-Step Flow

| Step | Action | Details |
|------|--------|---------|
| 1 | Setup Kafka | Install JDK 17, download Kafka 3.9.2, create `kafka-bench` user, configure KRaft (host: 192.168.0.9:9091), install systemd service |
| 1b | Reset Kafka | Always stop → clean data → reformat KRaft → restart (ensures clean state) |
| 2 | Create topic | `benchmark-messages` — 12 partitions, replication 1, 2min retention, 1GB retention bytes |
| 3 | iptables | Allow Docker containers (172.16.0.0/12) to reach Kafka at 192.168.0.9:9091 |
| 4 | Build + start gRPC | Build Docker images (`--no-cache`), start bridge containers (ports 50051-50053), then host containers (ports 60051-60053) |
| 4 | Start WS (PM2) | `npm install`, `pm2 start ecosystem.config.js` — 3 cluster instances on port 8090 |
| 5 | Health check | Runs `health-check-1gb.sh` |
| 6 | Build Go client | `npm install` deps, `go build` benchmark client |
| 6b | Container readiness | Check Docker logs for "Kafka consumer connected" in all 6 containers |
| 6c | Kafka verify | Produce + consume 1 test message to verify end-to-end |
| 6d | Topic verify | Check `benchmark-messages` topic offsets |
| 7 | Run benchmark | For each run (default 3): start producers → run Go client → stop producers → restart containers |
| 8 | System info | Collect kernel, Node, Docker, PM2 versions, topic info, PM2 metrics, Docker stats, disk info |

### Configuration Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `WARMUP` | 30 | Warmup seconds before measurement |
| `DURATION` | 120 | Measurement duration in seconds |
| `RUNS` | 3 | Number of benchmark iterations |
| `TARGET_MBPS` | 1000 | Target throughput per producer (MB/s) |
| `NUM_PRODUCERS` | 10 | Number of parallel producer processes |
| `CONNS` | 30 | Connections per group in Go client |

### Infrastructure Constants
- Kafka version: 3.9.2, Scala 2.13
- Kafka dir: `/opt/kafka-benchmark`
- Kafka data: `/home/kafka-benchmark/data`
- Kafka user: `kafka-bench`
- Kafka service: `kafka-benchmark` (systemd)
- Results: `<project>/results/`

---

## Key Patterns to Preserve

1. **PM2 cluster mode** — all instances share one port via `NODE_APP_INSTANCE` for unique Kafka group IDs
2. **Kafka consumer group isolation** — each server instance (WS or gRPC) gets a unique group ID, so each receives ALL messages
3. **gRPC linger batching** — 5ms or 500 messages, whichever comes first, with backpressure handling
4. **WS yield every 2k messages** — `setImmediate` to avoid starving event loop
5. **No compression** — both Kafka producer and WS dialer disable compression
6. **Environment-driven config** — all ports, brokers, topics via env vars with sensible defaults
7. **Graceful shutdown** — both servers handle SIGINT/SIGTERM, close connections, disconnect Kafka
8. **Consumer retry** — both servers auto-retry Kafka consumer on failure (5s delay)
