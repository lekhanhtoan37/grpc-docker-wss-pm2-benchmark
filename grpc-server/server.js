const grpc = require("@grpc/grpc-js");
const protoLoader = require("@grpc/proto-loader");
const Kafka = require("node-rdkafka");

const PROTO_PATH = process.env.PROTO_PATH || "/app/proto/benchmark.proto";
const BROKER = process.env.KAFKA_BROKER || "host.docker.internal:9091";
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

const activeStreams = new Set();

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
  console.log(`[grpc:${CONTAINER_ID}] Kafka consumer ready (group: ${GROUP_ID})`);
  consumer.subscribe([TOPIC]);
  consumer.consume();
});

consumer.on("data", (message) => {
  const raw = JSON.parse(message.value.toString());
  const response = {
    timestamp: raw.timestamp,
    seq: raw.seq,
    payload: message.value.toString(),
  };
  const toDelete = [];
  for (const call of activeStreams) {
    try {
      call.write(response);
    } catch {
      toDelete.push(call);
    }
  }
  for (const c of toDelete) activeStreams.delete(c);
});

consumer.on("event.error", (err) => {
  console.error(`[grpc:${CONTAINER_ID}] Kafka error: ${err.message}`);
});

function streamMessages(call) {
  activeStreams.add(call);
  console.log(`[grpc:${CONTAINER_ID}] Stream connected (total: ${activeStreams.size})`);
  call.on("cancelled", () => {
    activeStreams.delete(call);
    console.log(`[grpc:${CONTAINER_ID}] Stream cancelled (total: ${activeStreams.size})`);
  });
}

async function run() {
  const server = new grpc.Server();
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

  consumer.connect();
}

const shutdown = () => {
  console.log(`[grpc:${CONTAINER_ID}] Shutting down`);
  consumer.disconnect();
  process.exit(0);
};

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

run();
