const WebSocket = require("ws");
const grpc = require("@grpc/grpc-js");
const protoLoader = require("@grpc/proto-loader");
const hdr = require("hdr-histogram-js");
const { performance } = require("node:perf_hooks");

const PROTO_PATH = __dirname + "/proto/benchmark.proto";
const WARMUP_DEFAULT = 60;
const DURATION_DEFAULT = 300;
const WS_ENDPOINTS = ["ws://localhost:8080", "ws://localhost:8080", "ws://localhost:8080"];
const GRPC_ENDPOINTS = ["localhost:50051", "localhost:50052", "localhost:50053"];
const CONNECT_TIMEOUT = 10000;

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

function printTable(wsHist, grpcHist) {
  const percentiles = [50, 75, 90, 95, 99, 99.9];
  const labels = ["p50", "p75", "p90", "p95", "p99", "p99.9"];

  console.log("\n╔══════════╦══════════════╦══════════════╦════════════╗");
  console.log("║ Pctl     ║ WS (ms)      ║ gRPC (ms)    ║ Delta (ms) ║");
  console.log("╠══════════╬══════════════╬══════════════╬════════════╣");

  for (let i = 0; i < percentiles.length; i++) {
    const p = percentiles[i];
    const wsVal = wsHist.getValueAtPercentile(p) / 1e6;
    const grpcVal = grpcHist.getValueAtPercentile(p) / 1e6;
    const delta = grpcVal - wsVal;
    const pad = (s, w) => s.padStart(w);
    console.log(
      `║ ${pad(labels[i], 8)} ║ ${pad(wsVal.toFixed(3), 12)} ║ ${pad(grpcVal.toFixed(3), 12)} ║ ${pad((delta >= 0 ? "+" : "") + delta.toFixed(3), 10)} ║`
    );
  }
  console.log("╚══════════╩══════════════╩══════════════╩════════════╝\n");
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

  const wsHistograms = [createHistogram(), createHistogram(), createHistogram()];
  const grpcHistograms = [createHistogram(), createHistogram(), createHistogram()];
  const wsCounts = [0, 0, 0];
  const grpcCounts = [0, 0, 0];

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

  function connectWS(idx) {
    return new Promise((resolve) => {
      const ws = new WebSocket(WS_ENDPOINTS[idx]);
      ws.on("open", () => {
        console.log(`[client] WS #${idx + 1} connected`);
        resolve();
      });
      ws.on("message", (raw) => {
        if (!measuring) return;
        const now = performance.timeOrigin + performance.now();
        const msg = JSON.parse(raw.toString());
        const latencyMicros = Math.round((now - msg.timestamp) * 1000);
        if (latencyMicros > 0) wsHistograms[idx].recordValue(latencyMicros);
        wsCounts[idx]++;
      });
      ws.on("error", (err) => console.error(`[client] WS #${idx + 1} error: ${err.message}`));
    });
  }

  function connectGRPC(idx) {
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error(`gRPC #${idx + 1} connect timeout`)), CONNECT_TIMEOUT);
      let resolved = false;
      const done = () => {
        if (!resolved) { resolved = true; clearTimeout(timeout); resolve(); }
      };
      const client = new benchmarkProto.BenchmarkService(
        GRPC_ENDPOINTS[idx],
        creds,
        { "grpc.keepalive_time_ms": 30000, "grpc.keepalive_timeout_ms": 10000 }
      );
      const stream = client.StreamMessages({ client_id: `bench-${idx}` });
      stream.on("data", (resp) => {
        if (!resolved) {
          console.log(`[client] gRPC #${idx + 1} connected`);
          done();
        }
        if (!measuring) return;
        const now = performance.timeOrigin + performance.now();
        const latencyMicros = Math.round((now - Number(resp.timestamp)) * 1000);
        if (latencyMicros > 0) grpcHistograms[idx].recordValue(latencyMicros);
        grpcCounts[idx]++;
      });
      stream.on("error", (err) => {
        if (!resolved) { clearTimeout(timeout); reject(err); }
        else console.error(`[client] gRPC #${idx + 1} error: ${err.message}`);
      });
    });
  }

  console.log("[client] Connecting to all endpoints...");
  await Promise.all([
    ...WS_ENDPOINTS.map((_, i) => connectWS(i)),
    ...GRPC_ENDPOINTS.map((_, i) => connectGRPC(i)),
  ]);

  console.log(`[client] All connected. Warmup for ${warmup}s...`);
  await new Promise((r) => setTimeout(r, warmup * 1000));

  console.log(`[client] Measurement phase (${duration}s)...`);
  measuring = true;
  await new Promise((r) => setTimeout(r, duration * 1000));
  measuring = false;

  clearInterval(elLogInterval);
  elMonitor.disable();

  const wsCombined = mergeHistograms(wsHistograms);
  const grpcCombined = mergeHistograms(grpcHistograms);

  printTable(wsCombined, grpcCombined);

  console.log("Per-endpoint breakdown:");
  for (let i = 0; i < 3; i++) {
    console.log(`  WS #${i + 1}:    ${wsCounts[i]} msgs, ${printPercentile(wsHistograms[i])}`);
  }
  for (let i = 0; i < 3; i++) {
    console.log(`  gRPC #${i + 1}:  ${grpcCounts[i]} msgs, ${printPercentile(grpcHistograms[i])}`);
  }

  const totalMsgs = wsCounts.reduce((a, b) => a + b, 0) + grpcCounts.reduce((a, b) => a + b, 0);
  console.log(`\nEvent loop lag: p50=${(elMonitor.percentile(50) / 1e6).toFixed(2)}ms, p99=${(elMonitor.percentile(99) / 1e6).toFixed(2)}ms, max=${(elMonitor.max / 1e6).toFixed(2)}ms`);
  console.log(`Total messages: ${totalMsgs}`);

  process.exit(0);
}

main().catch((err) => {
  console.error(`[client] Fatal: ${err.message}`);
  process.exit(1);
});
