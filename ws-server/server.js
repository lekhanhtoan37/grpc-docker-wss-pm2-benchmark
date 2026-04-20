const http = require("http");
const { WebSocketServer } = require("ws");
const { Kafka } = require("kafkajs");

const PORT = parseInt(process.env.PORT || "8080", 10);
const BROKER = process.env.KAFKA_BROKER || "192.168.0.5:9091";
const TOPIC = process.env.KAFKA_TOPIC || "benchmark-messages";
const INSTANCE = process.env.NODE_APP_INSTANCE || process.pid;
const GROUP_ID = `ws-benchmark-worker-${INSTANCE}`;

const clients = new Set();
const YIELD_EVERY = 2000;
const yieldLoop = () => new Promise((r) => setImmediate(r));

const kafka = new Kafka({
  brokers: [BROKER],
  clientId: `ws-${INSTANCE}`,
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
  console.log(`[ws:${INSTANCE}] Kafka consumer connected (group: ${GROUP_ID})`);
  await consumer.subscribe({ topic: TOPIC, fromBeginning: false });
  console.log(`[ws:${INSTANCE}] Subscribed to ${TOPIC}`);
  await consumer.run({
    eachBatchAutoResolve: false,
    eachBatch: async ({ batch, resolveOffset }) => {
      const msgs = batch.messages;
      const len = msgs.length;
      const toDelete = [];
      for (const ws of clients) {
        if (ws.readyState !== 1) continue;
        try {
          for (let i = 0; i < len; i++) {
            ws.send(msgs[i].value.toString());
            if (i > 0 && i % YIELD_EVERY === 0) await yieldLoop();
          }
        } catch {
          toDelete.push(ws);
        }
      }
      for (const ws of toDelete) {
        clients.delete(ws);
        try { ws.close(); } catch {}
      }
      if (batch.lastOffset) {
        resolveOffset(batch.lastOffset);
      }
    },
  });
}

const retryConsumer = async () => {
  try {
    await startConsumer();
  } catch (err) {
    console.error(`[ws:${INSTANCE}] Kafka consumer error: ${err.message}, restarting in 5s...`);
    setTimeout(retryConsumer, 5000);
  }
};
retryConsumer();

const server = http.createServer();
const wss = new WebSocketServer({ server, maxPayload: 0 });

wss.on("connection", (ws) => {
  clients.add(ws);
  console.log(`[ws:${INSTANCE}] Client connected (total: ${clients.size})`);
  ws.on("close", () => {
    clients.delete(ws);
    console.log(`[ws:${INSTANCE}] Client disconnected (total: ${clients.size})`);
  });
  ws.on("error", () => {});
});

server.listen(PORT, () => {
  console.log(`[ws:${INSTANCE}] Listening on :${PORT}`);
  if (process.send) process.send("ready");
});

const shutdown = async () => {
  console.log(`[ws:${INSTANCE}] Shutting down`);
  for (const ws of clients) ws.close();
  await consumer.disconnect().catch(() => {});
  process.exit(0);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
