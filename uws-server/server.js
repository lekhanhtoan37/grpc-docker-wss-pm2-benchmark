const uWS = require("uWebSockets.js");
const { Kafka } = require("kafkajs");

const PORT = parseInt(process.env.PORT || "8091", 10);
const BROKER = process.env.KAFKA_BROKER || "192.168.0.9:9091";
const TOPIC = process.env.KAFKA_TOPIC || "benchmark-messages";
const INSTANCE = process.env.NODE_APP_INSTANCE || process.env.CONTAINER_ID || process.pid;
const GROUP_ID = `uws-benchmark-worker-${INSTANCE}`;

const clients = new Set();
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

async function startConsumer() {
  await consumer.connect();
  console.log(`[uws:${INSTANCE}] Kafka consumer connected (group: ${GROUP_ID})`);
  await consumer.subscribe({ topic: TOPIC, fromBeginning: false });
  console.log(`[uws:${INSTANCE}] Subscribed to ${TOPIC}`);
  await consumer.run({
    eachBatchAutoResolve: false,
    eachBatch: async ({ batch, resolveOffset }) => {
      const msgs = batch.messages;
      const len = msgs.length;
      const toDelete = [];
      for (const ws of clients) {
        try {
          for (let i = 0; i < len; i++) {
            JSON.parse(msgs[i].value.toString());
            const ok = ws.send(msgs[i].value.toString(), false);
            if (!ok) {
              toDelete.push(ws);
              break;
            }
            if (i > 0 && i % YIELD_EVERY === 0) await yieldLoop();
          }
        } catch {
          toDelete.push(ws);
        }
      }
      for (const ws of toDelete) {
        clients.delete(ws);
        ws.close();
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
    maxBackpressure: 128 * 1024 * 1024,
    closeOnBackpressureLimit: false,
    upgrade: (res, req, context) => {
      res.upgrade(
        {},
        req.getHeader("sec-websocket-key"),
        req.getHeader("sec-websocket-protocol"),
        req.getHeader("sec-websocket-extensions"),
        context,
      );
    },
    open: (ws) => {
      clients.add(ws);
      console.log(`[uws:${INSTANCE}] Client connected (total: ${clients.size})`);
    },
    close: (ws) => {
      clients.delete(ws);
      console.log(`[uws:${INSTANCE}] Client disconnected (total: ${clients.size})`);
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

const shutdown = async () => {
  console.log(`[uws:${INSTANCE}] Shutting down`);
  for (const ws of clients) ws.close();
  await consumer.disconnect().catch(() => {});
  if (listenSocket) {
    uWS.us_listen_socket_close(listenSocket);
  }
  process.exit(0);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
