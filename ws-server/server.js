const http = require("http");
const { WebSocketServer } = require("ws");
const Kafka = require("node-rdkafka");

const PORT = parseInt(process.env.PORT || "8080", 10);
const BROKER = process.env.KAFKA_BROKER || "127.0.0.1:9091";
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
