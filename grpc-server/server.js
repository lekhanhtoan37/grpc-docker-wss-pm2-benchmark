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

const packageDef = protoLoader.loadSync(PROTO_PATH, {
  keepCase: true,
  longs: String,
  enums: String,
  defaults: true,
  oneofs: true,
});
const benchmarkProto = grpc.loadPackageDefinition(packageDef).benchmark;

const activeCalls = new Set();

let batchCount = 0;
let totalMsgsIn = 0;
let totalMsgsOut = 0;
let drainWaits = 0;
const startTime = Date.now();

setInterval(() => {
  const elapsed = (Date.now() - startTime) / 1000;
  if (elapsed > 0) {
    console.log(
      `[grpc:${CONTAINER_ID}] batches=${batchCount} in=${totalMsgsIn} out=${totalMsgsOut} ` +
      `drains=${drainWaits} in/s=${(totalMsgsIn / elapsed).toFixed(0)} out/s=${(totalMsgsOut / elapsed).toFixed(0)} streams=${activeCalls.size}`,
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

      const entries = new Array(len);
      for (let i = 0; i < len; i++) {
        const parsed = parsePayload(msgs[i].value);
        entries[i] = {
          timestamp: parsed ? parsed.timestamp : Number(msgs[i].timestamp) || 0,
          seq: parsed ? parsed.seq || 0 : 0,
          payload: msgs[i].value,
        };
      }

      const grpcBatch = { messages: entries };

      const dead = [];
      for (const call of activeCalls) {
        try {
          const ok = call.write(grpcBatch);
          if (!ok) {
            drainWaits++;
            await new Promise((r) => {
              call.once("drain", r);
            });
          }
          totalMsgsOut += len;
        } catch {
          dead.push(call);
        }
      }

      for (const c of dead) activeCalls.delete(c);

      if (batch.lastOffset) {
        resolveOffset(typeof batch.lastOffset === 'function' ? batch.lastOffset() : batch.lastOffset);
      }
    },
  });
}

function streamMessages(call) {
  activeCalls.add(call);
  console.log(`[grpc:${CONTAINER_ID}] Stream connected (total: ${activeCalls.size})`);
  call.on("cancelled", () => {
    activeCalls.delete(call);
    console.log(`[grpc:${CONTAINER_ID}] Stream cancelled (total: ${activeCalls.size})`);
  });
  call.on("error", () => {
    activeCalls.delete(call);
  });
}

async function run() {
  const server = new grpc.Server({
    "grpc.max_receive_message_length": 104857600,
    "grpc.max_send_message_length": 104857600,
    "grpc.http2.max_frame_size": 16777215,
    "grpc.http2.initial_window_size": 671088640,
    "grpc.http2.initial_connection_window_size": 1342177280,
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
  for (const call of activeCalls) {
    try { call.end(); } catch {}
  }
  await consumer.disconnect().catch(() => {});
  process.exit(0);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

run();
