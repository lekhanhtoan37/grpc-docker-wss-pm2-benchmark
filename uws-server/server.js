const uWS = require("uWebSockets.js");
const { Kafka } = require("kafkajs");

const PORT = parseInt(process.env.PORT || "8091", 10);
const BROKER = process.env.KAFKA_BROKER || "192.168.0.9:9091";
const TOPIC = process.env.KAFKA_TOPIC || "benchmark-messages";
const INSTANCE = process.env.NODE_APP_INSTANCE || process.env.CONTAINER_ID || process.env.HOSTNAME || process.pid;
const GROUP_ID = `uws-benchmark-worker-${INSTANCE}`;
const MAX_BACKPRESSURE = 256 * 1024 * 1024;
const BATCH_MAX = parseInt(process.env.BATCH_MAX || "500", 10);
const LINGER_MS = parseInt(process.env.LINGER_MS || "5", 10);

const ts = () => new Date().toISOString();
const log = (...a) => console.log(`[${ts()}]`, ...a);
const warn = (...a) => console.warn(`[${ts()}]`, ...a);
const errLog = (...a) => console.error(`[${ts()}]`, ...a);

const clients = new Set();
let connCounter = 0;

const kafka = new Kafka({
  brokers: [BROKER],
  clientId: `uws-${INSTANCE}`,
});

const consumer = kafka.consumer({
  groupId: GROUP_ID,
  maxBytesPerPartition: 10485760,
  minBytes: 1,
  maxBytes: 52428800,
  maxWaitTimeInMs: 500,
  sessionTimeout: 30000,
});

let batchBuffer = [];
let lingerTimer = null;
let flushing = false;
let batchCount = 0;
let totalMsgsIn = 0;
let totalMsgsOut = 0;
let totalFlushes = 0;
const startTime = Date.now();

setInterval(() => {
  const elapsed = (Date.now() - startTime) / 1000;
  if (elapsed > 0) {
    log(
      `[uws:${INSTANCE}] batches=${batchCount} in=${totalMsgsIn} out=${totalMsgsOut} ` +
      `flushes=${totalFlushes} ` +
      `in/s=${(totalMsgsIn / elapsed).toFixed(0)} out/s=${(totalMsgsOut / elapsed).toFixed(0)} ` +
      `clients=${clients.size} buffer=${batchBuffer.length}`,
    );
  }
}, 5000);

function safeClose(ws) {
  try {
    clients.delete(ws);
    ws.close();
  } catch {}
}

async function flushToClients() {
  if (flushing || batchBuffer.length === 0) return;
  flushing = true;

  const entries = batchBuffer;
  batchBuffer = [];
  if (lingerTimer) { clearTimeout(lingerTimer); lingerTimer = null; }

  totalFlushes++;
  const len = entries.length;
  totalMsgsOut += len;

  const payload = entries.join("\n");
  const toDelete = [];
  for (const ws of clients) {
    try {
      if (!ws._alive) {
        toDelete.push(ws);
        continue;
      }
      const sendStatus = ws.send(payload, false);
      if (sendStatus === 2) {
        warn(`[uws:${INSTANCE}] conn#${ws._connId} send DROPPED`);
        toDelete.push(ws);
        ws._alive = false;
      } else if (sendStatus === 0) {
        const buffered = ws.getBufferedAmount();
        if (buffered > MAX_BACKPRESSURE * 0.8) {
          warn(`[uws:${INSTANCE}] conn#${ws._connId} backpressure ${(buffered / 1024 / 1024).toFixed(1)}MB (${((buffered / MAX_BACKPRESSURE) * 100).toFixed(0)}%)`);
        }
      }
    } catch {
      toDelete.push(ws);
      ws._alive = false;
    }
  }
  for (const ws of toDelete) {
    safeClose(ws);
  }
  flushing = false;
}

function appendToBuffer(entries) {
  for (const e of entries) {
    batchBuffer.push(e);
  }
  totalMsgsIn += entries.length;

  if (batchBuffer.length >= BATCH_MAX) {
    flushToClients();
    return;
  }

  if (!lingerTimer && !flushing) {
    lingerTimer = setTimeout(() => {
      lingerTimer = null;
      flushToClients();
    }, LINGER_MS);
  }
}

async function startConsumer() {
  await consumer.connect();
  log(`[uws:${INSTANCE}] Kafka consumer connected (group: ${GROUP_ID})`);
  await consumer.subscribe({ topic: TOPIC, fromBeginning: false });
  log(`[uws:${INSTANCE}] Subscribed to ${TOPIC} (linger=${LINGER_MS}ms, batchMax=${BATCH_MAX})`);
  await consumer.run({
    eachBatchAutoResolve: false,
    eachBatch: async ({ batch, resolveOffset }) => {
      if (shuttingDown) return;
      batchCount++;
      const msgs = batch.messages;
      const entries = new Array(msgs.length);
      for (let i = 0; i < msgs.length; i++) {
        entries[i] = msgs[i].value.toString();
      }
      appendToBuffer(entries);

      if (batch.lastOffset) {
        resolveOffset(typeof batch.lastOffset === "function" ? batch.lastOffset() : batch.lastOffset);
      }
    },
  });
}

const retryConsumer = async () => {
  try {
    await startConsumer();
  } catch (e) {
    errLog(`[uws:${INSTANCE}] Kafka consumer error: ${e.message}, restarting in 5s...`);
    setTimeout(retryConsumer, 5000);
  }
};
retryConsumer();

let listenSocket;

const app = uWS
  .App()
  .ws("/*", {
    compression: 0,
    maxPayloadLength: 64 * 1024 * 1024,
    idleTimeout: 120,
    maxBackpressure: MAX_BACKPRESSURE,
    closeOnBackpressureLimit: false,
    upgrade: (res, req, context) => {
      res.upgrade(
        { _connId: ++connCounter, _alive: true },
        req.getHeader("sec-websocket-key"),
        req.getHeader("sec-websocket-protocol"),
        req.getHeader("sec-websocket-extensions"),
        context,
      );
    },
    open: (ws) => {
      clients.add(ws);
      ws._alive = true;
      log(`[uws:${INSTANCE}] conn#${ws._connId} connected (total: ${clients.size})`);
    },
    close: (ws, code, message) => {
      ws._alive = false;
      clients.delete(ws);
      try {
        const buffered = ws.getBufferedAmount();
        log(`[uws:${INSTANCE}] conn#${ws._connId} closed code=${code} buffered=${(buffered / 1024 / 1024).toFixed(2)}MB (total: ${clients.size})`);
      } catch {
        log(`[uws:${INSTANCE}] conn#${ws._connId} closed code=${code} (total: ${clients.size})`);
      }
    },
    drain: (ws) => {
      const buffered = ws.getBufferedAmount();
      log(`[uws:${INSTANCE}] conn#${ws._connId} drain buffered=${(buffered / 1024 / 1024).toFixed(2)}MB`);
    },
    message: () => {},
  })
  .get("/health", (res) => {
    res.end("ok");
  })
  .listen(PORT, (token) => {
    if (token) {
      listenSocket = token;
      log(`[uws:${INSTANCE}] Listening on :${PORT}`);
      if (process.send) process.send("ready");
    } else {
      errLog(`[uws:${INSTANCE}] Failed to listen on :${PORT}`);
      process.exit(1);
    }
  });

let shuttingDown = false;

const shutdown = async () => {
  if (shuttingDown) return;
  shuttingDown = true;
  if (lingerTimer) clearTimeout(lingerTimer);
  await flushToClients();
  log(`[uws:${INSTANCE}] Shutting down, closing ${clients.size} clients...`);
  for (const ws of clients) {
    try { ws.end(1001, "server shutdown"); } catch {}
  }
  setTimeout(async () => {
    await consumer.disconnect().catch(() => {});
    if (listenSocket) {
      uWS.us_listen_socket_close(listenSocket);
    }
    process.exit(0);
  }, 1000);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
