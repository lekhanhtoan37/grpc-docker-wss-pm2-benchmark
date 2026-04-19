# Scout Report: grpc-docker-wss-pm2 Benchmark Project

**Scouted:** 2026-04-19  
**Repository:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2`  
**Purpose:** Benchmark comparing WebSocket (PM2 cluster, host network) vs gRPC (Docker containers, bridge network) end-to-end latency via Kafka message consumption.

---

## 1. gRPC Server Setup

### 1.1 grpc-server/server.js
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/grpc-server/server.js`
- **Description:** gRPC server that consumes from Kafka and streams messages to connected gRPC clients. Runs inside Docker containers (3 instances).
- **Key configuration:**
  - Proto path: `/app/proto/benchmark.proto` (inside container)
  - Kafka broker: `host.docker.internal:9092` (default, overridden to `benchmark-kafka:29092` via docker-compose)
  - Kafka topic: `benchmark-messages`
  - Kafka consumer group: `grpc-benchmark-{CONTAINER_ID}`
  - gRPC listen: `0.0.0.0:50051`
  - Uses `@grpc/grpc-js` and `@grpc/proto-loader`
  - Streams messages to all active gRPC client connections via `call.write()`
  - Graceful shutdown on SIGINT/SIGTERM

### 1.2 grpc-server/Dockerfile
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/grpc-server/Dockerfile`
- **Description:** Docker image definition for gRPC server containers.
- **Key configuration:**
  - Base image: `node:20-alpine`
  - Working dir: `/app`
  - Installs production dependencies only (`npm ci --omit=dev`)
  - Copies `server.js` and `proto/` directory
  - Exposes port `50051`
  - Env var: `CONTAINER_ID=default`
  - CMD: `node server.js`

### 1.3 grpc-server/docker-compose.yml
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/grpc-server/docker-compose.yml`
- **Description:** Defines 3 gRPC server containers with bridge networking, each on separate networks for gRPC and Kafka communication.
- **Key configuration:**
  - **grpc-server-1:** port `50051:50051`, CONTAINER_ID="1"
  - **grpc-server-2:** port `50052:50051`, CONTAINER_ID="2"
  - **grpc-server-3:** port `50053:50051`, CONTAINER_ID="3"
  - All containers use two networks:
    - `grpc-net` — bridge driver (for inter-container gRPC communication)
    - `kafka-net` — external network `infra_default` (to reach Kafka container)
  - Kafka broker env: `benchmark-kafka:29092` (internal Docker network address)
  - Topic: `benchmark-messages`

### 1.4 grpc-server/package.json
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/grpc-server/package.json`
- **Description:** npm dependencies for gRPC server.
- **Dependencies:** `@grpc/grpc-js ^1.12.0`, `@grpc/proto-loader ^0.7.13`, `kafkajs ^2.2.4`

### 1.5 grpc-server/proto/benchmark.proto
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/grpc-server/proto/benchmark.proto`
- **Description:** Copy of benchmark proto used inside Docker container at build time. Identical to the root `proto/benchmark.proto`.

---

## 2. Proto Definition

### proto/benchmark.proto (canonical)
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/proto/benchmark.proto`
- **Description:** gRPC service definition for the benchmark streaming service.
- **Structure:**
  ```
  syntax = "proto3";
  package benchmark;

  service BenchmarkService {
    rpc StreamMessages(StreamRequest) returns (stream StreamResponse);
  }

  message StreamRequest {
    string client_id = 1;
  }

  message StreamResponse {
    uint64 timestamp = 1;   // Producer timestamp (Date.now())
    uint64 seq = 2;         // Sequence number
    string payload = 3;     // Full JSON payload string
  }
  ```
- **RPC type:** Server-streaming RPC (one request, continuous stream of responses)
- **Copies exist at:**
  - `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/proto/benchmark.proto` (canonical)
  - `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/grpc-server/proto/benchmark.proto` (Docker build copy)
  - `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/benchmark-client/proto/benchmark.proto` (client copy)
- All three are **identical** in content.

---

## 3. Benchmark Client

### 3.1 benchmark-client/client.js
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/benchmark-client/client.js`
- **Description:** The main benchmark client that connects to both WS and gRPC servers, measures end-to-end latency using hdr-histogram, and outputs comparison tables.
- **Key configuration:**
  - **WS Endpoints:** `ws://localhost:8080` (x3, all same port via PM2 cluster)
  - **gRPC Endpoints:** `localhost:50051`, `localhost:50052`, `localhost:50053`
  - **Warmup default:** 60 seconds
  - **Measurement default:** 300 seconds (5 minutes)
  - **Connect timeout:** 10 seconds (gRPC)
  - **gRPC keepalive:** 30s interval, 10s timeout
  - **Histogram range:** 1 microsecond to 60 seconds, 3 significant digits
- **CLI args:** `--warmup <seconds>`, `--duration <seconds>`
- **Metrics collected:**
  - Per-endpoint latency histogram (p50, p75, p90, p95, p99, p99.9)
  - Merged WS vs gRPC comparison table with delta
  - Per-endpoint message counts
  - Node.js event loop delay monitoring (p50, p99, max) logged every 5s
- **Latency calculation:** `(now - msg.timestamp)` where `now = performance.timeOrigin + performance.now()` and `msg.timestamp` is `Date.now()` from producer, converted to microseconds.

### 3.2 benchmark-client/package.json
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/benchmark-client/package.json`
- **Description:** npm dependencies for benchmark client.
- **Dependencies:** `ws ^8.18.0`, `@grpc/grpc-js ^1.12.0`, `@grpc/proto-loader ^0.7.13`, `hdr-histogram-js ^3.0.1`
- **Scripts:** `start` (node client.js), `start:gc` (node --expose-gc client.js)

---

## 4. Infrastructure

### 4.1 infra/docker-compose.yml
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/infra/docker-compose.yml`
- **Description:** Kafka + Zookeeper infrastructure for the benchmark. Creates the network that gRPC containers also join.
- **Key configuration:**
  - **Zookeeper:**
    - Image: `confluentinc/cp-zookeeper:7.6.0`
    - Container: `benchmark-zookeeper`
    - Client port: `2181`
    - Tick time: `2000ms`
  - **Kafka:**
    - Image: `confluentinc/cp-kafka:7.6.0`
    - Container: `benchmark-kafka`
    - Ports: `9092:9092` (host), `29092:29092` (internal)
    - Listeners:
      - `PLAINTEXT://0.0.0.0:9092` (host access, advertised as `localhost:9092`)
      - `INTERNAL://0.0.0.0:29092` (container access, advertised as `benchmark-kafka:29092`)
    - Single broker, offset replication factor 1
    - `AUTO_CREATE_TOPICS_ENABLE: "false"`
    - `GROUP_INITIAL_REBALANCE_DELAY_MS: 0`
- **Network:** Default Docker Compose network named `infra_default` (used as `kafka-net` external network by gRPC containers)

### 4.2 Network Architecture
```
Host:
  - PM2 WS workers (localhost:9092) 
  - Kafka Producer (localhost:9092)
  - Benchmark Client (localhost:50051-50053, ws://localhost:8080)

Docker network "infra_default" (kafka-net):
  - benchmark-kafka (port 29092)
  - benchmark-zookeeper (port 2181)
  - grpc-server-1 (port 50051)
  - grpc-server-2 (port 50051)
  - grpc-server-3 (port 50051)

Docker network "grpc-net" (bridge):
  - grpc-server-1
  - grpc-server-2
  - grpc-server-3
```

---

## 5. WS Server (PM2 Cluster)

### 5.1 ws-server/server.js
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/ws-server/server.js`
- **Description:** WebSocket server running as PM2 cluster workers. Consumes from Kafka and broadcasts to connected WS clients.
- **Key configuration:**
  - Port: `8080` (env `PORT`, default 8080)
  - Kafka broker: `localhost:9092` (env `KAFKA_BROKER`)
  - Kafka topic: `benchmark-messages`
  - Consumer group: `ws-benchmark-worker-{INSTANCE}` where INSTANCE is `NODE_APP_INSTANCE` (PM2 sets this per worker)
  - Uses `ws` (WebSocket) and `kafkajs`
  - Sends `ready` signal to PM2 via `process.send("ready")`
  - All workers share port 8080 (PM2 cluster mode with socket sharing)

### 5.2 ws-server/ecosystem.config.js
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/ws-server/ecosystem.config.js`
- **Description:** PM2 process configuration for WS cluster.
- **Key configuration:**
  - App name: `ws-benchmark`
  - Script: `./server.js`
  - **Instances: 3** (cluster mode)
  - Exec mode: `cluster`
  - Max memory restart: `512M`
  - Kill timeout: `10000ms`
  - Listen timeout: `10000ms`
  - Wait ready: `true`
  - Max restarts: `10`
  - Restart delay: `4000ms`
  - Environment: PORT=8080, KAFKA_BROKER=localhost:9092, KAFKA_TOPIC=benchmark-messages

### 5.3 ws-server/package.json
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/ws-server/package.json`
- **Description:** npm dependencies for WS server.
- **Dependencies:** `ws ^8.18.0`, `kafkajs ^2.2.4`
- **Scripts:** start/stop/delete (PM2 commands), logs

---

## 6. Kafka Producer

### 6.1 producer/producer.js
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/producer/producer.js`
- **Description:** Generates benchmark messages to Kafka at a fixed rate.
- **Key configuration:**
  - Kafka broker: `localhost:9092` (env `KAFKA_BROKER`)
  - Topic: `benchmark-messages`
  - **Rate: 100 msg/s** (interval: 10ms)
  - **Payload size: ~1KB** (padded with "x" characters after JSON overhead)
  - Message format: `{ timestamp: Date.now(), seq: number, data: padding }`
  - Acks: `1` (leader only)
  - Logs throughput every 1000 messages

### 6.2 producer/package.json
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/producer/package.json`
- **Dependencies:** `kafkajs ^2.2.4`

---

## 7. Benchmark Runner Scripts

### 7.1 run-benchmark.sh
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/run-benchmark.sh`
- **Description:** One-command orchestrator that starts all services and runs the full benchmark suite.
- **Flow:**
  1. Start Kafka + Zookeeper via `infra/docker-compose.yml`
  2. Create topic `benchmark-messages` (1 partition, replication factor 1)
  3. Install producer deps
  4. Start 3 gRPC containers via `grpc-server/docker-compose.yml` (build + up)
  5. Start PM2 WS cluster (3 workers)
  6. Install benchmark client deps
  7. Run health check
  8. For each of 3 runs: start producer -> run client -> stop producer -> cooldown 10s
  9. Collect system info (kernel, node, docker, pm2 versions, PM2 metrics, Docker stats)
- **Defaults:** WARMUP=60s, DURATION=300s, RUNS=3
- **Results saved to:** `results/` directory

### 7.2 health-check.sh
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/health-check.sh`
- **Description:** Pre-flight verification of all services before benchmark runs.
- **Checks:**
  - Docker running (`docker info`)
  - Kafka container (`benchmark-kafka`)
  - Zookeeper container (`benchmark-zookeeper`)
  - Kafka topic exists (`benchmark-messages`)
  - gRPC ports: `50051`, `50052`, `50053` (via `nc -z`)
  - PM2 process: `ws-benchmark`
  - WS connectivity: `ws://localhost:8080` (actual WebSocket connection test)
- **Exits with code 1** if any check fails.

---

## 8. Results Directory

### 8.1 results/ structure
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/results/`
- **Description:** Contains benchmark output logs from previous runs.
- **Files (2 runs x 4 files = 8 files):**

  | File | Description |
  |------|-------------|
  | `run-1-20260419-013752.log` | Run 1 results (first batch) |
  | `run-2-20260419-013752.log` | Run 2 results (first batch) |
  | `run-3-20260419-013752.log` | Run 3 results (first batch) |
  | `system-info-20260419-013752.log` | System info for first batch |
  | `run-1-20260419-014250.log` | Run 1 results (second batch) |
  | `run-2-20260419-014250.log` | Run 2 results (second batch) |
  | `run-3-20260419-014250.log` | Run 3 results (second batch) |
  | `system-info-20260419-014250.log` | System info for second batch |

- **Sample results (run-1-20260419-014250.log):**
  ```
  WS  p50=0.001ms  p99=0.009ms  p99.9=0.037ms   (29534 msgs x 3)
  gRPC p50=0.002ms  p99=0.007ms  p99.9=0.022ms  (29534 msgs x 3)
  Event loop lag: p50=20.37ms, p99=23.74ms, max=24.58ms
  Total messages: 177204
  ```
- **Note:** Results are gitignored (in `.gitignore`).

---

## 9. Supporting Files

### 9.1 README.md
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/README.md`
- **Description:** Project documentation with architecture diagram, quick start, manual steps, and sample results.

### 9.2 .gitignore
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/.gitignore`
- **Description:** Ignores node_modules, results/, logs, .env, .pm2, IDE files, Docker tars, coverage.

### 9.3 plans/ directory
- **Path:** `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/plans/260418-latency-benchmark-ws-grpc/`
- **Description:** Original planning documents from initial implementation.
- **Files:**
  - `plan.md` — Master plan
  - `phase-01-kafka-setup.md` through `phase-05-integration-test.md` — Implementation phases
  - `research/researcher-01-pm2-ws-cluster.md` through `researcher-04-benchmark-approach.md` — Research notes

---

## 10. Complete File Inventory (Source Files Only)

| # | File | Role |
|---|------|------|
| 1 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/README.md` | Project documentation |
| 2 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/.gitignore` | Git ignore rules |
| 3 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/run-benchmark.sh` | Main benchmark orchestrator |
| 4 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/health-check.sh` | Pre-flight health checks |
| 5 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/proto/benchmark.proto` | Canonical gRPC proto definition |
| 6 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/infra/docker-compose.yml` | Kafka + Zookeeper infrastructure |
| 7 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/grpc-server/Dockerfile` | gRPC server Docker image |
| 8 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/grpc-server/docker-compose.yml` | 3x gRPC containers (bridge network) |
| 9 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/grpc-server/server.js` | gRPC server (Kafka consumer + stream) |
| 10 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/grpc-server/package.json` | gRPC server deps |
| 11 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/grpc-server/proto/benchmark.proto` | Proto copy for Docker build |
| 12 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/ws-server/server.js` | WS server (Kafka consumer + broadcast) |
| 13 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/ws-server/ecosystem.config.js` | PM2 cluster config (3 workers) |
| 14 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/ws-server/package.json` | WS server deps |
| 15 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/benchmark-client/client.js` | Benchmark client (6 connections, hdr-histogram) |
| 16 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/benchmark-client/package.json` | Benchmark client deps |
| 17 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/benchmark-client/proto/benchmark.proto` | Proto copy for client |
| 18 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/producer/producer.js` | Kafka message producer (100 msg/s) |
| 19 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/producer/package.json` | Producer deps |
| 20-27 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/results/*.log` | 8 result log files (2 benchmark sessions) |
| 28-37 | `/Users/toanlk/github.com/lekhanhtoan37/grpc-docker-wss-pm2/plans/260418-*/**` | 10 planning/research documents |

---

## 11. Key Observations for Host-Network Benchmark

1. **Current gRPC setup uses bridge networking:** The gRPC containers are on a custom bridge network (`grpc-net`) and connect to Kafka via an external network (`kafka-net` / `infra_default`). They do NOT use `network_mode: host`.

2. **WS runs natively on host:** PM2 workers connect directly to `localhost:9092` with no Docker networking overhead.

3. **Network topology asymmetry:**
   - WS path: Kafka (localhost:9092) -> WS worker (host) -> Client (localhost:8080)
   - gRPC path: Kafka (bridge:29092) -> gRPC container (bridge) -> port mapping -> Client (localhost:50051-50053)

4. **All 3 proto files are identical** — no divergence risk.

5. **Latency measurement approach:** Both WS and gRPC use `performance.timeOrigin + performance.now()` as the client-side timestamp and compare against `Date.now()` from the producer. This is a potential source of measurement noise since `performance.now()` and `Date.now()` use different clocks.

6. **Benchmark parameters:** 100 msg/s, ~1KB payload, 60s warmup, 300s measurement, 3 runs, 3 endpoints each.
