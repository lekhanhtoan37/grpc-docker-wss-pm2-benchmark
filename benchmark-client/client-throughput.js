const WebSocket = require("ws");
const grpc = require("@grpc/grpc-js");
const protoLoader = require("@grpc/proto-loader");
const hdr = require("hdr-histogram-js");
const { performance, monitorEventLoopDelay } = require("node:perf_hooks");

const PROTO_PATH = __dirname + "/proto/benchmark.proto";
const WARMUP_DEFAULT = 30;
const DURATION_DEFAULT = 120;
const CONNECT_TIMEOUT = 30000;

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

function initGroupStats() {
  return GROUPS.map(() => ({
    histograms: [0, 1, 2].map(() => createHistogram()),
    counts: [0, 0, 0],
    bytes: [0, 0, 0],
    firstMsgTime: [null, null, null],
    lastMsgTime: [null, null, null],
  }));
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

  const stats = initGroupStats();
  let measuring = false;
  let measureStart = null;
  let measureEnd = null;

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

  function handleMessage(gi, ei, payloadStr, timestamp) {
    if (!measuring) return;
    const now = performance.timeOrigin + performance.now();
    const latencyMicros = Math.round((now - timestamp) * 1000);
    if (latencyMicros > 0) stats[gi].histograms[ei].recordValue(latencyMicros);
    stats[gi].counts[ei]++;
    stats[gi].bytes[ei] += payloadStr.length;
    if (!stats[gi].firstMsgTime[ei]) stats[gi].firstMsgTime[ei] = now;
    stats[gi].lastMsgTime[ei] = now;
  }

  function connectWS(gi, ei) {
    return new Promise((resolve) => {
      const ws = new WebSocket(GROUPS[gi].endpoints[ei]);
      ws.on("open", () => {
        console.log(`[client] ${GROUPS[gi].name} #${ei + 1} connected`);
        resolve();
      });
      ws.on("message", (raw) => {
        const msg = JSON.parse(raw.toString());
        handleMessage(gi, ei, raw.toString(), msg.timestamp);
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
        {
          "grpc.keepalive_time_ms": 30000,
          "grpc.keepalive_timeout_ms": 10000,
          "grpc.max_receive_message_length": 10485760,
          "grpc.max_send_message_length": 10485760,
          "grpc.http2.max_frame_size": 16777215,
          "grpc.http2.initial_window_size": 67108864,
          "grpc.http2.initial_connection_window_size": 134217728,
        }
      );
      const stream = client.StreamMessages({ client_id: `bench-${gi}-${ei}` });
      stream.on("data", (resp) => {
        if (!resolved) {
          console.log(`[client] ${GROUPS[gi].name} #${ei + 1} connected`);
          done();
        }
        for (const entry of (resp.messages || [])) {
          handleMessage(gi, ei, entry.payload, Number(entry.timestamp));
        }
      });
      stream.on("error", (err) => {
        if (!resolved) { clearTimeout(timeout); reject(err); }
        else console.error(`[client] ${GROUPS[gi].name} #${ei + 1} error: ${err.message}`);
      });
    });
  }

  console.log("[client] Connecting to all endpoints...");
  const connectPromises = [];
  const connectMap = [];
  for (let gi = 0; gi < GROUPS.length; gi++) {
    for (let ei = 0; ei < GROUPS[gi].endpoints.length; ei++) {
      const idx = connectPromises.length;
      if (GROUPS[gi].type === "ws") connectPromises.push(connectWS(gi, ei));
      else connectPromises.push(connectGRPC(gi, ei));
      connectMap.push({ gi, ei });
    }
  }
  const results = await Promise.allSettled(connectPromises);
  const failedGroups = new Set();
  for (let i = 0; i < results.length; i++) {
    if (results[i].status === "rejected") {
      const { gi } = connectMap[i];
      failedGroups.add(gi);
      console.error(`[client] ${GROUPS[connectMap[i].gi].name} #${connectMap[i].ei + 1} failed: ${results[i].reason}`);
    }
  }
  if (failedGroups.size > 0) {
    for (const gi of failedGroups) {
      console.warn(`[client] WARNING: Skipping ${GROUPS[gi].name} - connection failed`);
    }
  }
  const connectedCount = results.filter(r => r.status === "fulfilled").length;
  if (connectedCount === 0) {
    console.error("[client] No connections succeeded. Aborting.");
    process.exit(1);
  }
  console.log(`[client] ${connectedCount}/${results.length} endpoints connected.`);

  console.log(`[client] All connected. Warmup for ${warmup}s...`);
  await new Promise((r) => setTimeout(r, warmup * 1000));

  console.log(`[client] Measurement phase (${duration}s)...`);
  measuring = true;
  measureStart = Date.now();
  await new Promise((r) => setTimeout(r, duration * 1000));
  measuring = false;
  measureEnd = Date.now();

  clearInterval(elLogInterval);
  elMonitor.disable();

  const measureDurationSec = (measureEnd - measureStart) / 1000;

  console.log("\n=== THROUGHPUT RESULTS ===\n");
  console.log(`${"Group".padEnd(16)} ${"Msgs".padStart(10)} ${"MB/s".padStart(10)} ${"msg/s".padStart(12)}`);
  console.log("-".repeat(50));

  const throughputs = [];
  for (let gi = 0; gi < GROUPS.length; gi++) {
    const totalMsgs = stats[gi].counts.reduce((a, b) => a + b, 0);
    const totalBytes = stats[gi].bytes.reduce((a, b) => a + b, 0);
    const mbps = (totalBytes / 1024 / 1024 / measureDurationSec).toFixed(2);
    const msgPerSec = (totalMsgs / measureDurationSec).toFixed(0);
    throughputs.push(parseFloat(mbps));
    const note = failedGroups.has(gi) ? " (FAILED)" : "";
    console.log(
      `${(GROUPS[gi].name + note).padEnd(16)} ${String(totalMsgs).padStart(10)} ${mbps.padStart(10)} ${msgPerSec.padStart(12)}`
    );
  }

  const wsThroughput = throughputs[0];
  if (wsThroughput > 0) {
    console.log("-".repeat(50));
    for (let gi = 1; gi < GROUPS.length; gi++) {
      if (failedGroups.has(gi)) continue;
      const delta = ((throughputs[gi] - wsThroughput) / wsThroughput * 100).toFixed(1);
      const sign = delta >= 0 ? "+" : "";
      console.log(`${GROUPS[gi].name.padEnd(16)} ${sign}${delta}% vs WS throughput`);
    }
  }

  console.log("\n=== LATENCY RESULTS ===\n");
  const groupMerged = GROUPS.map((_, gi) => mergeHistograms(stats[gi].histograms));

  const percentiles = [50, 75, 90, 95, 99, 99.9];
  const labels = ["p50", "p75", "p90", "p95", "p99", "p99.9"];
  const names = GROUPS.map(g => g.name);
  const pad = (s, w) => s.padStart(w);

  const c1 = 12, c2 = 14, c3 = 14, c4 = 12, c5 = 12;
  console.log(`╔══════════╦${"═".repeat(c1)}╦${"═".repeat(c2)}╦${"═".repeat(c3)}╦${"═".repeat(c4)}╦${"═".repeat(c5)}╗`);
  console.log(`║ Pctl     ║${pad(names[0], c1)}║${pad(names[1], c2)}║${pad(names[2], c3)}║${pad("bridge-WS Δ", c4)}║${pad("host-WS Δ", c5)}║`);
  console.log(`╠══════════╬${"═".repeat(c1)}╬${"═".repeat(c2)}╬${"═".repeat(c3)}╬${"═".repeat(c4)}╬${"═".repeat(c5)}╣`);

  for (let i = 0; i < percentiles.length; i++) {
    const p = percentiles[i];
    const vals = groupMerged.map(h => h.getValueAtPercentile(p) / 1e6);
    const d1 = vals[1] - vals[0];
    const d2 = vals[2] - vals[0];
    console.log(
      `║ ${pad(labels[i], 8)} ║${pad(vals[0].toFixed(3), c1)}║${pad(vals[1].toFixed(3), c2)}║${pad(vals[2].toFixed(3), c3)}║${pad((d1 >= 0 ? "+" : "") + d1.toFixed(3), c4)}║${pad((d2 >= 0 ? "+" : "") + d2.toFixed(3), c5)}║`
    );
  }
  console.log(`╚══════════╩${"═".repeat(c1)}╩${"═".repeat(c2)}╩${"═".repeat(c3)}╩${"═".repeat(c4)}╩${"═".repeat(c5)}╝`);

  console.log("\nPer-endpoint breakdown:");
  for (let gi = 0; gi < GROUPS.length; gi++) {
    for (let ei = 0; ei < GROUPS[gi].endpoints.length; ei++) {
      const h = stats[gi].histograms[ei];
      const fmt = (v) => (h.getValueAtPercentile(v) / 1e6).toFixed(3);
      const mbps = (stats[gi].bytes[ei] / 1024 / 1024 / measureDurationSec).toFixed(2);
      console.log(
        `  ${GROUPS[gi].name} #${ei + 1}: ${stats[gi].counts[ei]} msgs, ${mbps} MB/s, p50=${fmt(50)} p99=${fmt(99)}`
      );
    }
  }

  const totalMsgs = stats.flatMap(s => s.counts).reduce((a, b) => a + b, 0);
  const totalBytes = stats.flatMap(s => s.bytes).reduce((a, b) => a + b, 0);
  console.log(`\nAggregate: ${totalMsgs} msgs, ${(totalBytes / 1024 / 1024).toFixed(2)} MB`);
  console.log(`Event loop: p50=${(elMonitor.percentile(50) / 1e6).toFixed(2)}ms, p99=${(elMonitor.percentile(99) / 1e6).toFixed(2)}ms`);
  console.log(`Platform: ${process.platform}`);

  process.exit(0);
}

main().catch((err) => {
  console.error(`[client] Fatal: ${err.message}`);
  process.exit(1);
});
