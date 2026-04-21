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

const activeStreams = new Map();

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
      `drains=${drainWaits} in/s=${(totalMsgsIn / elapsed).toFixed(0)} out/s=${(totalMsgsOut / elapsed).toFixed(0)} streams=${activeStreams.size}`,
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

function createStreamWriter(call) {
  let dead = false;
  let drainResolve = null;

  const resolveDrain = () => {
    if (drainResolve) {
      const r = drainResolve;
      drainResolve = null;
      r();
    }
  };

  call.on("cancelled", () => { dead = true; resolveDrain(); });
  call.on("error", () => { dead = true; resolveDrain(); });

  return {
    get dead() { return dead; },
    async writeBatch(msgs) {
      if (dead) return;
      for (let i = 0; i < msgs.length; i++) {
        if (dead) return;
        try {
          const ok = call.write(msgs[i]);
          if (!ok && !dead) {
            drainWaits++;
            await new Promise((r) => {
              drainResolve = r;
              call.once("drain", resolveDrain);
            });
          }
          totalMsgsOut++;
        } catch {
          dead = true;
          return;
        }
      }
    },
  };
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

      const grpcMsgs = new Array(len);
      for (let i = 0; i < len; i++) {
        const parsed = parsePayload(msgs[i].value);
        grpcMsgs[i] = {
          timestamp: parsed ? parsed.timestamp : Number(msgs[i].timestamp) || 0,
          seq: parsed ? parsed.seq || 0 : 0,
          payload: msgs[i].value,
        };
      }

      const dead = [];
      const promises = [];
      for (const [call, writer] of activeStreams) {
        if (writer.dead) { dead.push(call); continue; }
        promises.push(writer.writeBatch(grpcMsgs));
      }
      await Promise.all(promises);

      for (const c of dead) activeStreams.delete(c);
      for (const [call, writer] of activeStreams) {
        if (writer.dead) activeStreams.delete(call);
      }

      if (batch.lastOffset) {
        resolveOffset(typeof batch.lastOffset === 'function' ? batch.lastOffset() : batch.lastOffset);
      }
    },
  });
}

function streamMessages(call) {
  const writer = createStreamWriter(call);
  activeStreams.set(call, writer);
  console.log(`[grpc:${CONTAINER_ID}] Stream connected (total: ${activeStreams.size})`);
  call.on("cancelled", () => {
    activeStreams.delete(call);
    console.log(`[grpc:${CONTAINER_ID}] Stream cancelled (total: ${activeStreams.size})`);
  });
  call.on("error", () => {
    activeStreams.delete(call);
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
  for (const [call] of activeStreams) {
    try { call.end(); } catch {}
  }
  await consumer.disconnect().catch(() => {});
  process.exit(0);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

run();
