# Phase 3: gRPC Server Docker Containers (Bridge Network)

**Prerequisites**: Phase 1 complete (Kafka topic + producer running)

---

## Tasks

### 3.1 Create Proto File

**File**: `proto/benchmark.proto`

```protobuf
syntax = "proto3";
package benchmark;

service BenchmarkService {
  rpc StreamMessages(StreamRequest) returns (stream StreamResponse);
}

message StreamRequest {
  string client_id = 1;
}

message StreamResponse {
  uint64 timestamp = 1;
  uint64 seq = 2;
  string payload = 3;
}
```

**Design notes**:
- `timestamp` = epoch ms from producer (uint64 → `longs: String` in proto loader)
- `seq` = sequential message ID from producer
- `payload` = JSON string containing the full message data
- Single RPC: server-streaming. Client sends 1 request, server streams responses indefinitely.

### 3.2 Create gRPC Server

**Directory**: `grpc-server/`

**package.json**:
```json
{
  "name": "benchmark-grpc-server",
  "version": "1.0.0",
  "private": true,
  "dependencies": {
    "@grpc/grpc-js": "^1.12.0",
    "@grpc/proto-loader": "^0.7.13",
    "kafkajs": "^2.2.4"
  }
}
```

**server.js**:
- Load proto from `/app/proto/benchmark.proto`
- `BenchmarkService.StreamMessages` handler:
  - Add `call` to `activeStreams` Set
  - On `call.cancelled` → remove from Set
- Kafka consumer:
  - Unique group ID: `grpc-benchmark-{CONTAINER_ID}`
  - Subscribe to `benchmark-messages` topic
  - On each message → iterate `activeStreams` → `call.write(response)`
- Bind to `0.0.0.0:50051` (NOT 127.0.0.1)
- `CONTAINER_ID` env var for unique consumer group

### 3.3 Dockerfile

```dockerfile
FROM node:20-alpine
WORKDIR /app
COPY package*.json ./
RUN npm ci --only=production
COPY server.js .
COPY proto/ ./proto/
EXPOSE 50051
ENV CONTAINER_ID=default
CMD ["node", "server.js"]
```

**Notes**:
- `node:20-alpine` — small image (~180MB)
- `npm ci --only=production` — no dev deps
- Proto file copied into image
- `CONTAINER_ID` overridden per container in compose

### 3.4 Docker Compose

**File**: `grpc-server/docker-compose.yml`

```yaml
version: "3.8"

services:
  grpc-server-1:
    build: .
    container_name: grpc-server-1
    environment:
      CONTAINER_ID: "1"
      KAFKA_BROKER: "host.docker.internal:9092"
      KAFKA_TOPIC: "benchmark-messages"
    ports:
      - "50051:50051"
    networks:
      - grpc-net
    extra_hosts:
      - "host.docker.internal:host-gateway"

  grpc-server-2:
    build: .
    container_name: grpc-server-2
    environment:
      CONTAINER_ID: "2"
      KAFKA_BROKER: "host.docker.internal:9092"
      KAFKA_TOPIC: "benchmark-messages"
    ports:
      - "50052:50051"
    networks:
      - grpc-net
    extra_hosts:
      - "host.docker.internal:host-gateway"

  grpc-server-3:
    build: .
    container_name: grpc-server-3
    environment:
      CONTAINER_ID: "3"
      KAFKA_BROKER: "host.docker.internal:9092"
      KAFKA_TOPIC: "benchmark-messages"
    ports:
      - "50053:50051"
    networks:
      - grpc-net
    extra_hosts:
      - "host.docker.internal:host-gateway"

networks:
  grpc-net:
    driver: bridge
```

**Key config**:
- Each container maps unique host port → internal 50051
- `host.docker.internal:host-gateway` for container→host Kafka access
- User-defined bridge `grpc-net` for inter-container DNS
- `KAFKA_BROKER=host.docker.internal:9092` — Kafka on host

### 3.5 Connection Flow

```
Benchmark Client (host)
  ├─ localhost:50051 → port mapping → grpc-server-1:50051 (bridge)
  ├─ localhost:50052 → port mapping → grpc-server-2:50051 (bridge)
  └─ localhost:50053 → port mapping → grpc-server-3:50051 (bridge)

Container → Kafka:
  grpc-server-1 → host.docker.internal:9092 → Kafka on host
  grpc-server-2 → host.docker.internal:9092 → Kafka on host
  grpc-server-3 → host.docker.internal:9092 → Kafka on host
```

### 3.6 Server Pseudocode

```
on startup:
  load proto definition
  create grpc server
  register BenchmarkService.StreamMessages handler
  bind to 0.0.0.0:50051
  start grpc server
  connect kafka consumer (group: grpc-benchmark-{CONTAINER_ID})
  subscribe to benchmark-messages topic

on StreamMessages(call):
  activeStreams.add(call)
  call.on('cancelled', () => activeStreams.delete(call))

on kafka message:
  response = {
    timestamp: payload.timestamp,
    seq: payload.seq,
    payload: JSON.stringify(payload)
  }
  for each call in activeStreams:
    try call.write(response)
    catch: remove call from activeStreams
```

### 3.7 Verification

```bash
# Build and start containers
cd grpc-server && docker compose up -d --build

# Verify 3 containers running
docker compose ps

# Quick test with grpcurl (if installed)
grpcurl -plaintext localhost:50051 benchmark.BenchmarkService/StreamMessages

# Start producer — should see messages streaming
```

---

## Gotchas

- **`0.0.0.0` binding**: gRPC server MUST bind `0.0.0.0:50051` inside container. `127.0.0.1` only accessible within container.
- **Bridge NAT overhead**: ~0.1-0.5ms per hop. This is the Docker overhead we're measuring (part of the benchmark comparison).
- **`host.docker.internal` on Linux**: Needs `extra_hosts: host.docker.internal:host-gateway`. On Docker Desktop (macOS), it's automatic. The `extra_hosts` config works on both.
- **`call.write()` backpressure**: Returns `false` if buffer full. For 100 msg/s this is unlikely, but should check return value in production code.
- **gRPC keepalive**: Default keepalive may kill idle streams. If benchmark has quiet periods, configure keepalive on client:
  ```js
  const client = new BenchmarkService(endpoint, creds, {
    'grpc.keepalive_time_ms': 30000,
    'grpc.keepalive_timeout_ms': 10000,
  });
  ```
- **Proto file location**: Server loads from `/app/proto/benchmark.proto` (inside container). Client loads from `./proto/benchmark.proto` (on host). Keep in sync.
- **Container rebuild**: `docker compose up -d --build` to rebuild after code changes.

---

## Acceptance Criteria

- [ ] 3 Docker containers running on `grpc-net` bridge network
- [ ] Host ports 50051/50052/50053 mapped to container port 50051
- [ ] Each container has unique Kafka consumer group
- [ ] Each container connects to Kafka via `host.docker.internal:9092`
- [ ] gRPC streaming works from host via `grpcurl` or client
- [ ] Messages from Kafka forwarded to all connected gRPC streams
- [ ] Clean container shutdown on `docker compose down`
