# Research: PM2 Cluster Mode with `ws` Library (3 Instances)

## Key Findings

### 1. PM2 Cluster + `ws` Library: No Sticky Session Problem

- **Critical insight from `ws` maintainer (@lpinca, GitHub issue #1631)**: WebSocket connections are persistent. Once upgraded, all subsequent frames go to the same worker. The Node.js `cluster` module distributes *new* connections via round-robin. There is NO sticky session problem for pure WebSocket (`ws` library).
- Sticky sessions are only needed for Socket.IO (which uses HTTP long-polling before upgrade). With raw `ws`, the TCP connection stays pinned to the worker that handled the upgrade.
- PM2 cluster mode is confirmed safe for production with `ws`.

### 2. How Connection Distribution Works

- PM2 uses Node.js `cluster` module under the hood
- The master process shares the same port across all workers
- New connections are distributed via round-robin (default on most platforms)
- Once a WebSocket connection is established on a worker, **all messages on that socket are handled by that same worker** — no routing confusion
- Each worker maintains its own set of connected clients (local state)

### 3. Cross-Worker Broadcasting Problem

- If Kafka event arrives on Worker 0, only clients connected to Worker 0 receive it
- Workers do NOT share WebSocket connection state
- Solutions for cross-worker broadcast:
  - **Redis Pub/Sub** (recommended): Each worker subscribes to a Redis channel. When Kafka event arrives, publish to Redis. All workers receive and push to their local clients.
  - **Kafka consumer groups**: Each worker is a consumer in the same group. Kafka handles partition assignment. With 3 partitions + 3 workers = 1:1 mapping. Each worker consumes from its own partition and pushes to its local clients only.

### 4. Kafka Consumer with PM2 Cluster

- **Best approach**: Use Kafka consumer groups with same group ID across all 3 instances
- Kafka will automatically assign partitions to consumers in the same group
- 3 partitions + 3 workers = each worker gets 1 partition (ideal)
- Messages are NOT duplicated — Kafka guarantees each message consumed by exactly one consumer in the group
- **Gotcha**: If #workers > #partitions, some workers sit idle. Ensure partition count >= instance count.

### 5. Per-Message Latency Measurement

- Measure at the client side: `send_time` → `receive_time` delta
- Embed timestamp in message payload, compute RTT on client
- For server-side latency: use `process.hrtime.bigint()` or `performance.now()` at Kafka consume → WS send
- PM2 provides `tx2` histogram metric for latency tracking:
  ```js
  const tx2 = require('tx2')
  const latencyHist = tx2.histogram({ name: 'ws_msg_latency', measurement: 'mean' })
  ```
- For cluster-wide latency, aggregate metrics via Redis or external monitoring

---

## Ecosystem Config for 3 WS Instances

```js
// ecosystem.config.js
module.exports = {
  apps: [{
    name: 'ws-kafka-server',
    script: './src/server.js',
    instances: 3,
    exec_mode: 'cluster',
    max_memory_restart: '512M',
    env: {
      NODE_ENV: 'production',
      PORT: 8080,
      KAFKA_GROUP_ID: 'ws-consumer-group',
      KAFKA_BROKERS: 'kafka:9092',
      REDIS_URL: 'redis://redis:6379'
    },
    error_file: './logs/error.log',
    out_file: './logs/out.log',
    log_date_format: 'YYYY-MM-DD HH:mm:ss Z',
    kill_timeout: 10000,
    listen_timeout: 10000,
    wait_ready: true,
    max_restarts: 10,
    restart_delay: 4000
  }]
}
```

---

## Server Implementation Pattern

```js
const express = require('express')
const http = require('http')
const WebSocket = require('ws')
const { Kafka } = require('kafkajs')
const Redis = require('ioredis')

const app = express()
const server = http.createServer(app)
const wss = new WebSocket.Server({ server })

const KAFKA_TOPIC = 'events'
const REDIS_CHANNEL = 'ws:broadcast'

// Redis subscriber for cross-worker broadcast
const redisSub = new Redis(process.env.REDIS_URL)
const redisPub = new Redis(process.env.REDIS_URL)

// Kafka consumer — same group ID, Kafka handles partition assignment
const kafka = new Kafka({
  brokers: [process.env.KAFKA_BROKERS],
  groupId: process.env.KAFKA_GROUP_ID
})
const consumer = kafka.consumer({ groupId: process.env.KAFKA_GROUP_ID })

// Track local clients
const clients = new Set()

wss.on('connection', (ws) => {
  clients.add(ws)
  ws.on('close', () => clients.delete(ws))
})

// Kafka → Redis broadcast (all workers get it)
async function startKafka() {
  await consumer.connect()
  await consumer.subscribe({ topic: KAFKA_TOPIC, fromBeginning: false })
  await consumer.run({
    eachMessage: async ({ message }) => {
      // Publish to Redis so ALL workers can push to their local clients
      redisPub.publish(REDIS_CHANNEL, message.value.toString())
    }
  })
}

// Redis → local WebSocket clients
redisSub.subscribe(REDIS_CHANNEL)
redisSub.on('message', (channel, data) => {
  const t0 = performance.now()
  for (const ws of clients) {
    if (ws.readyState === WebSocket.OPEN) {
      ws.send(data)
    }
  }
})

server.listen(process.env.PORT, () => {
  console.log(`Worker ${process.pid} ready on port ${process.env.PORT}`)
  if (process.send) process.send('ready') // PM2 wait_ready
})

startKafka().catch(console.error)
```

---

## Gotchas & Edge Cases

1. **WebSocket + PM2 cluster = no sticky session needed** (unlike Socket.IO). The `ws` library upgrade is a single HTTP request; once upgraded, connection stays on that worker.

2. **Kafka consumer groups must match partition count**: 3 instances → need at least 3 partitions. Otherwise workers sit idle or get uneven load.

3. **Graceful shutdown critical**: PM2 sends SIGINT. Must close WS connections + disconnect Kafka consumer before exiting. Set `kill_timeout` generously (10s+).

4. **Memory per worker**: Each worker has its own V8 heap. 3 instances = ~3x memory. Set `max_memory_restart` to catch leaks.

5. **Redis as broadcast bus**: Without Redis (or similar), a Kafka event consumed by Worker A can only reach clients on Worker A. Redis pub/sub is the simplest cross-worker broadcast mechanism.

6. **Client reconnection**: If a worker dies, its clients lose connection. Clients must implement reconnection logic. New connection goes to a different worker via round-robin.

7. **`wait_ready: true`**: Use `process.send('ready')` to signal PM2 that the worker is initialized (Kafka connected, WS server listening). Prevents PM2 from sending traffic to uninitialized workers.

8. **Zero-downtime reload**: Use `pm2 reload` (not `pm2 restart`). Reloads workers one by one, keeping at least one alive.

9. **`NODE_APP_INSTANCE`**: PM2 sets this env var (0, 1, 2). Can use it to assign static partition per worker instead of consumer groups, but consumer groups are simpler and more resilient.

10. **Latency measurement must be client-side**: Server-side measurements miss network RTT. Embed `Date.now()` or `performance.now()` in payload, compute delta on client.

---

## Recommendations

1. **Use `ws` library directly** — no need for Socket.IO. Avoids sticky session complexity entirely.
2. **Use Kafka consumer groups** with 3 partitions for 3 workers. No duplication, automatic rebalancing on worker failure.
3. **Add Redis pub/sub** for cross-worker WebSocket broadcast if any worker needs to push to all connected clients.
4. **Measure latency client-side** with embedded timestamps in payloads.
5. **Start with `pm2 reload`** for deployments, never `pm2 restart` (causes downtime).
6. **Set `wait_ready: true`** in ecosystem config + call `process.send('ready')` after Kafka + WS init.
7. **Monitor per-worker metrics**: `pm2 monit` or PM2 Plus for CPU/memory per instance.

---

## Sources

- GitHub `websockets/ws` issue #1631 — maintainer confirms `ws` works in PM2 cluster, no sticky sessions needed
- PM2 docs on load-balancing and cluster mode
- StackOverflow: Kafka consumer with PM2 cluster — consumer groups prevent duplication
- StackOverflow: Scaling `ws` with multiple instances — Redis pub/sub for cross-worker broadcast
- Socket.IO PM2 docs — reference for sticky session complexity (avoided by using raw `ws`)
