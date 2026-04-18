# Research Report: Kafka Producer/Consumer (KafkaJS) + Latency Benchmarking

**Date:** 2026-04-18
**Scope:** KafkaJS producer at 100msg/s, 1KB payload, p50/p99 latency measurement, Docker networking

---

## Key Findings

- **KafkaJS** is the standard pure-JS Kafka client. No native deps. Supports `send()`, `sendBatch()`, message timestamps, headers, compression.
- **`hdr-histogram-js`** (v3.0.1) is the best NodeJS library for p50/p99/p999 latency tracking. Most accurate + fastest per benchmarks. Provides `getValueAtPercentile()`, `summary`, and text output.
- **`@platformatic/kafka`** is 48% faster than KafkaJS for single-message produce (92K vs 62K op/s) but KafkaJS is fine for 100 msg/s throughput.
- KafkaJS does NOT auto-batch. Each `send()` call = one network roundtrip. For 100 msg/s, either batch messages per `send()` or use `sendBatch()`.
- **Timestamp embedding in payload** is the correct approach for end-to-end latency. Use `process.hrtime.bigint()` or `Date.now()` embedded in message value.
- **Docker -> host Kafka**: use `host.docker.internal:9092` (macOS/Windows) or `--network host` (Linux). Must configure Kafka `advertised.listeners` correctly.

---

## 1. Kafka Producer: 100 msg/s, 1KB Payload

```js
const { Kafka, Partitioners } = require('kafkajs');

const kafka = new Kafka({
  clientId: 'latency-producer',
  brokers: [process.env.KAFKA_BROKER || 'localhost:9092'],
});

const producer = kafka.producer({ createPartitioner: Partitioners.DefaultPartitioner });

async function startProducer() {
  await producer.connect();

  const TOPIC = 'benchmark-messages';
  const RATE_MS = 10; // 100 msg/sec = 1 msg every 10ms
  const PAYLOAD_SIZE = 1024; // 1KB

  setInterval(async () => {
    const sendTs = Date.now();
    const hrts = process.hrtime.bigint().toString();

    const payload = {
      timestamp: sendTs,
      hrtime: hrts,
      seq: Math.floor(Math.random() * 1e9),
      data: 'x'.repeat(PAYLOAD_SIZE - 80), // pad to ~1KB total
    };

    await producer.send({
      topic: TOPIC,
      messages: [{
        key: `msg-${sendTs}`,
        value: JSON.stringify(payload),
        timestamp: String(sendTs),
      }],
      acks: 1, // leader ack only (faster than -1 all ISR)
    });
  }, RATE_MS);
}

startProducer().catch(console.error);
```

**Batch alternative** (better throughput, collects 10 msgs per send):

```js
const BATCH_SIZE = 10;
const BATCH_INTERVAL_MS = 100; // 10 batches/sec * 10 msgs = 100 msg/s

setInterval(async () => {
  const messages = [];
  for (let i = 0; i < BATCH_SIZE; i++) {
    const sendTs = Date.now();
    messages.push({
      key: `msg-${sendTs}-${i}`,
      value: JSON.stringify({
        timestamp: sendTs,
        hrtime: process.hrtime.bigint().toString(),
        seq: i,
        data: 'x'.repeat(1024 - 60),
      }),
      timestamp: String(sendTs),
    });
  }
  await producer.send({ topic: 'benchmark-messages', messages, acks: 1 });
}, BATCH_INTERVAL_MS);
```

---

## 2. Latency Measurement: Timestamp Embedding Pattern

**Architecture:**

```
Producer embeds {timestamp, hrtime} in Kafka message
  -> Kafka topic
    -> Consumer (in WS/gRPC server) reads message
      -> Computes delta = now - payload.timestamp
        -> Records delta in HDR histogram
          -> Periodically logs p50/p99/p999
```

**Consumer with latency measurement:**

```js
const hdr = require('hdr-histogram-js');

const histogram = hdr.build({ smallestValue: 1, highestValue: 1e9, precision: 3 });

const consumer = kafka.consumer({ groupId: 'latency-bench-consumer' });

async function startConsumer() {
  await consumer.connect();
  await consumer.subscribe({ topic: 'benchmark-messages', fromBeginning: false });

  let msgCount = 0;

  await consumer.run({
    eachMessage: async ({ message }) => {
      const payload = JSON.parse(message.value.toString());
      const now = Date.now();
      const latencyMs = now - payload.timestamp;

      histogram.recordValue(latencyMs);
      msgCount++;

      if (msgCount % 1000 === 0) {
        console.log(`[msg #${msgCount}] p50=${histogram.getValueAtPercentile(50)}ms ` +
          `p99=${histogram.getValueAtPercentile(99)}ms ` +
          `p999=${histogram.getValueAtPercentile(99.9)}ms ` +
          `max=${histogram.maxValue}ms`);
      }
    },
  });
}
```

**High-resolution timing (sub-ms accuracy):**

```js
// Producer
const hrSend = process.hrtime.bigint(); // nanoseconds
payload.hrtime = hrSend.toString();

// Consumer
const hrRecv = process.hrtime.bigint();
const latencyNs = hrRecv - BigInt(payload.hrtime);
const latencyUs = Number(latencyNs / 1000n); // microseconds
histogram.recordValue(latencyUs);
```

---

## 3. p50/p99 Calculation Libraries

### Recommended: `hdr-histogram-js`

```
npm install hdr-histogram-js
```

| Feature | Details |
|---------|---------|
| Percentiles | p50, p75, p90, p95, p99, p99.9, p99.99, p99.999 |
| API | `h.getValueAtPercentile(99)`, `h.summary`, `h.mean` |
| Accuracy | <1% error across all percentiles (benchmarked) |
| Performance | 1,769 ops/sec recording, 3,509 ops/sec extracting |
| Memory | Constant regardless of sample count (bucket-based) |
| Coordinated omission | Supported |

**Usage:**

```js
const hdr = require('hdr-histogram-js');
const h = hdr.build();

h.recordValue(12);
h.recordValue(45);
h.recordValue(120);

console.log(h.summary);
// { p50: 45, p75: 120, p90: 120, p99: 120, p99_9: 120, max: 120, totalCount: 3 }

console.log(h.getValueAtPercentile(50));  // 45
console.log(h.getValueAtPercentile(99));  // 120

// Reset for new window
h.reset();
```

### Alternatives

| Library | Pros | Cons |
|---------|------|------|
| `hdr-histogram-js` | Most accurate, constant memory, WSASM option | None significant |
| `native-hdr-histogram` | C binding, fastest | Native compilation required |
| `measured-core` | Widely used | Less accurate at tails |
| `percentile` (npm) | Tiny, simple | No histogram, O(n log n) sort |
| Hand-rolled sort + index | Zero deps | O(n log n), memory grows unbounded |

---

## 4. Docker Networking: NodeJS Container -> Host Kafka

### Option A: macOS/Windows - `host.docker.internal`

```yaml
# docker-compose.yml
services:
  ws-server:
    build: ./ws-server
    environment:
      - KAFKA_BROKER=host.docker.internal:9092
    extra_hosts:
      - "host.docker.internal:host-gateway"
```

```js
const kafka = new Kafka({
  brokers: [process.env.KAFKA_BROKER || 'host.docker.internal:9092'],
});
```

### Option B: Linux - `--network host`

```yaml
services:
  ws-server:
    build: ./ws-server
    network_mode: host
    environment:
      - KAFKA_BROKER=localhost:9092
```

### Option C: Shared Docker network with Kafka container

```yaml
services:
  kafka:
    image: confluentinc/cp-kafka:latest
    networks:
      - app-tier
    environment:
      KAFKA_ADVERTISED_LISTENERS: PLAINTEXT://kafka:9092,PLAINTEXT_HOST://localhost:29092
    ports:
      - "29092:29092"

  ws-server:
    build: ./ws-server
    networks:
      - app-tier
    environment:
      - KAFKA_BROKER=kafka:9092

networks:
  app-tier:
    driver: bridge
```

### Critical: Kafka `advertised.listeners`

Kafka returns the advertised address to clients. If misconfigured, containers get unreachable addresses.

```properties
# Kafka server.properties
# Both internal (Docker) and external (host) listeners
listeners=PLAINTEXT://0.0.0.0:9092
advertised.listeners=PLAINTEXT://host.docker.internal:9092
```

---

## 5. Gotchas

### Clock Sync
- **`Date.now()`** has ~1ms resolution. Insufficient for sub-ms latency.
- **`process.hrtime.bigint()`** is monotonic, nanosecond precision, but only within a single process.
- For cross-process latency (producer -> consumer on different machines), must use `Date.now()` and ensure NTP sync.
- For same-host testing, `hrtime` is fine.

### GC Pauses
- V8 GC can pause 10-50ms, creating artificial latency spikes.
- Mitigate: use `--max-old-space-size=4096`, avoid allocations in hot path, pre-allocate buffers.
- `hdr-histogram-js` helps: coordinated omission correction can filter GC-caused outliers.

### Event Loop Blocking
- `await producer.send()` is async but blocks event loop during serialization.
- For 100 msg/s this is fine. For 10K+ msg/s, batch sends.
- Consumer `eachMessage` is sequential by default. Use `eachBatch` + parallel processing for high throughput.

### KafkaJS Performance Tips
- **No auto-batching**: each `send()` = 1 roundtrip. Use `sendBatch()` or pass multiple messages per `send()`.
- **acks=1** is fastest (leader only). **acks=-1** waits for all replicas (2-5x slower).
- **Compression**: `LZ4` or `Snappy` for large payloads. Not needed for 1KB.
- **Producer linger**: KafkaJS has no `linger.ms` config. It sends immediately. Batch manually.

### Memory Leaks
- Reset HDR histogram periodically (e.g., every 60s window) to avoid stale data.
- Log percentiles before reset.

---

## 6. Recommendations

1. **Use `hdr-histogram-js`** for all latency tracking. It's the standard. Rolling window + periodic reset.
2. **Embed `Date.now()` + `process.hrtime.bigint()`** in payload. Use `Date.now()` for cross-process, `hrtime` for same-process debugging.
3. **Batch sends** for 100 msg/s: send 10 messages every 100ms. Reduces network overhead.
4. **Use `host.docker.internal`** (macOS) or `--network host` (Linux) for Docker -> host Kafka.
5. **Configure `advertised.listeners`** with both internal and external addresses if Kafka runs in Docker.
6. **Log p50/p99/p999 every 1000 messages** and also on 60s rolling windows.
7. **Warm up**: discard first 100-500 messages to allow JIT compilation and connection stabilization.

---

## Unresolved Questions

- Whether `process.hrtime.bigint()` drift exists across long-running processes (unlikely in <1hr benchmarks).
- Optimal KafkaJS consumer `sessionTimeout` and `heartbeatInterval` for benchmark workloads.
- Whether `@platformatic/kafka` (48% faster) is worth the migration effort for this use case (100 msg/s is trivial throughput).
