const http = require("http");
const { WebSocketServer } = require("ws");
const { Kafka } = require("kafkajs");

const PORT = parseInt(process.env.PORT || "8080", 10);
const BROKER = process.env.KAFKA_BROKER || "192.168.0.9:9091";
const TOPIC = process.env.KAFKA_TOPIC || "benchmark-messages";
const INSTANCE = process.env.NODE_APP_INSTANCE || process.pid;
const GROUP_ID = `ws-benchmark-worker-${INSTANCE}`;
const BATCH_MAX = parseInt(process.env.BATCH_MAX || "20", 10);
const LINGER_MS = parseInt(process.env.LINGER_MS || "5", 10);

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
    console.log(
      `[ws:${INSTANCE}] batches=${batchCount} in=${totalMsgsIn} out=${totalMsgsOut} ` +
      `flushes=${totalFlushes} ` +
      `in/s=${(totalMsgsIn / elapsed).toFixed(0)} out/s=${(totalMsgsOut / elapsed).toFixed(0)} ` +
      `clients=${clients.size} buffer=${batchBuffer.length}`,
    );
  }
}, 5000);

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
    if (ws.readyState !== 1) continue;
    try {
      ws.send(payload);
    } catch {
      toDelete.push(ws);
    }
  }
  for (const ws of toDelete) {
    clients.delete(ws);
    try { ws.close(); } catch {}
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
  console.log(`[ws:${INSTANCE}] Kafka consumer connected (group: ${GROUP_ID})`);
  await consumer.subscribe({ topic: TOPIC, fromBeginning: false });
  console.log(`[ws:${INSTANCE}] Subscribed to ${TOPIC} (linger=${LINGER_MS}ms, batchMax=${BATCH_MAX})`);
  await consumer.run({
    eachBatchAutoResolve: false,
    eachBatch: async ({ batch, resolveOffset }) => {
      if (shuttingDown) return;
      const msgs = batch.messages;
      batchCount++;
      const entries = new Array(msgs.length);
      for (let i = 0; i < msgs.length; i++) {
        entries[i] = msgs[i].value.toString();
      }
      appendToBuffer(entries);

      if (batch.lastOffset) {
        resolveOffset(typeof batch.lastOffset === 'function' ? batch.lastOffset() : batch.lastOffset);
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

let shuttingDown = false;

const shutdown = async () => {
  if (shuttingDown) return;
  shuttingDown = true;
  if (lingerTimer) clearTimeout(lingerTimer);
  await flushToClients();
  console.log(`[ws:${INSTANCE}] Shutting down, closing ${clients.size} clients...`);
  for (const ws of clients) {
    try { ws.close(1001, "server shutdown"); } catch {}
  }
  setTimeout(async () => {
    await consumer.disconnect().catch(() => {});
    server.close(() => process.exit(0));
    setTimeout(() => process.exit(0), 2000);
  }, 1000);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
