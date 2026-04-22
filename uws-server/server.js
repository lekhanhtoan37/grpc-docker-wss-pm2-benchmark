const uWS = require("uWebSockets.js");
const { Kafka } = require("kafkajs");

const PORT = parseInt(process.env.PORT || "8091", 10);
const BROKER = process.env.KAFKA_BROKER || "192.168.0.9:9091";
const TOPIC = process.env.KAFKA_TOPIC || "benchmark-messages";
const INSTANCE = process.env.NODE_APP_INSTANCE || process.env.CONTAINER_ID || process.pid;
const GROUP_ID = `uws-benchmark-worker-${INSTANCE}`;
const MAX_BACKPRESSURE = 128 * 1024 * 1024;
const WARN_THRESHOLD = 0.8;

const clients = new Set();
let connCounter = 0;
const YIELD_EVERY = 2000;
const yieldLoop = () => new Promise((r) => setImmediate(r));

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

function safeClose(ws) {
  try {
    clients.delete(ws);
    ws.close();
  } catch {}
}

async function startConsumer() {
  await consumer.connect();
  console.log(`[uws:${INSTANCE}] Kafka consumer connected (group: ${GROUP_ID})`);
  await consumer.subscribe({ topic: TOPIC, fromBeginning: false });
  console.log(`[uws:${INSTANCE}] Subscribed to ${TOPIC}`);
  await consumer.run({
    eachBatchAutoResolve: false,
    eachBatch: async ({ batch, resolveOffset }) => {
      if (shuttingDown) return;
      const msgs = batch.messages;
      const len = msgs.length;
      const toDelete = [];
      for (const ws of clients) {
        try {
          if (!ws._alive) {
            toDelete.push(ws);
            continue;
          }
          for (let i = 0; i < len; i++) {
            const payload = msgs[i].value.toString();
            const sendStatus = ws.send(payload, false);
            if (sendStatus === 2) {
              const buffered = ws.getBufferedAmount();
              console.warn(`[uws:${INSTANCE}] conn#${ws._connId} send DROPPED (status=2), buffered=${(buffered / 1024 / 1024).toFixed(2)}MB`);
              toDelete.push(ws);
              ws._alive = false;
              break;
            }
            if (sendStatus === 0 && i > 0 && i % YIELD_EVERY === 0) {
              const buffered = ws.getBufferedAmount();
              if (buffered >= MAX_BACKPRESSURE) {
                console.warn(`[uws:${INSTANCE}] conn#${ws._connId} backpressure full: ${(buffered / 1024 / 1024).toFixed(2)}MB, closing`);
                toDelete.push(ws);
                ws._alive = false;
                break;
              }
              if (buffered > MAX_BACKPRESSURE * WARN_THRESHOLD && !ws._warned80) {
                console.warn(`[uws:${INSTANCE}] conn#${ws._connId} backpressure >=80%: ${(buffered / 1024 / 1024).toFixed(2)}MB / ${(MAX_BACKPRESSURE / 1024 / 1024).toFixed(0)}MB`);
                ws._warned80 = true;
              }
              if (buffered < MAX_BACKPRESSURE * WARN_THRESHOLD) {
                ws._warned80 = false;
              }
              await yieldLoop();
            }
          }
        } catch (e) {
          console.warn(`[uws:${INSTANCE}] conn#${ws._connId} send error: ${e.message}`);
          toDelete.push(ws);
          ws._alive = false;
        }
      }
      for (const ws of toDelete) {
        safeClose(ws);
      }
      if (batch.lastOffset) {
        resolveOffset(typeof batch.lastOffset === "function" ? batch.lastOffset() : batch.lastOffset);
      }
    },
  });
}

const retryConsumer = async () => {
  try {
    await startConsumer();
  } catch (err) {
    console.error(`[uws:${INSTANCE}] Kafka consumer error: ${err.message}, restarting in 5s...`);
    setTimeout(retryConsumer, 5000);
  }
};
retryConsumer();

let listenSocket;

const app = uWS
  .App()
  .ws("/*", {
    compression: 0,
    maxPayloadLength: 16 * 1024 * 1024,
    idleTimeout: 120,
    maxBackpressure: MAX_BACKPRESSURE,
    closeOnBackpressureLimit: false,
    upgrade: (res, req, context) => {
      res.upgrade(
        { _connId: ++connCounter, _warned80: false, _alive: true },
        req.getHeader("sec-websocket-key"),
        req.getHeader("sec-websocket-protocol"),
        req.getHeader("sec-websocket-extensions"),
        context,
      );
    },
    open: (ws) => {
      clients.add(ws);
      ws._alive = true;
      console.log(`[uws:${INSTANCE}] conn#${ws._connId} connected (total: ${clients.size})`);
    },
    close: (ws) => {
      const buffered = ws.getBufferedAmount();
      ws._alive = false;
      clients.delete(ws);
      console.log(`[uws:${INSTANCE}] conn#${ws._connId} closed by client, buffered=${(buffered / 1024 / 1024).toFixed(2)}MB (total: ${clients.size})`);
    },
    message: () => {},
  })
  .get("/health", (res) => {
    res.end("ok");
  })
  .listen(PORT, (token) => {
    if (token) {
      listenSocket = token;
      console.log(`[uws:${INSTANCE}] Listening on :${PORT}`);
      if (process.send) process.send("ready");
    } else {
      console.error(`[uws:${INSTANCE}] Failed to listen on :${PORT}`);
      process.exit(1);
    }
  });

let shuttingDown = false;

const shutdown = async () => {
  if (shuttingDown) return;
  shuttingDown = true;
  console.log(`[uws:${INSTANCE}] Shutting down, closing ${clients.size} clients...`);
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
