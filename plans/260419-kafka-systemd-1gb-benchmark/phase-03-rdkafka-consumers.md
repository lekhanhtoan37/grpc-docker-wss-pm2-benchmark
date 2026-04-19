# Phase 03: Replace kafkajs with node-rdkafka in Consumers

**Goal:** Replace kafkajs consumers in both ws-server and grpc-server with node-rdkafka for high-throughput consumption. Use batch fetch with `consumeLoop` for maximum throughput.

**Effort:** 2-3h

---

## Task 3.1: Install node-rdkafka in ws-server

**Files:**
- Modify: `ws-server/package.json`
- Modify: `ws-server/ecosystem.config.js`

- [ ] **Step 1: Update ws-server/package.json**

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
    "kafkajs": "^2.2.4",
    "node-rdkafka": "^3.3.0"
  }
}
```

- [ ] **Step 2: Install deps**

```bash
cd ws-server && npm install
```

Expected: node-rdkafka compiles successfully

- [ ] **Step 3: Update ecosystem.config.js — increase memory to 1G**

```javascript
module.exports = {
  apps: [
    {
      name: "ws-benchmark",
      script: "./server.js",
      instances: 3,
      exec_mode: "cluster",
      max_memory_restart: "1G",
      env: {
        PORT: 8080,
        KAFKA_BROKER: "localhost:9092",
        KAFKA_TOPIC: "benchmark-messages",
        UV_THREADPOOL_SIZE: "16",
      },
      kill_timeout: 10000,
      listen_timeout: 10000,
      wait_ready: true,
      max_restarts: 10,
      restart_delay: 4000,
    },
  ],
};
```

- [ ] **Step 4: Commit**

```bash
git add ws-server/package.json ws-server/package-lock.json ws-server/ecosystem.config.js
git commit -m "feat: add node-rdkafka to ws-server, increase memory limit"
```

---

## Task 3.2: Rewrite ws-server with node-rdkafka consumer

**Files:**
- Modify: `ws-server/server.js`

- [ ] **Step 1: Rewrite ws-server/server.js**

```javascript
const http = require("http");
const { WebSocketServer } = require("ws");
const Kafka = require("node-rdkafka");

const PORT = parseInt(process.env.PORT || "8080", 10);
const BROKER = process.env.KAFKA_BROKER || "localhost:9092";
const TOPIC = process.env.KAFKA_TOPIC || "benchmark-messages";
const INSTANCE = process.env.NODE_APP_INSTANCE || process.pid;
const GROUP_ID = `ws-benchmark-worker-${INSTANCE}`;

const clients = new Set();

const consumer = new Kafka.KafkaConsumer({
  "metadata.broker.list": BROKER,
  "group.id": GROUP_ID,
  "enable.auto.commit": true,
  "auto.commit.interval.ms": 5000,
  "fetch.min.bytes": 1048576,
  "fetch.max.bytes": 52428800,
  "fetch.wait.max.ms": 500,
  "max.partition.fetch.bytes": 10485760,
  "queued.min.messages": 100000,
  "queued.max.messages.kbytes": 1048576,
  "session.timeout.ms": 30000,
}, {
  "auto.offset.reset": "latest",
});

consumer.on("ready", () => {
  console.log(`[ws:${INSTANCE}] Kafka consumer ready (group: ${GROUP_ID})`);
  consumer.subscribe([TOPIC]);
  consumer.consume();
});

consumer.on("data", (message) => {
  const payload = message.value.toString();
  for (const ws of clients) {
    if (ws.readyState === 1) {
      ws.send(payload);
    }
  }
});

consumer.on("event.error", (err) => {
  console.error(`[ws:${INSTANCE}] Kafka error: ${err.message}`);
});

consumer.connect();

const server = http.createServer();
const wss = new WebSocketServer({ server });

wss.on("connection", (ws) => {
  clients.add(ws);
  console.log(`[ws:${INSTANCE}] Client connected (total: ${clients.size})`);
  ws.on("close", () => {
    clients.delete(ws);
    console.log(`[ws:${INSTANCE}] Client disconnected (total: ${clients.size})`);
  });
});

server.listen(PORT, () => {
  console.log(`[ws:${INSTANCE}] Listening on :${PORT}`);
  if (process.send) process.send("ready");
});

const shutdown = () => {
  console.log(`[ws:${INSTANCE}] Shutting down`);
  for (const ws of clients) ws.close();
  consumer.disconnect();
  process.exit(0);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
```

- [ ] **Step 2: Test ws-server standalone**

```bash
cd ws-server && node server.js
```

In another terminal, connect a test WS client:
```bash
node -e "const ws=new(require('ws'))('ws://localhost:8080');ws.on('message',d=>console.log(d.toString().substring(0,80)));ws.on('open',()=>console.log('connected'))"
```

Expected: Messages flow if producer is running

- [ ] **Step 3: Commit**

```bash
git add ws-server/server.js
git commit -m "feat: replace kafkajs with node-rdkafka in ws-server consumer"
```

---

## Task 3.3: Install node-rdkafka in grpc-server Docker image

**Files:**
- Modify: `grpc-server/package.json`
- Modify: `grpc-server/Dockerfile`

- [ ] **Step 1: Update grpc-server/package.json**

```json
{
  "name": "benchmark-grpc-server",
  "version": "1.0.0",
  "private": true,
  "dependencies": {
    "@grpc/grpc-js": "^1.12.0",
    "@grpc/proto-loader": "^0.7.13",
    "kafkajs": "^2.2.4",
    "node-rdkafka": "^3.3.0"
  }
}
```

- [ ] **Step 2: Update Dockerfile to use Debian-based image (node-rdkafka needs glibc)**

```dockerfile
FROM node:20-bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    librdkafka-dev \
    python3 \
    make \
    g++ \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY package*.json ./
RUN npm ci --omit=dev
COPY server.js .
COPY proto/ ./proto/
ENV GRPC_PORT=50051
ENV CONTAINER_ID=default
CMD ["node", "server.js"]
```

**Note:** `librdkafka-dev` provides the shared library. `python3`, `make`, `g++` needed for node-gyp at `npm ci`. The build tools are only used at build time — the runtime only needs `librdkafka2` (pulled in by `-dev`). For a production image, use multi-stage build.

- [ ] **Step 3: Build and verify**

```bash
cd grpc-server && docker compose build
```

Expected: Image builds, node-rdkafka compiles inside container

- [ ] **Step 4: Verify node-rdkafka inside container**

```bash
docker compose run --rm grpc-server-1 node -e "const K=require('node-rdkafka');console.log('rdkafka:',K.librdkafkaVersion)"
```

Expected: Prints version string

- [ ] **Step 5: Commit**

```bash
git add grpc-server/package.json grpc-server/Dockerfile
git commit -m "feat: add node-rdkafka to grpc-server with Debian-based Dockerfile"
```

---

## Task 3.4: Rewrite grpc-server with node-rdkafka consumer

**Files:**
- Modify: `grpc-server/server.js`

- [ ] **Step 1: Rewrite grpc-server/server.js**

```javascript
const grpc = require("@grpc/grpc-js");
const protoLoader = require("@grpc/proto-loader");
const Kafka = require("node-rdkafka");

const PROTO_PATH = process.env.PROTO_PATH || "/app/proto/benchmark.proto";
const BROKER = process.env.KAFKA_BROKER || "host.docker.internal:9092";
const TOPIC = process.env.KAFKA_TOPIC || "benchmark-messages";
const CONTAINER_ID = process.env.CONTAINER_ID || "default";
const GROUP_ID = `grpc-benchmark-${CONTAINER_ID}`;
const PORT = parseInt(process.env.GRPC_PORT || "50051", 10);
const HOST = process.env.GRPC_HOST || "0.0.0.0";

const packageDef = protoLoader.loadSync(PROTO_PATH, {
  keepCase: true,
  longs: String,
  enums: String,
  defaults: true,
  oneofs: true,
});
const benchmarkProto = grpc.loadPackageDefinition(packageDef).benchmark;

const activeStreams = new Set();

const consumer = new Kafka.KafkaConsumer({
  "metadata.broker.list": BROKER,
  "group.id": GROUP_ID,
  "enable.auto.commit": true,
  "auto.commit.interval.ms": 5000,
  "fetch.min.bytes": 1048576,
  "fetch.max.bytes": 52428800,
  "fetch.wait.max.ms": 500,
  "max.partition.fetch.bytes": 10485760,
  "queued.min.messages": 100000,
  "queued.max.messages.kbytes": 1048576,
  "session.timeout.ms": 30000,
}, {
  "auto.offset.reset": "latest",
});

consumer.on("ready", () => {
  console.log(`[grpc:${CONTAINER_ID}] Kafka consumer ready (group: ${GROUP_ID})`);
  consumer.subscribe([TOPIC]);
  consumer.consume();
});

consumer.on("data", (message) => {
  const raw = JSON.parse(message.value.toString());
  const response = {
    timestamp: raw.timestamp,
    seq: raw.seq,
    payload: message.value.toString(),
  };
  const toDelete = [];
  for (const call of activeStreams) {
    try {
      call.write(response);
    } catch {
      toDelete.push(call);
    }
  }
  for (const c of toDelete) activeStreams.delete(c);
});

consumer.on("event.error", (err) => {
  console.error(`[grpc:${CONTAINER_ID}] Kafka error: ${err.message}`);
});

function streamMessages(call) {
  activeStreams.add(call);
  console.log(`[grpc:${CONTAINER_ID}] Stream connected (total: ${activeStreams.size})`);
  call.on("cancelled", () => {
    activeStreams.delete(call);
    console.log(`[grpc:${CONTAINER_ID}] Stream cancelled (total: ${activeStreams.size})`);
  });
}

async function run() {
  const server = new grpc.Server();
  server.addService(benchmarkProto.BenchmarkService.service, {
    StreamMessages: streamMessages,
  });
  server.bindAsync(
    `${HOST}:${PORT}`,
    grpc.ServerCredentials.createInsecure(),
    (err) => {
      if (err) {
        console.error(`[grpc:${CONTAINER_ID}] Bind failed: ${err.message}`);
        process.exit(1);
      }
      server.start();
      console.log(`[grpc:${CONTAINER_ID}] Listening on ${HOST}:${PORT}`);
    }
  );

  consumer.connect();
}

const shutdown = () => {
  console.log(`[grpc:${CONTAINER_ID}] Shutting down`);
  consumer.disconnect();
  process.exit(0);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

run();
```

- [ ] **Step 2: Rebuild Docker image**

```bash
cd grpc-server && docker compose build
```

Expected: Build succeeds

- [ ] **Step 3: Commit**

```bash
git add grpc-server/server.js
git commit -m "feat: replace kafkajs with node-rdkafka in grpc-server consumer"
```
