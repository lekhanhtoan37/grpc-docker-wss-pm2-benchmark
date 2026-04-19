# Researcher-03: Node.js Kafka Consumer Throughput & Benchmark Methodology for 1GB/s

## 1. KafkaJS Throughput Limits

- **KafkaJS is pure JavaScript** — no native bindings. Uses `Buffer.concat` internally for encoding, which scales poorly with large batches (github.com/tulios/kafkajs#383: 10K messages → 12s encode time; fixed in PR #394 but encoder perf remains a concern)
- **Consumer benchmark (Platformatic comparison)**: KafkaJS consumer ~9,293 ops/sec; `@platformatic/kafka` ~10,270 ops/sec (+10.4%); `node-rdkafka` ~9,211 ops/sec. These are small-message benchmarks — NOT 1GB/s throughput
- **Realistic KafkaJS ceiling**: ~4,000-10,000 messages/sec for small messages with processing. Without processing (just poll+deserialize): ~250K msg/sec Java client; KafkaJS is roughly 5-10x slower than Java client for equivalent ops
- **1GB/s with KafkaJS?** Unlikely on a single consumer instance. KafkaJS throughput is measured in thousands of msg/sec for typical payloads. At 1KB/message, 1GB/s = 1M msg/sec — far beyond KafkaJS single-consumer capability
- **node-rdkafka** handles 5-10x more messages/sec than KafkaJS on same hardware (per npm-compare.com benchmark). Sub-ms p99 latency at 100K+ msg/s
- **Recommendation for 1GB/s**: Use `node-rdkafka` or `@platformatic/kafka` (built on rdkafka). KafkaJS is not designed for this throughput tier

### Key KafkaJS Bottlenecks
- `Buffer.concat` in encoder loop — O(n²) buffer cloning at scale
- No built-in linger.ms support for batching (producer sends immediately per send() call)
- GC pressure increases significantly beyond ~10K msg/sec
- Single-threaded JS processing limits decompression + deserialization throughput
- `eachMessage` handler is sequential per partition by default; `partitionsConsumedConcurrently` helps but is still single-process

## 2. Node.js Single-Threaded Limitations at High Throughput

- **Event loop lag**: At high throughput, event loop lag grows from ~10ms (healthy) to seconds. Production case study (nairihar, Mar 2026): 3s p99 event loop lag caused by unbounded array growth → GC blocking main thread
- **GC pressure**: V8 GC runs on main thread. Heavy object creation (Kafka message deserialization) causes GC pauses that block event loop. RSS grows unbounded if references aren't cleaned
- **Backpressure**: Node.js has no built-in Kafka backpressure. Consumer fetches as fast as Kafka delivers. If processing is async, internal queue grows unbounded → OOM. Must implement manual pause/resume or bounded queue
- **Worker pool contention**: libuv default thread pool = 4 threads. Multiple consumers + DNS + zlib (compression) + crypto all compete. Must set `UV_THREADPOOL_SIZE` for multi-consumer setups
- **Scaling options**:
  - `partitionsConsumedConcurrently` in KafkaJS — process multiple partitions in parallel within one process
  - `worker_threads` — offload CPU-bound processing (JSON parse, transforms)
  - `cluster` module / PM2 — run multiple Node.js processes, one consumer per process
  - Multiple consumer instances in separate processes — true horizontal scaling

### Critical Metrics to Monitor
- Event loop lag (ms) — `toobusy-js` or `prom-client`
- Heap usage & GC pause duration
- RSS (Resident Set Size) — detect memory leaks
- Consumer lag — Kafka-side metric
- `maxBytesPerPartition` — default 1MB, increase to 10MB+ for high throughput

## 3. High-Throughput Kafka Producer Design for 1GB/s

### Message Sizing Strategy
- **1KB messages**: 1GB/s = 1,048,576 msg/sec. Very high msg rate. Requires many partitions (10+)
- **10KB messages**: 1GB/s = ~102,400 msg/sec. More manageable
- **100KB messages**: 1GB/s = ~10,240 msg/sec. Easiest to achieve
- **Recommendation**: Use 10-100KB messages for benchmark to reduce per-message overhead

### Batching Configuration
```
batch.size=131072        # 128KB — good balance for compression
linger.ms=20             # wait 20ms to fill batches
max.request.size=10485760  # 10MB max request
buffer.memory=134217728  # 128MB producer buffer
```

### Compression Selection (Critical for 1GB/s)
| Codec | Compress Speed | Decompress Speed | Ratio | Best For |
|-------|---------------|-----------------|-------|----------|
| LZ4   | 300-500 MB/s  | 1000+ MB/s      | 2-4:1 | **1GB/s+ throughput** |
| Snappy| 200-300 MB/s  | 400-500 MB/s    | 2-3:1 | Balanced |
| ZSTD  | 200-400 MB/s  | 500-800 MB/s    | 3-6:1 | Best ratio/speed |
| GZIP  | 20-30 MB/s    | 150-200 MB/s    | 5-8:1 | **Too slow for 1GB/s** |

- **For 1GB/s raw throughput**: LZ4 — fastest compression, decompress at 1GB/s+
- **For reducing wire size**: ZSTD — 3-6x compression with moderate CPU
- **Benchmark data**: 200K JSON messages, LZ4 = 3.27s vs uncompressed 4.17s (faster due to reduced I/O)
- Batch-level compression: larger batches = better ratios. 64KB batch → 3.5:1 LZ4 vs 1KB batch → 1.5:1

### Node.js Producer Code Structure (KafkaJS)
```javascript
const { Kafka, CompressionTypes } = require('kafkajs');

const producer = kafka.producer({
  allowAutoTopicCreation: false,
  idempotent: false,
});

// Send in chunks of 500-1000 messages per send() call
// Use LZ4 compression codec
await producer.send({
  topic: 'benchmark-topic',
  compression: CompressionTypes.LZ4,
  messages: batch, // array of { key, value } with large payloads
});
```

- **Important**: KafkaJS has no `linger.ms`. Must manually batch via application-level buffering (setTimeout + count threshold)
- **Alternative**: `node-rdkafka` has native linger support and will achieve much higher throughput

### Kafka-Producer-Perf-Test (Java, for baseline)
```bash
kafka-producer-perf-test.sh \
  --topic benchmark-topic \
  --num-records 10000000 \
  --record-size 1000 \
  --throughput -1 \
  --producer-props \
    bootstrap.servers=kafka:9092 \
    batch.size=131072 \
    linger.ms=10 \
    compression.type=lz4 \
    acks=1
# Result: ~1M records/sec (~100 MB/sec) on tuned 3-broker cluster
```

## 4. Benchmark Measurement Methodology

### Throughput Measurement (MB/s)
```
throughput_MB = (total_bytes_consumed / 1024 / 1024) / elapsed_seconds
```
- Start timer at first message received (not at consumer.connect)
- Stop timer after last message processed
- Count bytes: sum of `message.value.length` for all consumed messages
- Run 3+ trials, report median ± stddev
- Pre-produce all data before consumer starts to isolate consumer perf from producer

### Latency Measurement (μs)
```
latency_μs = (consumer_receive_ts - producer_embed_ts) 
```
- Embed `Date.now()` or `process.hrtime.bigint()` in message value
- Consumer extracts timestamp and computes delta
- Report: p50, p95, p99, max (not average — tail latency matters)
- For end-to-end: include Kafka broker time
- For consumer-only: measure time from `eachMessage` callback start to processing complete

### Consumer Configuration for Benchmark
```javascript
const consumer = kafka.consumer({
  groupId: 'benchmark-group',
  maxBytesPerPartition: 10485760,  // 10MB
  maxBytes: 52428800,               // 50MB total fetch
  minBytes: 1048576,                // 1MB minimum before responding
  maxWaitTimeInMs: 500,
});
```

### Tools
- `kafka-consumer-perf-test.sh` — Java baseline
- `clinic.js` — Node.js profiling (doctor, flame, bubbleprof)
- `prom-client` + Grafana — runtime metrics
- `toobusy-js` — event loop lag monitoring
- Custom Node.js script with `process.hrtime.bigint()` for nanosecond precision

### Benchmark Protocol
1. Pre-produce N messages to topic (e.g., 10M messages × 1KB = ~10GB)
2. Start consumer with `fromBeginning: true`
3. Measure: time to consume all messages, bytes consumed, per-message latency
4. Repeat 3× with fresh consumer group ID each time
5. Report: throughput MB/s (median), latency p50/p99 (μs), event loop lag (ms)

## 5. Multiple Consumers (6 consumers) on Same Topic — Can Each Sustain 1GB/s?

### Short Answer: No, not on same topic with same cluster.

### Analysis
- **6 consumers × 1GB/s = 6GB/s aggregate read throughput from Kafka**
- Each consumer with unique group gets its own copy of all messages — Kafka fans out data to every group
- Kafka broker must serve 6× the data over network: 6GB/s network egress from brokers
- **Broker-side limits**: Single Kafka broker typically handles 100-200 MB/s read throughput. 3-broker cluster = 300-600 MB/s max. 6GB/s would require 30+ brokers or extreme hardware
- **Network bottleneck**: 6GB/s = 48 Gbps. Requires 50+ Gbps network infrastructure. Typical 10GbE = ~1.2GB/s max per link

### What IS Feasible
- **6 consumers × ~100-200 MB/s each** on 3-broker cluster = 600MB/s-1.2GB/s aggregate
- **6 consumers × ~50-100 MB/s each** on single broker = 300-600MB/s aggregate
- With compression (LZ4 3:1): wire data is 1/3, so 6 consumers × 100MB/s wire = 200MB/s logical each

### Node.js Process Constraints for 6 Consumers
- Each consumer process needs: 512MB-1GB heap (message buffering, decompression)
- 6 processes × 1GB = 6GB RAM minimum
- Each process competes for: network bandwidth, CPU (decompression), Kafka broker connections
- **libuv thread pool**: Each `node-rdkafka` consumer uses worker threads. 6 consumers → set `UV_THREADPOOL_SIZE=24+`
- **For KafkaJS**: 6 consumers in same process = shared event loop = contention. Better: 6 separate processes (PM2 cluster)

### KafkaJS-Specific: 6 Consumers in One Process
- Each `kafka.consumer()` creates its own connection and fetch loop
- All share one event loop → event loop lag compounds
- `partitionsConsumedConcurrently` only helps within one consumer
- **Verdict**: Separate processes required for 6 consumers at high throughput

### Recommended Architecture for Benchmark
```
Producer (node-rdkafka, LZ4, 128KB batches)
    → Kafka (3 brokers, 6+ partitions)
        → Consumer Group A (gRPC) — 1 process
        → Consumer Group B (gRPC) — 1 process
        → Consumer Group C (gRPC) — 1 process
        → Consumer Group D (WS) — 1 process
        → Consumer Group E (WS) — 1 process
        → Consumer Group F (WS) — 1 process
Each consumer: node-rdkafka or @platformatic/kafka
Each consumer: separate PM2 process, own event loop
```

### Realistic Throughput Targets
| Scenario | Per Consumer | 6 Consumers Aggregate |
|----------|-------------|----------------------|
| KafkaJS, 1KB msg, single broker | 20-50 MB/s | 120-300 MB/s |
| node-rdkafka, 1KB msg, single broker | 100-200 MB/s | 600MB-1.2 GB/s |
| node-rdkafka, 10KB msg, 3 brokers | 200-400 MB/s | 1.2-2.4 GB/s |
| node-rdkafka, 100KB msg, LZ4, 3 brokers | 300-500 MB/s | 1.8-3.0 GB/s |
| **1GB/s per consumer (target)** | **Not feasible** with Node.js single process on typical hardware |

### Conclusion
- **1GB/s per single Node.js consumer**: Not achievable with KafkaJS. Borderline with node-rdkafka on optimized hardware (multiple partitions, large messages, LZ4, 10GbE+ network)
- **1GB/s aggregate across 6 consumers**: Feasible with node-rdkafka + 3-broker cluster + 10GbE network + large messages (10KB+) + LZ4 compression
- **Benchmark recommendation**: Use node-rdkafka or @platformatic/kafka (not KafkaJS) for any test targeting >100MB/s. Use Java `kafka-producer-perf-test` for producer baseline. Use Node.js consumer with `process.hrtime.bigint()` for latency measurement.
