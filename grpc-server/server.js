const grpc = require("@grpc/grpc-js");
const protoLoader = require("@grpc/proto-loader");
const { Kafka } = require("kafkajs");

const PROTO_PATH = process.env.PROTO_PATH || "/app/proto/benchmark.proto";
const BROKER = process.env.KAFKA_BROKER || "192.168.0.5:9091";
const TOPIC = process.env.KAFKA_TOPIC || "benchmark-messages";
const CONTAINER_ID = process.env.CONTAINER_ID || "default";
const GROUP_ID = `grpc-benchmark-${CONTAINER_ID}`;
const PORT = parseInt(process.env.GRPC_PORT || "50051", 10);
const HOST = process.env.GRPC_HOST || "0.0.0.0";
const DRAIN_TIMEOUT_MS = parseInt(process.env.DRAIN_TIMEOUT_MS || "5000", 10);
const YIELD_EVERY = parseInt(process.env.YIELD_EVERY || "2000", 10);

const packageDef = protoLoader.loadSync(PROTO_PATH, {
  keepCase: true,
  longs: String,
  enums: String,
  defaults: true,
  oneofs: true,
});
const benchmarkProto = grpc.loadPackageDefinition(packageDef).benchmark;

const activeStreams = new Map();

let batchCount = 0;
let totalMsgsIn = 0;
let totalMsgsOut = 0;
let drainWaits = 0;
let droppedMsgs = 0;
const startTime = Date.now();

setInterval(() => {
  const elapsed = (Date.now() - startTime) / 1000;
  if (elapsed > 0) {
    console.log(
      `[grpc:${CONTAINER_ID}] batches=${batchCount} in=${totalMsgsIn} out=${totalMsgsOut} ` +
      `drains=${drainWaits} drops=${droppedMsgs} in/s=${(totalMsgsIn / elapsed).toFixed(0)} out/s=${(totalMsgsOut / elapsed).toFixed(0)} streams=${activeStreams.size}`,
    );
  }
}, 5000);

const kafka = new Kafka({
  brokers: [BROKER],
  clientId: `grpc-${CONTAINER_ID}`,
});

const consumer = kafka.consumer({
  groupId: GROUP_ID,
  maxBytesPerPartition: 10485760,
  minBytes: 1,
  maxBytes: 52428800,
  maxWaitTimeInMs: 500,
  sessionTimeout: 30000,
});

class StreamWriter {
  constructor(call) {
    this.call = call;
    this.queue = [];
    this.flushing = false;
    this.dead = false;
    this.sentCount = 0;
    this.dropCount = 0;
    this._onDrain = null;
    this._drainTimer = null;

    call.on("cancelled", () => {
      this.dead = true;
      this._resolveDrain();
    });
    call.on("error", () => {
      this.dead = true;
      this._resolveDrain();
    });
  }

  _resolveDrain() {
    if (this._onDrain) {
      if (this._drainTimer) clearTimeout(this._drainTimer);
      const r = this._onDrain;
      this._onDrain = null;
      this._drainTimer = null;
      r();
    }
  }

  enqueue(msg) {
    if (this.dead) {
      this.dropCount++;
      return;
    }
    this.queue.push(msg);
  }

  async flush() {
    if (this.flushing || this.dead) return;
    this.flushing = true;
    const items = this.queue;
    this.queue = [];

    for (let i = 0; i < items.length; i++) {
      if (this.dead) {
        this.dropCount += items.length - i;
        break;
      }
      try {
        const ok = this.call.write(items[i]);
        if (!ok) {
          drainWaits++;
          await new Promise((r) => {
            this._onDrain = r;
            this.call.once("drain", () => this._resolveDrain());
            this._drainTimer = setTimeout(() => {
              console.warn(`[grpc:${CONTAINER_ID}] Drain timeout (${DRAIN_TIMEOUT_MS}ms), dropping stream`);
              this.dead = true;
              this._resolveDrain();
            }, DRAIN_TIMEOUT_MS);
          });
        }
        if (!this.dead) {
          this.sentCount++;
          totalMsgsOut++;
        }
      } catch {
        this.dead = true;
        this.dropCount += items.length - i;
        break;
      }
    }
    this.flushing = false;
  }
}

function parsePayload(buf) {
  try {
    return JSON.parse(buf.toString());
  } catch {
    return null;
  }
}

async function startConsumer() {
  await consumer.connect();
  console.log(`[grpc:${CONTAINER_ID}] Kafka consumer connected (group: ${GROUP_ID})`);
  await consumer.subscribe({ topic: TOPIC, fromBeginning: false });
  console.log(`[grpc:${CONTAINER_ID}] Subscribed to ${TOPIC}`);
  await consumer.run({
    eachBatchAutoResolve: false,
    eachBatch: async ({ batch, resolveOffset }) => {
      const msgs = batch.messages;
      const len = msgs.length;
      batchCount++;
      totalMsgsIn += len;

      let msgIdx = 0;
      for (const writer of activeStreams.values()) {
        for (let i = 0; i < len; i++) {
          const parsed = parsePayload(msgs[i].value);
          writer.enqueue({
            timestamp: parsed ? parsed.timestamp : Number(msgs[i].timestamp) || 0,
            seq: parsed ? parsed.seq || 0 : 0,
            payload: msgs[i].value,
          });
        }
      }

      const writers = [...activeStreams.values()];
      await Promise.all(writers.map((w) => w.flush()));

      const toDelete = [];
      for (const [call, writer] of activeStreams) {
        if (writer.dead) {
          droppedMsgs += writer.dropCount;
          toDelete.push(call);
        }
      }
      for (const c of toDelete) activeStreams.delete(c);

      if (batch.lastOffset) {
        resolveOffset(batch.lastOffset);
      }
    },
  });
}

function streamMessages(call) {
  const writer = new StreamWriter(call);
  activeStreams.set(call, writer);
  console.log(`[grpc:${CONTAINER_ID}] Stream connected (total: ${activeStreams.size})`);
  call.on("cancelled", () => {
    activeStreams.delete(call);
    console.log(`[grpc:${CONTAINER_ID}] Stream cancelled (total: ${activeStreams.size})`);
  });
  call.on("error", (err) => {
    writer.dead = true;
    activeStreams.delete(call);
    console.log(`[grpc:${CONTAINER_ID}] Stream error: ${err.message} (total: ${activeStreams.size})`);
  });
}

async function run() {
  const server = new grpc.Server({
    "grpc.max_receive_message_length": 10485760,
    "grpc.max_send_message_length": 10485760,
    "grpc.http2.max_frame_size": 16777215,
    "grpc.http2.initial_window_size": 67108864,
    "grpc.http2.initial_connection_window_size": 134217728,
  });
  server.addService(benchmarkProto.BenchmarkService.service, {
    StreamMessages: streamMessages,
  });
  server.bindAsync(
    `${HOST}:${PORT}`,
    grpc.ServerCredentials.createInsecure(),
    (err) => {
      if (err) {
        console.error(`[grpc:${CONTAINER_ID}] Bind failed: ${err.message}`);
        process.exit(1);
      }
      server.start();
      console.log(`[grpc:${CONTAINER_ID}] Listening on ${HOST}:${PORT}`);
    },
  );

  const retryConsumer = async () => {
    try {
      await startConsumer();
    } catch (err) {
      console.error(`[grpc:${CONTAINER_ID}] Kafka consumer error: ${err.message}, restarting in 5s...`);
      setTimeout(retryConsumer, 5000);
    }
  };
  retryConsumer();
}

const shutdown = async () => {
  console.log(`[grpc:${CONTAINER_ID}] Shutting down`);
  for (const [call] of activeStreams) {
    try { call.end(); } catch {}
  }
  await consumer.disconnect().catch(() => {});
  process.exit(0);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

run();
