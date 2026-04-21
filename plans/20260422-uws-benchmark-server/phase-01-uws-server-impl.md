---
phase: 1
title: "uWS Server Implementation"
description: "Create uws-server/ directory with server.js, package.json, ecosystem.config.js"
depends_on: []
---

# Phase 1: uWS Server Implementation

## Goal

Create `uws-server/` mirroring `ws-server/` structure but using uWebSockets.js. Server consumes Kafka, broadcasts to WS clients with identical processing to ws-server (JSON.parse validation, yield every 2k messages, manual broadcast).

## Files to Create

### 1. `uws-server/package.json`

```json
{
  "name": "benchmark-uws-server",
  "version": "1.0.0",
  "private": true,
  "scripts": {
    "start": "pm2 start ecosystem.config.js",
    "stop": "pm2 stop ecosystem.config.js",
    "delete": "pm2 delete ecosystem.config.js",
    "logs": "pm2 logs uws-benchmark"
  },
  "dependencies": {
    "uWebSockets.js": "github:uNetworking/uWebSockets.js#v20.61.0",
    "kafkajs": "^2.2.4"
  }
}
```

**Notes:**
- `uWebSockets.js` is NOT on npm registry — install from GitHub
- `kafkajs` version matches `ws-server/package.json`

### 2. `uws-server/server.js`

Structure (mirrors `ws-server/server.js` line-by-line):

```
Lines 1-9:   Imports + env config (PORT, BROKER, TOPIC, INSTANCE, GROUP_ID)
Lines 10-13: clients Set, YIELD_EVERY, yieldLoop helper
Lines 14-27: Kafka consumer setup (identical to ws-server)
Lines 28-60: Kafka consumer start + retry loop
  - eachBatch: iterate clients Set
  - For each client: JSON.parse(msg.value.toString()), ws.send(msg.value.toString())
  - Yield every 2k messages via setImmediate
  - Track dead clients, remove after loop
Lines 61-80: uWS server setup
  - uWS.App().ws('/*', { handlers }) + .get('/health', handler) + .listen(PORT)
  - open: clients.add(ws)
  - close: clients.delete(ws)  
  - message: no-op (server doesn't process client messages)
  - Listen callback: process.send("ready")
Lines 81-90: Graceful shutdown (SIGINT/SIGTERM)
```

**Key differences from ws-server/server.js:**

| Aspect | ws-server | uws-server |
|--------|-----------|------------|
| Server creation | `http.createServer()` + `new WebSocketServer({ server })` | `uWS.App().ws('/*', {...}).listen(PORT)` |
| Health endpoint | None (PM2 uses `wait_ready`) | `app.get('/health', (res, req) => res.end('ok'))` |
| Client tracking | `wss.on('connection', ...)` | `open: (ws) => { clients.add(ws) }` |
| Client removal | `ws.on('close', ...)` + `ws.on('error', ...)` | `close: (ws) => { clients.delete(ws) }` |
| Send call | `ws.send(data)` | `ws.send(data, false)` (false = not binary) |
| ReadyState check | `ws.readyState !== 1` | No readyState — check `ws.getBufferedAmount()` or just try send |
| Listen callback | `server.listen(PORT, () => ...)` | `app.listen(PORT, (token) => ...)` |
| Shutdown | `server.close()` implicit | `uWS.us_listen_socket_close(listenSocket)` |

**Critical settings:**

```js
uWS.App().ws('/*', {
  compression: 0,                    // no compression (matches ws-server)
  maxPayloadLength: 0,               // unlimited (matches ws maxPayload: 0)
  idleTimeout: 120,                  // 2 min idle timeout
  maxBackpressure: 1024 * 1024,      // 1MB — auto-disconnect slow clients
  upgrade: (res, req, context) => {  // explicit upgrade to attach data
    res.upgrade({}, req.getHeader('sec-websocket-key'),
      req.getHeader('sec-websocket-protocol'),
      req.getHeader('sec-websocket-extensions'), context);
  },
  open: (ws) => { ... },
  close: (ws) => { ... },
  message: (ws, msg, isBinary) => { }  // no-op
})
```

**Send with backpressure check:**

```js
for (let i = 0; i < len; i++) {
  JSON.parse(msgs[i].value.toString());
  const ok = ws.send(msgs[i].value.toString(), false);
  if (!ok) { /* backpressure — treat as dead */ toDelete.push(ws); break; }
  if (i > 0 && i % YIELD_EVERY === 0) await yieldLoop();
}
```

### 3. `uws-server/ecosystem.config.js`

```js
module.exports = {
  apps: [{
    name: "uws-benchmark",
    script: "./server.js",
    instances: 3,
    exec_mode: "cluster",
    max_memory_restart: "16G",
    node_args: "--max-old-space-size=16384",
    env: {
      PORT: 8091,
      KAFKA_BROKER: "192.168.0.5:9091",
      KAFKA_TOPIC: "benchmark-messages",
      UV_THREADPOOL_SIZE: "16",
    },
    kill_timeout: 10000,
    listen_timeout: 10000,
    wait_ready: true,
    max_restarts: 10,
    restart_delay: 4000,
  }],
};
```

**Changes from ws-server/ecosystem.config.js:**
- `name`: `uws-benchmark` (not `ws-benchmark`)
- `PORT`: `8091` (not `8090`)

## Acceptance Criteria

- [ ] `cd uws-server && npm install` succeeds (uWebSockets.js from GitHub)
- [ ] `node server.js` starts, logs "Listening on :8091", processes `process.send("ready")`
- [ ] `GET /health` returns `ok` (for Docker health checks)
- [ ] WS client connects, receives Kafka messages
- [ ] JSON.parse validation runs on each message
- [ ] Yield every 2k messages via setImmediate
- [ ] Graceful shutdown on SIGINT/SIGTERM
- [ ] PM2 cluster mode: `pm2 start ecosystem.config.js` starts 3 workers, all share port 8091
- [ ] Each PM2 worker gets unique Kafka group: `uws-benchmark-worker-0`, `uws-benchmark-worker-1`, `uws-benchmark-worker-2`
