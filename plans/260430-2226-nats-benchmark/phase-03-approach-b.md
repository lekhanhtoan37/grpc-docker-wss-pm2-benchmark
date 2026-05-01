---
title: "Phase 03: Approach B — Standalone NATS Benchmark"
phase: 3-of-5
status: pending
priority: P2
effort: 2h
blocked-by: [phase-01]
blocks: none
approach: B
---

# Phase 03: Approach B — Standalone NATS Benchmark

## Context

- Pure NATS performance measurement
- No Kafka dependency — measures NATS pub/sub in isolation
- Supplements Approach A results
- Useful for understanding NATS ceiling performance

## Overview

New Go app `nats-bench/`. Self-contained: produces messages to NATS, subscribes to NATS, measures throughput + latency. Reports in same format as benchmark client.

## Requirements

### Functional
- Publish N messages to NATS subject at configurable rate
- Subscribe to same NATS subject, receive messages
- Measure end-to-end latency (producer timestamp → subscriber receipt)
- HDR histogram for latency distribution
- Throughput stats: MB/s, msg/s
- Configurable: message size, rate, duration, subscribers, publishers

### Non-Functional
- Zero external dependencies beyond nats.go
- Single binary
- Report format compatible with existing benchmark results

## Architecture

```
nats-bench/
├── main.go         (CLI entry point)
├── publisher.go    (NATS publisher)
├── subscriber.go   (NATS subscriber + stats)
├── report.go       (output formatting)
├── go.mod
└── Dockerfile
```

### Data Flow

```
[Publisher goroutine]
  → nc.Publish("bench.test", msg)
  → msg = {"timestamp": unixMicro, "seq": N, "data": "xxx..."}

[Subscriber goroutine]
  → nc.Subscribe("bench.test", handler)
  → handler: latency = now - timestamp
  → HDR histogram record
  → Count/Bytes atomic increment

[Main goroutine]
  → warmup → measure → report
```

### Modes

1. **Single-process**: Publisher + subscriber in same process
2. **Multi-process**: Separate publisher and subscriber binaries (via flags)
3. **Queue group**: Multiple subscribers with `nc.QueueSubscribe()` for load balancing

## Implementation Steps

1. **Create `nats-bench/` directory**

   ```
   nats-bench/
   ├── main.go
   ├── go.mod
   └── Dockerfile
   ```

2. **Create `nats-bench/go.mod`**

   ```
   module nats-bench
   go 1.22
   require (
       github.com/nats-io/nats.go v1.39.1
       github.com/HdrHistogram/hdrhistogram-go v1.1.2
   )
   ```

3. **Create `nats-bench/main.go`**

   Flags:
   ```
   -nats-url    string  "nats://localhost:4222"
   -subject     string  "bench.test"
   -mode        string  "both" | "pub" | "sub"
   -duration    int     120 (seconds)
   -warmup      int     30 (seconds)
   -rate        int     0 (0 = max, >0 = msg/s limit)
   -msg-size    int     1024 (bytes)
   -batch-size  int     20 (messages per flush)
   -subscribers int     1
   -queue       string  "" (empty = no queue group)
   ```

4. **Publisher implementation**

   ```go
   type Publisher struct {
       nc      *nats.Conn
       subject string
       msgSize int
       rate    int
       hist    *hdrhistogram.Histogram  // publish latency
   }

   func (p *Publisher) Run(ctx context.Context, measuring *atomic.Bool) {
       data := strings.Repeat("x", p.msgSize - 40)  // account for JSON overhead
       seq := int64(0)
       ticker := time.NewTicker(time.Second / time.Duration(max(p.rate, 100000)))

       for {
           select {
           case <-ctx.Done():
               return
           case <-ticker.C:
               ts := time.Now().UnixMicro()
               msg := fmt.Sprintf(`{"timestamp":%d,"seq":%d,"data":"%s"}`, ts, seq, data)
               start := time.Now()
               p.nc.Publish(p.subject, []byte(msg))
               if measuring.Load() {
                   p.hist.RecordValue(time.Since(start).Microseconds())
               }
               seq++
           }
       }
   }
   ```

5. **Subscriber implementation**

   ```go
   type Subscriber struct {
       nc      *nats.Conn
       subject string
       queue   string
       count   atomic.Int64
       bytes   atomic.Int64
       hist    *hdrhistogram.Histogram
   }

   func (s *Subscriber) Start(ctx context.Context, measuring *atomic.Bool) {
       handler := func(msg *nats.Msg) {
           now := time.Now().UnixMicro()
           ts := ExtractTimestampInt64(msg.Data)  // reuse frame.go pattern
           s.count.Add(1)
           s.bytes.Add(int64(len(msg.Data)))

           if measuring.Load() && ts > 0 {
               lat := now - ts
               if lat > 0 {
                   s.hist.RecordValue(lat)
               }
           }
       }

       if s.queue != "" {
           s.nc.QueueSubscribe(s.subject, s.queue, handler)
       } else {
           s.nc.Subscribe(s.subject, handler)
       }
   }
   ```

6. **Report output** — match benchmark-client format

   ```
   === NATS BENCHMARK RESULTS ===

   Throughput:
     Messages:  1,234,567
     MB/s:      123.45
     msg/s:     10,288

   Latency (microseconds):
     p50:    45.2
     p75:    67.3
     p90:    89.1
     p95:    123.4
     p99:    234.5
     p99.9:  567.8
   ```

7. **Create `nats-bench/Dockerfile`**

   Standard Go multi-stage build, similar to nats-worker.

## Files

| File | Action |
|------|--------|
| `nats-bench/main.go` | CREATE |
| `nats-bench/go.mod` | CREATE |
| `nats-bench/Dockerfile` | CREATE |

## Todo Checklist

- [ ] Create nats-bench/ directory
- [ ] Implement main.go with publisher + subscriber
- [ ] Create go.mod
- [ ] Create Dockerfile
- [ ] Test: single-process mode
- [ ] Test: queue group with 3 subscribers
- [ ] Test: rate-limited publish
- [ ] Verify latency measurements are reasonable (<1ms for local)

## Success Criteria

- Publishes and receives messages via NATS
- HDR histogram latency measurements accurate
- Report format readable
- Works in single-process and multi-process modes
- Queue group load balancing verified

## Trade-off Analysis

### Pros
- ✅ Pure NATS performance measurement (no Kafka overhead)
- ✅ Simple — single binary, no dependencies
- ✅ Quick to build and test
- ✅ Good for NATS tuning and capacity planning

### Cons
- ❌ NOT comparable with WS/gRPC results (different end-to-end path)
- ❌ Measures NATS-only latency, not Kafka→NATS→Client
- ❌ Doesn't use existing benchmark client infrastructure

### Effort: 2h

### Apples-to-apples comparison: ❌ NO
Different path: Producer→NATS→Subscriber vs Kafka→Worker→WS/gRPC→Client.
Latency numbers will be lower (no Kafka hop), throughput may be higher (no Kafka bottleneck).

### Recommended: ❌ SUPPLEMENT ONLY
Build as a secondary tool for NATS-specific performance analysis.
Do NOT use as primary comparison with WS/gRPC.

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Not used after Approach A works | High | Low | Keep it simple, minimal investment |
| Misleading comparison with WS/gRPC | Medium | High | Clearly document it measures NATS-only |
