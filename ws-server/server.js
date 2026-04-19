const http = require("http");
const { WebSocketServer } = require("ws");
const { Kafka } = require("kafkajs");

const PORT = parseInt(process.env.PORT || "8080", 10);
const BROKER = process.env.KAFKA_BROKER || "192.168.0.5:9091";
const TOPIC = process.env.KAFKA_TOPIC || "benchmark-messages";
const INSTANCE = process.env.NODE_APP_INSTANCE || process.pid;
const GROUP_ID = `ws-benchmark-worker-${INSTANCE}`;

const clients = new Set();

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
    eachBatch: async ({ batch }) => {
      const payload = [];
      for (const message of batch.messages) {
        payload.push(message.value);
      }
      const raw = payload.length === 1 ? payload[0].toString() : null;
      for (const ws of clients) {
        if (ws.readyState === 1) {
          if (raw) {
            ws.send(raw);
          } else {
            for (const p of payload) ws.send(p);
          }
        }
      }
    },
  });
}

startConsumer().catch((err) => {
  console.error(`[ws:${INSTANCE}] Kafka consumer error: ${err.message}`);
  process.exit(1);
});

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

const shutdown = async () => {
  console.log(`[ws:${INSTANCE}] Shutting down`);
  for (const ws of clients) ws.close();
  await consumer.disconnect().catch(() => {});
  process.exit(0);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
