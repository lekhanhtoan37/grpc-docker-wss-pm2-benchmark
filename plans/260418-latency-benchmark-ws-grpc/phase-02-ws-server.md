# Phase 2: PM2 Cluster WebSocket Server

**Prerequisites**: Phase 1 complete (Kafka topic + producer running)

---

## Tasks

### 2.1 Create WS Server App

**Directory**: `ws-server/`

**package.json**:
```json
{
  "name": "benchmark-ws-server",
  "version": "1.0.0",
  "private": true,
  "scripts": {
    "start": "pm2 start ecosystem.config.js",
    "stop": "pm2 stop ecosystem.config.js",
    "delete": "pm2 delete ecosystem.config.js",
    "logs": "pm2 logs ws-benchmark"
  },
  "dependencies": {
    "ws": "^8.18.0",
    "kafkajs": "^2.2.4"
  }
}
```

### 2.2 Server Implementation (`server.js`)

**Architecture**:
- `http.createServer()` + `WebSocket.Server({ server })` on shared port
- Kafka consumer with **unique group ID per worker** (`ws-benchmark-worker-${process.env.NODE_APP_INSTANCE || process.pid}`)
- Each worker consumes ALL messages from `benchmark-messages` topic
- On Kafka message → iterate local WS clients → `ws.send(message.value)`
- Track active connections in `Set`
- Embed receive timestamp for server-side latency tracking (optional)

**Key behaviors**:
- PM2 cluster mode: master shares port 8080, round-robin distributes connections
- `ws` library: no sticky sessions needed (WebSocket connections are persistent)
- Each worker has independent Kafka consumer (unique group = sees all messages)
- `process.send('ready')` after Kafka connected + WS listening (for `wait_ready`)

**No Redis needed**: Each worker is its own Kafka consumer with unique group. Each gets all messages independently. No cross-worker broadcast required.

### 2.3 PM2 Ecosystem Config (`ecosystem.config.js`)

```js
module.exports = {
  apps: [{
    name: 'ws-benchmark',
    script: './server.js',
    instances: 3,
    exec_mode: 'cluster',
    max_memory_restart: '512M',
    env: {
      PORT: 8080,
      KAFKA_BROKER: 'localhost:9092',
      KAFKA_TOPIC: 'benchmark-messages',
    },
    kill_timeout: 10000,
    listen_timeout: 10000,
    wait_ready: true,
    max_restarts: 10,
    restart_delay: 4000,
  }]
}
```

**Notes**:
- `instances: 3` — 3 workers
- Single port 8080 — PM2 cluster shares across workers
- `wait_ready: true` — workers signal readiness via `process.send('ready')`
- `NODE_APP_INSTANCE` env var set by PM2 (0, 1, 2) — use for consumer group ID

### 2.4 Server Pseudocode

```
on startup:
  kafka consumer connect (group: ws-benchmark-worker-{INSTANCE_ID})
  subscribe to benchmark-messages topic
  create http server on PORT
  attach WebSocket.Server
  
on kafka message:
  payload = message.value (JSON string with timestamp, seq, data)
  for each ws client in local Set:
    if ws.readyState == OPEN:
      ws.send(payload)

on ws connection:
  add to local clients Set
  
on ws close:
  remove from local clients Set

on SIGINT:
  disconnect kafka consumer
  close all ws connections
  close http server
```

### 2.5 Connection Flow

```
Benchmark Client (host)
  ├─ ws://localhost:8080 → PM2 round-robin → Worker 0
  ├─ ws://localhost:8080 → PM2 round-robin → Worker 1
  └─ ws://localhost:8080 → PM2 round-robin → Worker 2

Note: All 3 WS connections go to same port 8080.
PM2 distributes them across workers via round-robin.
Benchmark client can't control which worker gets which connection.
This is fine — we measure per-connection latency regardless.
```

### 2.6 Verification

```bash
# Start WS server
cd ws-server && npm install && pm2 start ecosystem.config.js

# Verify 3 workers running
pm2 list

# Quick test with wscat
npx wscat -c ws://localhost:8080

# Start producer (Phase 1) — should see messages arrive at wscat
```

---

## Gotchas

- **All connections on same port**: Benchmark client connects 3x to `ws://localhost:8080`. PM2 distributes across workers. Client can't target specific workers. This is fine — latency measurement is per-connection.
- **Consumer group uniqueness**: Must use `NODE_APP_INSTANCE` or `process.pid` in group ID. If all workers share same group ID, Kafka splits messages (only 1/3 per worker).
- **PM2 `wait_ready`**: Without this, PM2 may route connections to workers before Kafka is connected. Workers would miss early messages.
- **Worker restart**: If a worker crashes, its WS clients disconnect. Benchmark client should handle reconnection (or accept gap in measurements).
- **Memory**: 3 workers × ~50MB each = ~150MB baseline. Set `max_memory_restart: 512M` as safety net.

---

## Acceptance Criteria

- [ ] 3 PM2 workers running on port 8080
- [ ] Each worker has unique Kafka consumer group
- [ ] Each worker receives all messages from Kafka
- [ ] Messages forwarded to connected WS clients
- [ ] Graceful shutdown (Kafka disconnect + WS close on SIGINT)
- [ ] `process.send('ready')` after init
