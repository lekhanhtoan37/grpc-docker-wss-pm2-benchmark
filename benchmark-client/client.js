const WebSocket = require("ws");
const grpc = require("@grpc/grpc-js");
const protoLoader = require("@grpc/proto-loader");
const hdr = require("hdr-histogram-js");
const { performance } = require("node:perf_hooks");

const PROTO_PATH = __dirname + "/proto/benchmark.proto";
const WARMUP_DEFAULT = 60;
const DURATION_DEFAULT = 300;
const CONNECT_TIMEOUT = 10000;

const GROUPS = [
  {
    name: "WS (host/PM2)",
    type: "ws",
    endpoints: ["ws://127.0.0.1:8090", "ws://127.0.0.1:8090", "ws://127.0.0.1:8090"],
  },
  {
    name: "gRPC bridge",
    type: "grpc",
    endpoints: ["localhost:50051", "localhost:50052", "localhost:50053"],
  },
  {
    name: "gRPC host",
    type: "grpc",
    endpoints: ["localhost:60051", "localhost:60052", "localhost:60053"],
  },
];

function parseArgs() {
  const args = process.argv.slice(2);
  const parsed = { warmup: WARMUP_DEFAULT, duration: DURATION_DEFAULT };
  for (let i = 0; i < args.length; i++) {
    if (args[i] === "--warmup" && args[i + 1]) parsed.warmup = parseInt(args[i + 1], 10);
    if (args[i] === "--duration" && args[i + 1]) parsed.duration = parseInt(args[i + 1], 10);
  }
  return parsed;
}

function createHistogram() {
  return hdr.build({ lowestDiscernibleValue: 1, highestTrackableValue: 60000000, numberOfSignificantValueDigits: 3 });
}

function mergeHistograms(histograms) {
  const merged = createHistogram();
  for (const h of histograms) merged.add(h);
  return merged;
}

function printPercentile(h) {
  const fmt = (v) => (v / 1e6).toFixed(3);
  return [
    `p50=${fmt(h.getValueAtPercentile(50))}`,
    `p75=${fmt(h.getValueAtPercentile(75))}`,
    `p90=${fmt(h.getValueAtPercentile(90))}`,
    `p95=${fmt(h.getValueAtPercentile(95))}`,
    `p99=${fmt(h.getValueAtPercentile(99))}`,
    `p99.9=${fmt(h.getValueAtPercentile(99.9))}`,
  ].join(" ");
}

function printTable(groupHists) {
  const percentiles = [50, 75, 90, 95, 99, 99.9];
  const labels = ["p50", "p75", "p90", "p95", "p99", "p99.9"];
  const names = GROUPS.map(g => g.name);
  const pad = (s, w) => s.padStart(w);

  const c1 = 12, c2 = 14, c3 = 14, c4 = 12, c5 = 12;
  console.log("");
  console.log(`╔══════════╦${"═".repeat(c1)}╦${"═".repeat(c2)}╦${"═".repeat(c3)}╦${"═".repeat(c4)}╦${"═".repeat(c5)}╗`);
  console.log(`║ Pctl     ║${pad(names[0], c1)}║${pad(names[1], c2)}║${pad(names[2], c3)}║${pad("bridge-WS Δ", c4)}║${pad("host-WS Δ", c5)}║`);
  console.log(`╠══════════╬${"═".repeat(c1)}╬${"═".repeat(c2)}╬${"═".repeat(c3)}╬${"═".repeat(c4)}╬${"═".repeat(c5)}╣`);

  for (let i = 0; i < percentiles.length; i++) {
    const p = percentiles[i];
    const vals = groupHists.map(h => h.getValueAtPercentile(p) / 1e6);
    const d1 = vals[1] - vals[0];
    const d2 = vals[2] - vals[0];
    console.log(
      `║ ${pad(labels[i], 8)} ║${pad(vals[0].toFixed(3), c1)}║${pad(vals[1].toFixed(3), c2)}║${pad(vals[2].toFixed(3), c3)}║${pad((d1 >= 0 ? "+" : "") + d1.toFixed(3), c4)}║${pad((d2 >= 0 ? "+" : "") + d2.toFixed(3), c5)}║`
    );
  }
  console.log(`╚══════════╩${"═".repeat(c1)}╩${"═".repeat(c2)}╩${"═".repeat(c3)}╩${"═".repeat(c4)}╩${"═".repeat(c5)}╝`);
  console.log("");
}

async function main() {
  const { warmup, duration } = parseArgs();
  console.log(`[client] Warmup: ${warmup}s, Measurement: ${duration}s`);

  const packageDef = protoLoader.loadSync(PROTO_PATH, {
    keepCase: true,
    longs: String,
    enums: String,
    defaults: true,
    oneofs: true,
  });
  const benchmarkProto = grpc.loadPackageDefinition(packageDef).benchmark;
  const creds = grpc.credentials.createInsecure();

  const histograms = GROUPS.map(() =>
    [0, 1, 2].map(() => createHistogram())
  );
  const counts = GROUPS.map(() => [0, 0, 0]);

  let measuring = false;

  const { monitorEventLoopDelay } = require("node:perf_hooks");
  const elMonitor = monitorEventLoopDelay({ resolution: 20 });
  elMonitor.enable();

  const elLogInterval = setInterval(() => {
    console.log(
      `[EL] p50=${(elMonitor.percentile(50) / 1e6).toFixed(2)}ms ` +
      `p99=${(elMonitor.percentile(99) / 1e6).toFixed(2)}ms ` +
      `max=${(elMonitor.max / 1e6).toFixed(2)}ms`
    );
    elMonitor.reset();
  }, 5000);

  function connectEndpoint(groupIdx, endpointIdx) {
    const group = GROUPS[groupIdx];
    if (group.type === "ws") return connectWS(groupIdx, endpointIdx);
    return connectGRPC(groupIdx, endpointIdx);
  }

  function connectWS(gi, ei) {
    return new Promise((resolve) => {
      const ws = new WebSocket(GROUPS[gi].endpoints[ei]);
      ws.on("open", () => {
        console.log(`[client] ${GROUPS[gi].name} #${ei + 1} connected`);
        resolve();
      });
      ws.on("message", (raw) => {
        if (!measuring) return;
        const now = performance.timeOrigin + performance.now();
        const msg = JSON.parse(raw.toString());
        const latencyMicros = Math.round((now - msg.timestamp) * 1000);
        if (latencyMicros > 0) histograms[gi][ei].recordValue(latencyMicros);
        counts[gi][ei]++;
      });
      ws.on("error", (err) =>
        console.error(`[client] ${GROUPS[gi].name} #${ei + 1} error: ${err.message}`)
      );
    });
  }

  function connectGRPC(gi, ei) {
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(
        () => reject(new Error(`${GROUPS[gi].name} #${ei + 1} connect timeout`)),
        CONNECT_TIMEOUT
      );
      let resolved = false;
      const done = () => {
        if (!resolved) { resolved = true; clearTimeout(timeout); resolve(); }
      };
      const client = new benchmarkProto.BenchmarkService(
        GROUPS[gi].endpoints[ei], creds,
        { "grpc.keepalive_time_ms": 30000, "grpc.keepalive_timeout_ms": 10000 }
      );
      const stream = client.StreamMessages({ client_id: `bench-${gi}-${ei}` });
      stream.on("data", (resp) => {
        if (!resolved) {
          console.log(`[client] ${GROUPS[gi].name} #${ei + 1} connected`);
          done();
        }
        if (!measuring) return;
        const now = performance.timeOrigin + performance.now();
        const latencyMicros = Math.round((now - Number(resp.timestamp)) * 1000);
        if (latencyMicros > 0) histograms[gi][ei].recordValue(latencyMicros);
        counts[gi][ei]++;
      });
      stream.on("error", (err) => {
        if (!resolved) { clearTimeout(timeout); reject(err); }
        else console.error(`[client] ${GROUPS[gi].name} #${ei + 1} error: ${err.message}`);
      });
    });
  }

  console.log("[client] Connecting to all endpoints...");
  const connectPromises = [];
  for (let gi = 0; gi < GROUPS.length; gi++) {
    for (let ei = 0; ei < GROUPS[gi].endpoints.length; ei++) {
      connectPromises.push(connectEndpoint(gi, ei));
    }
  }
  await Promise.all(connectPromises);

  console.log(`[client] All connected. Warmup for ${warmup}s...`);
  await new Promise((r) => setTimeout(r, warmup * 1000));

  console.log(`[client] Measurement phase (${duration}s)...`);
  measuring = true;
  await new Promise((r) => setTimeout(r, duration * 1000));
  measuring = false;

  clearInterval(elLogInterval);
  elMonitor.disable();

  const groupMerged = GROUPS.map((_, gi) => mergeHistograms(histograms[gi]));

  printTable(groupMerged);

  console.log("Per-endpoint breakdown:");
  for (let gi = 0; gi < GROUPS.length; gi++) {
    for (let ei = 0; ei < GROUPS[gi].endpoints.length; ei++) {
      console.log(
        `  ${GROUPS[gi].name} #${ei + 1}: ${counts[gi][ei]} msgs, ${printPercentile(histograms[gi][ei])}`
      );
    }
  }

  const totalMsgs = counts.flat().reduce((a, b) => a + b, 0);
  console.log(`\nEvent loop lag: p50=${(elMonitor.percentile(50) / 1e6).toFixed(2)}ms, p99=${(elMonitor.percentile(99) / 1e6).toFixed(2)}ms, max=${(elMonitor.max / 1e6).toFixed(2)}ms`);
  console.log(`Total messages: ${totalMsgs}`);
  console.log(`\nPlatform: ${process.platform} (${process.platform === "darwin" ? "macOS Docker Desktop host networking uses VM, not true host" : "Linux"})`);

  process.exit(0);
}

main().catch((err) => {
  console.error(`[client] Fatal: ${err.message}`);
  process.exit(1);
});
