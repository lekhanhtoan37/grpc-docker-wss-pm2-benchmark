const Kafka = require("node-rdkafka");

const BROKER = process.env.KAFKA_BROKER || "localhost:9092";
const TOPIC = process.env.KAFKA_TOPIC || "benchmark-messages";
const TARGET_MBPS = parseInt(process.env.TARGET_MBPS || "1000", 10);
const MSG_SIZE = parseInt(process.env.MSG_SIZE || "1024", 10);
const NUM_PARTITIONS = parseInt(process.env.NUM_PARTITIONS || "12", 10);

const PAYLOAD_OVERHEAD = 40;
const PADDING = "x".repeat(MSG_SIZE - PAYLOAD_OVERHEAD);

const producer = new Kafka.HighLevelProducer({
  "metadata.broker.list": BROKER,
  "client.id": "benchmark-producer-rdkafka",
  "compression.codec": "lz4",
  "batch.size": 262144,
  "linger.ms": 20,
  "acks": 1,
  "queue.buffering.max.messages": 1000000,
  "queue.buffering.max.kbytes": 524288,
  "max.in.flight.requests.per.connection": 10,
  "message.max.bytes": 10485760,
  "enable.idempotence": false,
});

let seq = 0;
let bytesSent = 0;
let messagesSent = 0;
let startTime = null;

producer.on("ready", () => {
  console.log(`[producer] Connected to ${BROKER}`);
  console.log(`[producer] Target: ${TARGET_MBPS} MB/s, msg size: ${MSG_SIZE} bytes`);
  startTime = Date.now();
  produceLoop();
});

producer.on("delivery-report", (err) => {
  if (err) console.error(`[producer] Delivery error: ${err.message}`);
});

producer.on("event.error", (err) => {
  console.error(`[producer] Error: ${err.message}`);
});

function produceLoop() {
  const BATCH_SIZE = 500;
  const messages = [];

  for (let i = 0; i < BATCH_SIZE; i++) {
    const now = Date.now();
    const msg = JSON.stringify({ timestamp: now, seq: seq++, data: PADDING });
    messages.push({
      value: Buffer.from(msg),
      key: `msg-${seq}`,
      partition: seq % NUM_PARTITIONS,
    });
    bytesSent += msg.length;
  }
  messagesSent += BATCH_SIZE;

  producer.produceBatch(TOPIC, -1, messages);

  if (messagesSent % 100000 === 0) {
    const elapsed = (Date.now() - startTime) / 1000;
    const mbps = (bytesSent / 1024 / 1024 / elapsed).toFixed(1);
    const rate = (messagesSent / elapsed).toFixed(0);
    console.log(`[producer] ${messagesSent} msgs, ${mbps} MB/s (${rate} msg/s)`);
  }

  const elapsed = (Date.now() - startTime) / 1000;
  const currentMBps = bytesSent / 1024 / 1024 / elapsed;
  const delay = currentMBps > TARGET_MBPS ? 1 : 0;

  setTimeout(produceLoop, delay);
}

const shutdown = () => {
  const elapsed = ((Date.now() - startTime) / 1000).toFixed(1);
  const mbps = (bytesSent / 1024 / 1024 / elapsed).toFixed(1);
  console.log(`\n[producer] Shutdown. ${messagesSent} msgs in ${elapsed}s (${mbps} MB/s)`);
  producer.disconnect();
  process.exit(0);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

producer.connect();
