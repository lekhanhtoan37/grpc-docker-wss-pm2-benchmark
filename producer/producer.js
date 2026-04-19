const { Kafka } = require("kafkajs");

const BROKER = process.env.KAFKA_BROKER || "127.0.0.1:9091";
const TOPIC = "benchmark-messages";
const RATE = 100;
const INTERVAL_MS = 1000 / RATE;
const PAYLOAD_SIZE = 1024;

const kafka = new Kafka({
  clientId: "benchmark-producer",
  brokers: [BROKER],
});

const producer = kafka.producer();
let seq = 0;
let sent = 0;
let startTime = Date.now();

async function run() {
  await producer.connect();
  console.log(`[producer] Connected to ${BROKER}`);

  const overhead = JSON.stringify({ timestamp: 0, seq: 0, data: "" }).length;
  const padding = "x".repeat(Math.max(0, PAYLOAD_SIZE - overhead));

  const timer = setInterval(async () => {
    const now = Date.now();
    const msg = { timestamp: now, seq, data: padding };
    seq++;

    try {
      await producer.send({
        topic: TOPIC,
        messages: [{ key: `msg-${msg.seq}`, value: JSON.stringify(msg) }],
        acks: 1,
      });
    } catch (err) {
      console.error(`[producer] Send error: ${err.message}`);
    }

    sent++;
    if (sent % 1000 === 0) {
      const elapsed = (Date.now() - startTime) / 1000;
      const rate = (sent / elapsed).toFixed(1);
      console.log(`[producer] ${sent} messages sent (${rate} msg/s)`);
    }
  }, INTERVAL_MS);

  const shutdown = async () => {
    clearInterval(timer);
    console.log(`\n[producer] Shutting down. Total sent: ${sent}`);
    await producer.disconnect();
    process.exit(0);
  };

  process.on("SIGINT", shutdown);
  process.on("SIGTERM", shutdown);
}

run().catch((err) => {
  console.error(`[producer] Fatal: ${err.message}`);
  process.exit(1);
});
