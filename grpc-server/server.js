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

async function startConsumer() {
  await consumer.connect();
  console.log(`[grpc:${CONTAINER_ID}] Kafka consumer connected (group: ${GROUP_ID})`);
  await consumer.subscribe({ topic: TOPIC, fromBeginning: false });
  console.log(`[grpc:${CONTAINER_ID}] Subscribed to ${TOPIC}`);
  await consumer.run({
    eachBatchAutoResolve: false,
    eachBatch: async ({ batch }) => {
      const toDelete = [];
      for (const [call, paused] of activeStreams) {
        if (paused) continue;
        try {
          for (const message of batch.messages) {
            const raw = JSON.parse(message.value.toString());
            const ok = call.write({
              timestamp: raw.timestamp,
              seq: raw.seq,
              payload: message.value.toString(),
            });
            if (!ok) {
              activeStreams.set(call, true);
              break;
            }
          }
        } catch {
          toDelete.push(call);
        }
      }
      for (const c of toDelete) {
        activeStreams.delete(c);
      }
    },
  });
}

function streamMessages(call) {
  activeStreams.set(call, false);
  console.log(`[grpc:${CONTAINER_ID}] Stream connected (total: ${activeStreams.size})`);
  call.on("cancelled", () => {
    activeStreams.delete(call);
    console.log(`[grpc:${CONTAINER_ID}] Stream cancelled (total: ${activeStreams.size})`);
  });
  call.on("drain", () => {
    activeStreams.set(call, false);
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
    }
  );

  startConsumer().catch((err) => {
    console.error(`[grpc:${CONTAINER_ID}] Kafka consumer error: ${err.message}`);
  });
}

const shutdown = async () => {
  console.log(`[grpc:${CONTAINER_ID}] Shutting down`);
  await consumer.disconnect().catch(() => {});
  process.exit(0);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

run();
