# Research Report: uWebSockets.js for Kafka→WebSocket Server

**Date:** 2026-04-22
**Scope:** uWS API, Kafka integration pattern, Docker compatibility, performance vs `ws`

## Executive Summary

uWebSockets.js (uWS) is a C++ native addon for Node.js providing the highest-performance WebSocket server available. It exposes a built-in **pub/sub system** that is the idiomatic way to broadcast messages — no manual client Set tracking needed. For Kafka→WS broadcasting, the pattern is: Kafka consumer calls `app.publish(topic, message)` and all subscribed WS clients receive it instantly. The main gotcha is **Docker Alpine incompatibility** (musl vs glibc) and **glibc version requirements** on slim images.

**Verdict:** Best choice for high-throughput Kafka→WS fan-out. Use `node:XX-bookworm-slim` (Debian-based) or `node:XX` Docker images, NOT Alpine.

## Installation

uWS is **not on npm registry**. Install via GitHub:

```bash
npm install uNetworking/uWebSockets.js#v20.61.0
```

Prebuilt binaries ship for: Linux (glibc, x64/arm64), macOS, Windows. No compilation needed on supported platforms.

## Core API

### Creating a WebSocket Server

```js
const uWS = require('uWebSockets.js');

const app = uWS.App()  // or uWS.SSLApp({...}) for TLS
  .ws('/*', {
    compression: uWS.SHARED_COMPRESSOR,  // or 0 for none
    maxPayloadLength: 16 * 1024 * 1024,
    idleTimeout: 30,        // seconds, 0 = no timeout
    maxBackpressure: 1024,  // max buffered bytes before force-close

    upgrade: (res, req, context) => {
      // Custom upgrade handler (optional — omit for default behavior)
      res.upgrade(
        { myData: req.getUrl() },   // UserData — attached to ws object
        req.getHeader('sec-websocket-key'),
        req.getHeader('sec-websocket-protocol'),
        req.getHeader('sec-websocket-extensions'),
        context
      );
    },

    open: (ws) => {
      // ws.getUserData() returns what you passed to res.upgrade
      ws.subscribe('kafka-messages');  // subscribe to topic
    },

    message: (ws, message, isBinary) => {
      // message is ArrayBuffer, convert: Buffer.from(message)
      // Echo back:
      ws.send(message, isBinary);
    },

    drain: (ws) => {
      // Called when backpressure decreases
      console.log('Backpressure:', ws.getBufferedAmount());
    },

    close: (ws, code, message) => {
      // Auto-unsubscribes from all topics
    }
  })
  .listen(9001, (listenSocket) => {
    if (listenSocket) console.log('Listening on 9001');
  });
```

### Key Differences from `ws` Library

| Feature | `ws` | uWebSockets.js |
|---|---|---|
| Implementation | Pure JS | C++ native addon |
| Message param type | `Buffer` | `ArrayBuffer` (use `Buffer.from(msg)`) |
| Send API | `ws.send(data)` returns void | `ws.send(data, isBinary)` returns `boolean` (false = backpressure) |
| Broadcast | Manual iteration over Set | Built-in `app.publish()` / `ws.publish()` |
| HTTP server | Separate (`http.createServer`) | Integrated (`uWS.App()` handles HTTP + WS) |
| Backpressure | `ws.bufferedAmount` (read-only) | `ws.getBufferedAmount()` + `drain` handler + `maxBackpressure` auto-close |
| Connection tracking | `wss.clients` Set | Manual or use pub/sub topics |
| Upgrade control | `verifyClient` option | `upgrade` handler with `res.upgrade()` / `res.close()` |

## Broadcasting: The Pub/Sub System

### Method 1: `app.publish()` (RECOMMENDED for Kafka→WS)

This is the **most efficient** way to broadcast — C++ level, no JS iteration:

```js
const uWS = require('uWebSockets.js');
const { Kafka } = require('kafkajs');

const app = uWS.App().ws('/*', {
  compression: 0,
  maxPayloadLength: 1024 * 1024,
  idleTimeout: 60,

  open: (ws) => {
    ws.subscribe('kafka-broadcast');
  },
  message: (ws, msg, isBinary) => {
    // handle client→server if needed
  },
  close: (ws) => {
    // auto-unsubscribed
  }
}).listen(9001, (token) => {
  if (token) console.log('WS server on :9001');
});

// Kafka consumer — publish directly to uWS topic
const kafka = new Kafka({ brokers: ['kafka:9092'] });
const consumer = kafka.consumer({ groupId: 'ws-broadcast' });

async function startKafka() {
  await consumer.connect();
  await consumer.subscribe({ topic: 'my-topic', fromBeginning: false });

  await consumer.run({
    eachMessage: async ({ message }) => {
      // app.publish(topic, message, isBinary)
      // message must be ArrayBuffer, TypedArray, or Buffer
      app.publish('kafka-broadcast', message.value, true);
    }
  });
}

startKafka().catch(console.error);
```

### Method 2: Manual Set Tracking (when you need per-client logic)

```js
const clients = new Set();

// in open handler:
clients.add(ws);

// in close handler:
clients.delete(ws);

// broadcast:
clients.forEach(ws => {
  if (ws.getBufferedAmount() < maxBackpressure) {
    ws.send(message, isBinary);
  }
});
```

### `app.publish()` vs `ws.publish()`

| Call | Scope |
|---|---|
| `app.publish('topic', msg)` | All subscribers on that app (GLOBAL — use this for Kafka) |
| `ws.publish('topic', msg)` | All subscribers EXCEPT the calling ws (for chat relay) |

## Performance vs `ws`

| Metric | `ws` | uWebSockets.js |
|---|---|---|
| Messages/sec | ~400K | ~2M+ |
| Connections/sec | ~10K | ~100K |
| Memory (10K conns) | ~200MB | ~40MB |
| Latency p99 | ~5ms | ~1ms |

Sources: PkgPulse benchmarks (Feb 2026), community benchmarks. uWS is **5-10x faster** in throughput and **5x lower** in memory.

## Docker Compatibility

### Problem: Alpine (musl) does NOT work

uWS binaries are compiled against **glibc**. Alpine uses musl libc.

**Error:** `Error: This version of uWS.js supports only ... on (glibc) Linux`

### Solution: Use Debian-based images

```dockerfile
# WORKS — Debian bookworm (glibc 2.36+, needed for recent uWS)
FROM node:20-bookworm-slim

# If you need glibc >= 2.38 (uWS v20.55+), use trixie:
FROM node:25-trixie-slim

WORKDIR /app
COPY package*.json ./
RUN npm install
COPY . .
CMD ["node", "server.js"]
```

### Alpine Workaround (NOT recommended)

```dockerfile
FROM node:20-alpine
# May work with older uWS versions — unreliable
RUN apk add --no-cache gcompat
# OR:
RUN ln -s "/lib/libc.musl-$(uname -m).so.1" "/lib/ld-linux-$(uname -m).so.1"
```

### glibc Version Requirements

| uWS Version | Min glibc | Docker Base |
|---|---|---|
| v20.55+ | GLIBC_2.38 | `node:25-trixie-slim` or Ubuntu 24.04+ |
| v20.x (older) | GLIBC_2.31 | `node:20-bookworm-slim` |

**Recommendation:** Pin your uWS version and match the Docker base image accordingly.

## Gotchas & Limitations

1. **Message type is ArrayBuffer, not Buffer** — Always convert: `Buffer.from(message)` on receive
2. **No npm registry** — Must install from GitHub URL, some monorepo tools struggle
3. **Backpressure is critical** — Always check `ws.send()` return value; set `maxBackpressure` to auto-disconnect slow clients
4. **No auto-reconnect** — Clients must implement reconnection logic (like raw `ws`)
5. **`drain` handler required** for high-throughput — Resume sending when backpressure clears
6. **`ws` objects are NOT plain EventEmitter** — Cannot add arbitrary properties directly (use UserData via upgrade handler)
7. **No `wss.clients`** — Connection tracking is manual or via pub/sub
8. **Graceful shutdown** — Save `listenSocket` from `.listen()` callback, call `uWS.us_listen_socket_close(listenSocket)`
9. **Binary only on some versions** — The `app.publish()` 3rd param `isBinary` is important for binary Kafka messages
10. **No hot-reload friendly** — Native addon means nodemon/supertest can be flaky

## Kafka→WS Server: Complete Pattern

```js
const uWS = require('uWebSockets.js');
const { Kafka } = require('kafkajs');

const TOPIC_WS = 'kafka-broadcast';
const KAFKA_TOPIC = process.env.KAFKA_TOPIC || 'events';
const PORT = parseInt(process.env.PORT || '9001');

let listenSocket;

const app = uWS.App()
  .ws('/*', {
    compression: 0,
    maxPayloadLength: 1024 * 1024,
    idleTimeout: 120,
    maxBackpressure: 64 * 1024,

    open: (ws) => {
      ws.subscribe(TOPIC_WS);
      console.log('Client connected');
    },

    message: (ws, msg, isBinary) => {
      // Optional: handle client→server messages
    },

    close: (ws, code, msg) => {
      // Auto-unsubscribed by uWS
    }
  })
  .get('/health', (res, req) => {
    res.end('ok');
  })
  .listen(PORT, (token) => {
    listenSocket = token;
    if (token) {
      console.log(`uWS server listening on :${PORT}`);
    } else {
      console.error(`Failed to listen on :${PORT}`);
      process.exit(1);
    }
  });

// Kafka consumer
const kafka = new Kafka({
  clientId: 'ws-broadcast-server',
  brokers: (process.env.KAFKA_BROKERS || 'localhost:9092').split(',')
});

const consumer = kafka.consumer({ groupId: 'ws-broadcast-group' });

(async () => {
  await consumer.connect();
  await consumer.subscribe({ topic: KAFKA_TOPIC, fromBeginning: false });

  await consumer.run({
    eachMessage: async ({ topic, partition, message }) => {
      // Broadcast to all subscribed WS clients via C++ pub/sub
      app.publish(TOPIC_WS, message.value, true);
    }
  });

  console.log(`Consuming from Kafka topic: ${KAFKA_TOPIC}`);
})();

// Graceful shutdown
process.on('SIGTERM', async () => {
  console.log('Shutting down...');
  await consumer.disconnect();
  if (listenSocket) {
    uWS.us_listen_socket_close(listenSocket);
  }
  process.exit(0);
});
```

## Resources

- **GitHub:** https://github.com/uNetworking/uWebSockets.js (v20.61.0, latest)
- **Examples:** https://github.com/uNetworking/uWebSockets.js/tree/master/examples
- **PubSub example:** https://github.com/uNetworking/pubsub-example
- **Broadcast discussion:** https://github.com/uNetworking/uWebSockets.js/discussions/140
- **Docker Alpine issue:** https://github.com/uNetworking/uWebSockets.js/discussions/158
- **Docker slim glibc issue:** https://github.com/uNetworking/uWebSockets.js/issues/1196
- **Performance comparison:** https://www.pkgpulse.com/blog/socketio-vs-ws-vs-uwebsockets-websocket-servers-nodejs-2026

## Unresolved Questions

1. **PM2 compatibility** — Native addons can sometimes conflict with PM2 cluster mode. Need to test if `cluster_mode: true` works or if PM2 `fork` mode is required.
2. **Compression trade-offs** — `SHARED_COMPRESSOR` vs `DEDICATED_COMPRESSOR_*` for Kafka binary messages: compression overhead may negate throughput gains.
3. **Kafka message serialization** — If Kafka messages are Avro/Protobuf encoded, the binary flag `true` in `app.publish()` is correct, but need to verify client-side deserialization.
