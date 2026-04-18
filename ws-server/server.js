const http = require("http");
const { WebSocketServer } = require("ws");
const { Kafka } = require("kafkajs");

const PORT = parseInt(process.env.PORT || "8080", 10);
const BROKER = process.env.KAFKA_BROKER || "localhost:9092";
const TOPIC = process.env.KAFKA_TOPIC || "benchmark-messages";
const INSTANCE = process.env.NODE_APP_INSTANCE || process.pid;
const GROUP_ID = `ws-benchmark-worker-${INSTANCE}`;

const kafka = new Kafka({
  clientId: `ws-server-${INSTANCE}`,
  brokers: [BROKER],
});

const consumer = kafka.consumer({ groupId: GROUP_ID });
const clients = new Set();

async function run() {
  await consumer.connect();
  console.log(`[ws:${INSTANCE}] Kafka connected (group: ${GROUP_ID})`);

  await consumer.subscribe({ topic: TOPIC, fromBeginning: false });
  console.log(`[ws:${INSTANCE}] Subscribed to ${TOPIC}`);

  await consumer.run({
    eachMessage: async ({ message }) => {
      const payload = message.value.toString();
      for (const ws of clients) {
        if (ws.readyState === 1) {
          ws.send(payload);
        }
      }
    },
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
}

const shutdown = async () => {
  console.log(`[ws:${INSTANCE}] Shutting down`);
  for (const ws of clients) ws.close();
  await consumer.disconnect();
  process.exit(0);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

run().catch((err) => {
  console.error(`[ws:${INSTANCE}] Fatal: ${err.message}`);
  process.exit(1);
});
