---
title: "Distributed Benchmark: Coordinator + N Workers"
description: "Split single-process benchmark client into 1 coordinator + N worker distributed system"
status: pending
priority: P1
effort: 24h
branch: main
tags: [feature, benchmark, distributed, gRPC]
created: 2026-04-26
---

# Distributed Benchmark: Coordinator + N Workers — Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the single-process Go benchmark client (`benchmark-client/go-client/main.go`, 751 lines) into a coordinator binary + N worker binaries so that load generation can be distributed across multiple machines.

**Architecture:** Coordinator is a zero-load orchestrator (Locust pattern). Workers open connections to SUT endpoints, measure latency locally via HDR histograms, and send serialized results to the coordinator. Coordinator merges histograms losslessly and prints the unified report.

**Tech Stack:** Go 1.25, gRPC (control plane), `coder/websocket` (SUT connections), `HdrHistogram/hdrhistogram-go` (latency), protobuf (wire format).

---

## Current System Summary

| Aspect | Detail |
|--------|--------|
| Binary | `benchmark-client/go-client/main.go` (751 lines, single `main()`) |
| Groups | 9 groups (WS/PM2, uWS/PM2, GoWS/PM2, uWS bridge, GoWS bridge, uWS host, GoWS host, gRPC bridge, gRPC host) |
| Endpoints | 3 per group, round-robin |
| Histogram | HDR: 1µs–1hr, 3 sig digits |
| Phases | connect → warmup (30s) → measure (120s) → report |
| Scenarios | 3, 30, 90 connections per group |
| Libraries | `coder/websocket`, `hdrhistogram-go`, `google.golang.org/grpc` |
| Deployment | Single binary, run via shell script |

### Reusable Code (move to shared packages)

| Component | Lines | Destination |
|-----------|-------|-------------|
| `ConnStats`, `GroupStats` structs | 33-48 | `internal/stats/stats.go` |
| `newConnStats()`, `newGroupStats()` | 62-77 | `internal/stats/stats.go` |
| `extractTimestampInt64()` | 128-160 | `internal/worker/frame.go` |
| `wsStatsWorker()` | 162-209 | `internal/worker/ws_stats.go` |
| `connectWS()` | 212-353 | `internal/worker/ws.go` |
| `connectGRPC()` | 355-451 | `internal/worker/grpc.go` |
| `mergeGroupHistogram()` | 453-459 | `internal/stats/histogram.go` |
| `aggregateGroup()` | 461-469 | `internal/stats/histogram.go` |
| `printLiveStats()` | 471-491 | `internal/report/report.go` |
| Report printing | 586-743 | `internal/report/report.go` |
| Latency slice pool | 81-108 | `internal/worker/pool.go` |
| `readFrameReusable()` | 110-126 | `internal/worker/frame.go` |

---

## Approach A: Minimal Viable Split

### Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                    COORDINATOR                           │
│  ┌──────────┐  ┌──────────────┐  ┌───────────────────┐  │
│  │ CLI/Config│  │ Phase Manager│  │ Report Aggregator │  │
│  └─────┬────┘  └──────┬───────┘  └────────┬──────────┘  │
│        │              │                    │              │
│  ┌─────▼──────────────▼────────────────────▼──────────┐  │
│  │           gRPC Server (unary RPCs)                  │  │
│  │  RegisterWorker → AssignConfig                      │  │
│  │  ReportSnapshot → ack                               │  │
│  │  ReportFinal → ack                                   │  │
│  └────────────────────────┬────────────────────────────┘  │
└───────────────────────────┼──────────────────────────────┘
                            │ gRPC (unary)
           ┌────────────────┼────────────────┐
           │                │                │
    ┌──────▼──────┐  ┌──────▼──────┐  ┌──────▼──────┐
    │   WORKER 0  │  │   WORKER 1  │  │   WORKER N  │
    │ ┌─────────┐ │  │ ┌─────────┐ │  │ ┌─────────┐ │
    │ │Conns to │ │  │ │Conns to │ │  │ │Conns to │ │
    │ │  SUT    │ │  │ │  SUT    │ │  │ │  SUT    │ │
    │ └────┬────┘ │  │ └────┬────┘ │  │ └────┬────┘ │
    │ HDR Hist   │  │ HDR Hist   │  │ HDR Hist   │
    │ Encode→RPC │  │ Encode→RPC │  │ Encode→RPC │
    └─────────────┘  └─────────────┘  └─────────────┘
```

### Proto Schema

```protobuf
// benchmark-client/proto/control.proto
syntax = "proto3";
package control;

option go_package = "benchmark-client/proto/control";

service CoordinatorService {
  // Worker → Coordinator: register + get config
  rpc RegisterWorker(RegisterRequest) returns (RegisterResponse);
  // Worker → Coordinator: periodic snapshot during measurement
  rpc ReportSnapshot(SnapshotRequest) returns (SnapshotResponse);
  // Worker → Coordinator: final results after measurement ends
  rpc ReportFinal(FinalReport) returns (FinalResponse);
}

message RegisterRequest {
  string worker_id = 1;
  string hostname = 2;
}

message RegisterResponse {
  repeated GroupAssignment groups = 1;
  int32 warmup_seconds = 2;
  int32 measure_seconds = 3;
  int64 measure_start_unix_ms = 4;  // barrier: worker sleeps until this time
}

message GroupAssignment {
  string name = 1;
  string type = 2;  // "ws" or "grpc"
  repeated string endpoints = 3;
  int32 connections = 4;
}

message SnapshotRequest {
  string worker_id = 1;
  int64 elapsed_ms = 2;
  repeated GroupSnapshot groups = 3;
}

message GroupSnapshot {
  string name = 1;
  int64 msg_count = 2;
  int64 byte_count = 3;
  int64 raw_count = 4;
  int64 raw_bytes = 5;
  int32 active_conns = 6;
  bytes latency_histogram = 7;  // HDR Encode()
}

message SnapshotResponse {
  bool ok = 1;
}

message FinalReport {
  string worker_id = 1;
  int64 measure_duration_ms = 2;
  repeated GroupResult groups = 3;
}

message GroupResult {
  string name = 1;
  repeated ConnResult connections = 2;
  bytes latency_histogram = 3;  // merged group histogram
  bytes reconnect_histogram = 4;
}

message ConnResult {
  int32 conn_index = 1;
  string endpoint = 2;
  int64 msg_count = 3;
  int64 byte_count = 4;
  int64 raw_count = 5;
  int64 raw_bytes = 6;
  int64 disconnect_count = 7;
  int64 reconnect_count = 8;
  bool first_msg_received = 9;
  bool conn_active = 10;
  bytes latency_histogram = 11;
  bytes reconnect_histogram = 12;
}

message FinalResponse {
  bool ok = 1;
}
```

### Directory Structure

```
benchmark-client/go-client/
├── go.mod
├── go.sum
├── cmd/
│   ├── coordinator/
│   │   └── main.go          # coordinator binary entry point
│   └── worker/
│       └── main.go          # worker binary entry point
├── internal/
│   ├── stats/
│   │   ├── stats.go         # ConnStats, GroupStats structs
│   │   ├── histogram.go     # mergeGroupHistogram, aggregateGroup, Encode/Decode helpers
│   │   └── serialize.go     # ConnStats ↔ protobuf conversion
│   ├── worker/
│   │   ├── ws.go            # connectWS() (reused from main.go)
│   │   ├── grpc.go          # connectGRPC() (reused from main.go)
│   │   ├── ws_stats.go      # wsStatsWorker() (reused)
│   │   ├── frame.go         # extractTimestampInt64, readFrameReusable
│   │   ├── pool.go          # latencySlicePool
│   │   └── runner.go        # Worker lifecycle: connect→warmup→measure→report
│   ├── coordinator/
│   │   ├── server.go        # gRPC server impl for CoordinatorService
│   │   ├── phase.go         # Phase manager: wait for workers→dispatch config→collect
│   │   ├── aggregator.go    # Merge histograms, aggregate stats from all workers
│   │   └── shard.go         # Split groups across workers
│   └── report/
│       └── report.go        # printLiveStats, final report printing (reused)
├── proto/
│   ├── benchmark.proto      # SUT proto (existing, unchanged)
│   ├── benchmark.pb.go      # generated (existing)
│   ├── benchmark_grpc.pb.go # generated (existing)
│   ├── control.proto        # control plane proto (new)
│   ├── control.pb.go        # generated
│   └── control_grpc.pb.go   # generated
├── Makefile                 # build + proto generation
└── main.go                  # KEEP as single-process fallback (thin wrapper)
```

### Phase Breakdown

#### Phase 1: Refactor — Extract shared packages (4h)

**Goal:** Split `main.go` into internal packages without changing behavior. Single-process mode still works identically.

**Files:**
- Create: `internal/stats/stats.go`
- Create: `internal/stats/histogram.go`
- Create: `internal/worker/frame.go`
- Create: `internal/worker/ws_stats.go`
- Create: `internal/worker/ws.go`
- Create: `internal/worker/grpc.go`
- Create: `internal/worker/pool.go`
- Create: `internal/report/report.go`
- Modify: `main.go` (thin wrapper importing internal packages)

- [ ] **Step 1:** Create `internal/stats/stats.go` — move `ConnStats`, `GroupStats`, `newConnStats()`, `newGroupStats()` structs. Make `hist` and `reconnectHist` exported (`Hist`, `ReconnectHist`) so report package can access them.
- [ ] **Step 2:** Create `internal/stats/histogram.go` — move `mergeGroupHistogram()`, `aggregateGroup()`. Add `EncodeHistogram(h *hdrhistogram.Histogram) ([]byte, error)` and `DecodeHistogram(data []byte) (*hdrhistogram.Histogram, error)` helpers using `hdrhistogram.Encode`/`Decode`.
- [ ] **Step 3:** Create `internal/worker/pool.go` — move `latencySlicePool`, `getLatencySlice()`, `putLatencySlice()`.
- [ ] **Step 4:** Create `internal/worker/frame.go` — move `extractTimestampInt64()`, `readFrameReusable()`, `timestampKey` var.
- [ ] **Step 5:** Create `internal/worker/ws_stats.go` — move `wsFrameEvent` struct, `wsStatsWorker()`.
- [ ] **Step 6:** Create `internal/worker/ws.go` — move `connectWS()`. Change to accept `Group` struct as parameter instead of using global.
- [ ] **Step 7:** Create `internal/worker/grpc.go` — move `connectGRPC()`. Same parameterization.
- [ ] **Step 8:** Create `internal/report/report.go` — move `printLiveStats()` and all report printing code (lines 586-743).
- [ ] **Step 9:** Rewrite `main.go` as thin wrapper: import internal packages, call same flow. Verify `go build` succeeds and behavior is identical.
- [ ] **Step 10:** Run existing benchmark (3 conns, 30s warmup, 60s measure) to verify identical output.

**Acceptance Criteria:**
- `go build -o benchmark-client .` produces binary with identical behavior
- All report sections (throughput, raw, latency, per-conn, stability) render identically
- No global state — all configuration passed as parameters

---

#### Phase 2: Proto + gRPC control plane (3h)

**Goal:** Define the control plane protocol and generate Go code.

**Files:**
- Create: `proto/control.proto`
- Create: `Makefile` (proto generation targets)
- Modify: `go.mod` (if needed for grpc deps — already has them)

- [ ] **Step 1:** Write `proto/control.proto` with the schema above.
- [ ] **Step 2:** Create `Makefile` with `proto` target:
  ```makefile
  proto:
  	protoc --go_out=. --go_opt=. \
  	       --go-grpc_out=. --go-grpc_opt=. \
  	       proto/control.proto
  ```
- [ ] **Step 3:** Run `make proto` to generate `proto/control.pb.go` and `proto/control_grpc.pb.go`.
- [ ] **Step 4:** Verify generated code compiles: `go build ./...`

**Acceptance Criteria:**
- `make proto` generates valid Go files
- `go build ./...` succeeds with generated proto

---

#### Phase 3: Coordinator binary (5h)

**Goal:** Build the coordinator that accepts worker registrations, dispatches work, collects results, and prints the merged report.

**Files:**
- Create: `cmd/coordinator/main.go`
- Create: `internal/coordinator/server.go`
- Create: `internal/coordinator/phase.go`
- Create: `internal/coordinator/aggregator.go`
- Create: `internal/coordinator/shard.go`

- [ ] **Step 1:** Create `internal/coordinator/shard.go` — function `ShardGroups(allGroups []Group, numWorkers int) [][]Group` that splits the 9 groups across N workers. Simple strategy: groups split evenly, remainder goes to worker 0. Each worker gets full connection count for its assigned groups.
- [ ] **Step 2:** Create `internal/coordinator/aggregator.go`:
  - `WorkerResult` struct: holds `FinalReport` proto from each worker
  - `AddWorkerResult(workerID string, report *control.FinalReport)`
  - `MergeToGroupStats() map[string]*stats.GroupStats` — decodes histograms from each worker's connections, merges per-group
  - `MergeToGroupHistograms() map[string]*hdrhistogram.Histogram` — decodes and merges group-level histograms
- [ ] **Step 3:** Create `internal/coordinator/phase.go`:
  - `PhaseManager` struct: tracks expected workers, registrations, phase state
  - `WaitForWorkers(ctx, expectedWorkers int, timeout time.Duration)` — blocks until all workers register
  - `StartMeasurement()` — computes `measure_start_unix_ms = now + 500ms`, stores measure duration
  - `WaitForResults(ctx, timeout time.Duration)` — blocks until all workers report final results
- [ ] **Step 4:** Create `internal/coordinator/server.go` — implement `CoordinatorService` gRPC server:
  - `RegisterWorker`: record worker, return its group assignments + barrier time
  - `ReportSnapshot`: log live stats (optional, can just log)
  - `ReportFinal`: store result in aggregator, signal PhaseManager
- [ ] **Step 5:** Create `cmd/coordinator/main.go`:
  - CLI flags: `-workers`, `-warmup`, `-duration`, `-conns`, `-listen` (default `:50000`)
  - Start gRPC server
  - Phase flow: start server → wait for workers → workers auto-proceed → wait for results → print report → exit
  - Print report using `internal/report` package with merged data

**Acceptance Criteria:**
- Coordinator starts gRPC server on specified port
- Accepts worker registrations, returns group assignments
- Waits for final reports from all workers
- Prints merged throughput + latency report

---

#### Phase 4: Worker binary (4h)

**Goal:** Build the worker binary that registers with coordinator, opens SUT connections, measures, and reports results.

**Files:**
- Create: `cmd/worker/main.go`
- Create: `internal/worker/runner.go`

- [ ] **Step 1:** Create `internal/worker/runner.go`:
  - `Runner` struct: holds coordinator address, worker ID
  - `Connect(coordinatorAddr string) (*control.RegisterResponse, error)` — calls `RegisterWorker` RPC
  - `Run(ctx context.Context, config *control.RegisterResponse)` — executes the benchmark:
    1. Parse `GroupAssignment` → `[]Group`
    2. Create `[]*GroupStats`
    3. Launch connection goroutines (reuse `connectWS`/`connectGRPC`)
    4. Sleep until `measure_start_unix_ms` (barrier)
    5. Set `measuring = true`
    6. Sleep for `measure_seconds`
    7. Set `measuring = false`
    8. Cancel context, wait for goroutines
    9. Serialize results → call `ReportFinal` RPC
- [ ] **Step 2:** Create `internal/stats/serialize.go`:
  - `ConnStatsToProto(cs *ConnStats, connIndex int, endpoint string) *control.ConnResult` — reads atomics, encodes histograms
  - `GroupStatsToProto(gs *GroupStats, name string, endpoints []string) *control.GroupResult` — iterates conns, merges histograms
- [ ] **Step 3:** Create `cmd/worker/main.go`:
  - CLI flags: `-coordinator` (address, required), `-worker-id` (auto-generate UUID if empty)
  - Connect to coordinator
  - Run benchmark
  - Report results
  - Exit

**Acceptance Criteria:**
- Worker connects to coordinator, receives config
- Opens connections to assigned SUT groups
- Measures latency, sends final report
- Clean shutdown on SIGINT/SIGTERM

---

#### Phase 5: Integration test + deployment (3h)

**Goal:** End-to-end test with coordinator + 2 workers on localhost, deployment scripts.

**Files:**
- Create: `run-distributed-benchmark.sh`
- Create: `Dockerfile.worker`
- Create: `Dockerfile.coordinator`
- Create: `docker-compose.benchmark.yml`

- [ ] **Step 1:** Create `run-distributed-benchmark.sh`:
  - Start coordinator in background
  - Start N workers in background
  - Wait for coordinator to finish
  - Collect logs
- [ ] **Step 2:** Create `Dockerfile.worker` and `Dockerfile.coordinator` (multi-stage Go builds)
- [ ] **Step 3:** Create `docker-compose.benchmark.yml` with 1 coordinator + N workers
- [ ] **Step 4:** Run full E2E test: coordinator + 2 workers, 3 conns, 30s warmup, 60s measure
- [ ] **Step 5:** Compare distributed report with single-process baseline — verify totals match within 5%

**Acceptance Criteria:**
- Coordinator + 2 workers run successfully on localhost
- Final report shows merged results from all workers
- Latency percentiles are consistent with single-process run
- Docker deployment works

---

### Effort Estimate (Approach A)

| Phase | Effort | Description |
|-------|--------|-------------|
| Phase 1 | 4h | Refactor into packages |
| Phase 2 | 3h | Proto + codegen |
| Phase 3 | 5h | Coordinator binary |
| Phase 4 | 4h | Worker binary |
| Phase 5 | 3h | Integration + deployment |
| **Total** | **19h** | |

### Pros

1. **Low risk** — existing code moves to packages with minimal changes; single-process mode preserved as fallback
2. **Fast to implement** — no new patterns to learn, just code organization + thin gRPC layer
3. **Simple proto** — unary RPCs are easy to debug, no stream lifecycle management
4. **Incremental** — each phase produces working, testable software
5. **Low operational complexity** — unary RPCs, no keepalive tuning, no stream reconnection logic
6. **Backward compatible** — `main.go` still works as single-process client

### Cons

1. **No barrier synchronization** — workers start measuring at slightly different times (±500ms window)
2. **No worker health monitoring** — if a worker dies mid-test, coordinator hangs waiting for results
3. **No dynamic sharding** — groups assigned at registration time, cannot rebalance
4. **No live stats aggregation** — coordinator doesn't merge live snapshots for display
5. **Snapshot data transfer** — histograms sent as full snapshots, not deltas (more bandwidth)
6. **Coordinator is stateless between phases** — if coordinator crashes, no recovery

### Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Refactor breaks existing behavior | Low | High | Phase 1 has validation step |
| Clock skew between machines | Medium | Low | ±5ms is acceptable for load testing |
| Worker dies during measurement | Medium | Medium | Add timeout in `WaitForResults`, log which worker failed |
| Proto serialization overhead | Low | Low | HDR encoded histograms are ~1.5KB |
| gRPC connection issues | Low | Medium | Add retry logic in worker `Connect()` |

---

## Approach B: Full Distributed Architecture

### Architecture Overview

```
┌──────────────────────────────────────────────────────────────┐
│                       COORDINATOR                             │
│  ┌──────────┐  ┌──────────────┐  ┌────────────────────────┐  │
│  │ CLI/Config│  │ Phase Manager│  │ Live Stats Aggregator  │  │
│  └─────┬────┘  └──────┬───────┘  └───────────┬────────────┘  │
│        │              │                       │               │
│  ┌─────▼──────────────▼───────────────────────▼────────────┐  │
│  │        gRPC Bidi Streaming Server                        │  │
│  │  rpc ConnectWorker(stream WorkerMsg)                     │  │
│  │                 returns (stream CoordinatorCmd)           │  │
│  │                                                          │  │
│  │  Worker streams: REGISTER → HEARTBEAT → SNAPSHOT → FINAL │  │
│  │  Coordinator streams: CONFIG → PHASE_CHANGE → STOP       │  │
│  └────────────────────────┬─────────────────────────────────┘  │
│  ┌────────────────────────▼─────────────────────────────────┐  │
│  │  Health Monitor (goroutine)                               │  │
│  │  - Detect dead workers via missed heartbeats              │  │
│  │  - Evict stale workers, redistribute shards              │  │
│  └──────────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────────┘
           │ gRPC bidi stream (persistent)
           │ keepalive: 30s interval, 10s timeout
    ┌──────▼──────┐  ┌──────▼──────┐  ┌──────▼──────┐
    │   WORKER 0  │  │   WORKER 1  │  │   WORKER N  │
    │ ┌─────────┐ │  │ ┌─────────┐ │  │ ┌─────────┐ │
    │ │Shard    │ │  │ │Shard    │ │  │ │Shard    │ │
    │ │Executor │ │  │ │Executor │ │  │ │Executor │ │
    │ │         │ │  │ │         │ │  │ │         │ │
    │ │Send gor │ │  │ │Send gor │ │  │ │Send gor │ │
    │ │Recv gor │ │  │ │Recv gor │ │  │ │Recv gor │ │
    │ └────┬────┘ │  │ └────┬────┘ │  │ └────┬────┘ │
    │ ┌────▼────┐ │  │ ┌────▼────┐ │  │ ┌────▼────┐ │
    │ │Conn Pool│ │  │ │Conn Pool│ │  │ │Conn Pool│ │
    │ │→ SUT   │ │  │ │→ SUT   │ │  │ │→ SUT   │ │
    │ └─────────┘ │  │ └─────────┘ │  │ └─────────┘ │
    └──────────────┘  └──────────────┘  └──────────────┘
```

### Proto Schema

```protobuf
// benchmark-client/proto/control.proto
syntax = "proto3";
package control;

option go_package = "benchmark-client/proto/control";

service CoordinatorService {
  // Persistent bidi stream — worker keeps it open for entire test lifecycle
  rpc ConnectWorker(stream WorkerMessage) returns (stream CoordinatorCommand);
}

// ─── Worker → Coordinator ───

message WorkerMessage {
  oneof payload {
    RegisterRequest register = 1;
    Heartbeat heartbeat = 2;
    WorkerReady ready = 3;
    SnapshotMessage snapshot = 4;
    FinalReport final = 5;
    WorkerError error = 6;
  }
}

message RegisterRequest {
  string worker_id = 1;
  string hostname = 2;
  int32 cpu_count = 3;
  int64 memory_bytes = 4;
}

message Heartbeat {
  int64 timestamp_ms = 1;
  double cpu_usage = 2;
  uint64 memory_rss = 3;
}

message WorkerReady {
  int32 active_connections = 1;
  int32 total_connections = 2;
  repeated string connected_endpoints = 3;
}

message SnapshotMessage {
  int64 elapsed_ms = 1;
  repeated GroupSnapshot groups = 2;
}

message GroupSnapshot {
  string name = 1;
  int64 msg_count = 2;
  int64 byte_count = 3;
  int64 raw_count = 4;
  int64 raw_bytes = 5;
  int32 active_conns = 6;
  bytes latency_histogram = 7;  // HDR Encode() — full snapshot, coordinator diffs
}

message FinalReport {
  int64 measure_duration_ms = 1;
  repeated GroupResult groups = 2;
}

message GroupResult {
  string name = 1;
  repeated ConnResult connections = 2;
  bytes latency_histogram = 3;
  bytes reconnect_histogram = 4;
}

message ConnResult {
  int32 conn_index = 1;
  string endpoint = 2;
  int64 msg_count = 3;
  int64 byte_count = 4;
  int64 raw_count = 5;
  int64 raw_bytes = 6;
  int64 disconnect_count = 7;
  int64 reconnect_count = 8;
  bool first_msg_received = 9;
  bool conn_active = 10;
  bytes latency_histogram = 11;
  bytes reconnect_histogram = 12;
}

message WorkerError {
  string message = 1;
  bool fatal = 2;
}

// ─── Coordinator → Worker ───

message CoordinatorCommand {
  oneof payload {
    ConfigCommand config = 1;
    PhaseCommand phase = 2;
    StopCommand stop = 3;
    ReshardCommand reshard = 4;
  }
}

message ConfigCommand {
  repeated GroupAssignment groups = 1;
  int32 warmup_seconds = 2;
  int32 measure_seconds = 3;
  int64 measure_start_unix_ms = 4;
  bool disable_gc = 5;
}

message PhaseCommand {
  enum Phase {
    UNKNOWN = 0;
    CONNECTING = 1;
    WARMUP = 2;
    MEASURE = 3;
    COOLDOWN = 4;
    DONE = 5;
  }
  Phase phase = 1;
  int64 phase_start_unix_ms = 2;
}

message StopCommand {
  string reason = 1;
  bool emergency = 2;
}

message ReshardCommand {
  repeated GroupAssignment add_groups = 1;
  repeated string remove_groups = 2;
}
```

### Directory Structure

```
benchmark-client/go-client/
├── go.mod
├── go.sum
├── cmd/
│   ├── coordinator/
│   │   └── main.go
│   └── worker/
│       └── main.go
├── internal/
│   ├── stats/
│   │   ├── stats.go
│   │   ├── histogram.go
│   │   └── serialize.go
│   ├── worker/
│   │   ├── ws.go
│   │   ├── grpc.go
│   │   ├── ws_stats.go
│   │   ├── frame.go
│   │   ├── pool.go
│   │   ├── runner.go
│   │   └── stream.go         # bidi stream send/recv goroutines
│   ├── coordinator/
│   │   ├── server.go         # gRPC bidi stream server
│   │   ├── phase.go          # PhaseManager with state machine
│   │   ├── aggregator.go     # Live + final result aggregation
│   │   ├── shard.go          # Dynamic shard allocation
│   │   └── health.go         # Worker health monitor
│   └── report/
│       └── report.go
├── proto/
│   ├── benchmark.proto
│   ├── benchmark.pb.go
│   ├── benchmark_grpc.pb.go
│   ├── control.proto
│   ├── control.pb.go
│   └── control_grpc.pb.go
├── Makefile
├── Dockerfile.worker
├── Dockerfile.coordinator
├── docker-compose.benchmark.yml
└── main.go                   # single-process fallback
```

### Phase Breakdown

#### Phase 1: Refactor — Extract shared packages (4h)

Identical to Approach A Phase 1. Same files, same steps, same acceptance criteria.

---

#### Phase 2: Proto + bidi streaming codegen (3h)

**Goal:** Define the bidi streaming control plane protocol.

**Files:**
- Create: `proto/control.proto` (full schema above)
- Create: `Makefile`

- [ ] **Step 1:** Write `proto/control.proto` with bidi streaming schema above.
- [ ] **Step 2:** Create `Makefile` with proto generation.
- [ ] **Step 3:** Run `make proto`, verify `go build ./...` succeeds.
- [ ] **Step 4:** Write `internal/worker/stream.go` — bidi stream client wrapper:
  - `StreamClient` struct with `sendCh chan *control.WorkerMessage` (buffered 256)
  - `Connect(ctx, addr)` — opens bidi stream
  - `Send(msg)` — non-blocking send to channel
  - `Recv()` — blocking receive of `CoordinatorCommand`
  - Background send goroutine: drains `sendCh` → `stream.Send()`
  - Background recv goroutine: `stream.Recv()` → `recvCh`
  - Graceful close on context cancellation

**Acceptance Criteria:**
- Proto generates valid Go code
- StreamClient compiles and handles mock stream

---

#### Phase 3: Coordinator with bidi streaming (7h)

**Goal:** Full coordinator with phase state machine, live aggregation, health monitoring.

**Files:**
- Create: `cmd/coordinator/main.go`
- Create: `internal/coordinator/server.go`
- Create: `internal/coordinator/phase.go`
- Create: `internal/coordinator/aggregator.go`
- Create: `internal/coordinator/shard.go`
- Create: `internal/coordinator/health.go`

- [ ] **Step 1:** Create `internal/coordinator/shard.go`:
  - `ShardGroups(allGroups []Group, numWorkers int) [][]Group`
  - `Reshard(current map[string][]Group, deadWorkerID string, aliveWorkers []string) map[string][]Group` — redistribute dead worker's groups
- [ ] **Step 2:** Create `internal/coordinator/health.go`:
  - `HealthMonitor` struct: tracks last heartbeat per worker
  - `CheckLoop(ctx)` — runs every 15s, evicts workers silent > 60s
  - `OnEvict` callback: triggers reshard + send `ReshardCommand` to surviving workers
- [ ] **Step 3:** Create `internal/coordinator/aggregator.go`:
  - `LiveAggregator` — merges snapshots in real-time for live display
  - `FinalAggregator` — stores `FinalReport` per worker, provides merged data for report
  - `GetMergedStats() ([]report.GroupOutput, error)` — decoded + merged histograms
- [ ] **Step 4:** Create `internal/coordinator/phase.go`:
  - `PhaseManager` state machine: `IDLE → WAITING → CONNECTING → WARMUP → MEASURE → COOLDOWN → DONE`
  - State transitions triggered by worker messages or timeouts
  - `AdvanceTo(phase)` — sends `PhaseCommand` to all workers via their streams
  - Barrier computation: `measure_start_unix_ms = now + 500ms`
- [ ] **Step 5:** Create `internal/coordinator/server.go`:
  - Implement `ConnectWorker(stream)` — one goroutine per connected worker
  - Receive loop: switch on `oneof payload` type → dispatch to phase/health/aggregator
  - Access to per-worker send stream for pushing `CoordinatorCommand`
  - Thread-safe worker registry (sync.Map or mutex-protected map)
- [ ] **Step 6:** Create `cmd/coordinator/main.go`:
  - CLI flags: `-workers`, `-warmup`, `-duration`, `-conns`, `-listen`, `-heartbeat-timeout`
  - gRPC server with keepalive params (30s time, 10s timeout)
  - Main loop: start server → wait for workers → auto-advance phases → print report → exit
  - Live stats display: periodic merged stats printed every 30s during measurement

**Acceptance Criteria:**
- Coordinator manages full phase lifecycle via bidi streams
- Detects dead workers via heartbeat timeout
- Aggregates live snapshots and prints merged display
- Prints final merged report

---

#### Phase 4: Worker with bidi streaming (6h)

**Goal:** Worker with persistent bidi stream, barrier sync, heartbeat.

**Files:**
- Create: `cmd/worker/main.go`
- Create: `internal/worker/runner.go`
- Create: `internal/worker/stream.go` (from Phase 2)

- [ ] **Step 1:** Complete `internal/worker/stream.go` (started in Phase 2):
  - Heartbeat goroutine: sends `Heartbeat` every 10s
  - Reconnection logic: if stream breaks, reconnect with exponential backoff
  - Graceful drain: flush pending messages before closing
- [ ] **Step 2:** Create `internal/worker/runner.go`:
  - `Runner` struct: holds stream client, group assignments, stats
  - `Run(ctx)` lifecycle:
    1. Connect to coordinator via bidi stream
    2. Send `RegisterRequest`
    3. Receive `ConfigCommand` → parse groups + settings
    4. Launch connection goroutines
    5. Send `WorkerReady` when all conns established (or timeout)
    6. Receive `PhaseCommand(WARMUP)` → start warmup timer
    7. Optionally `debug.SetGCPercent(-1)` if `disable_gc` is true
    8. Receive `PhaseCommand(MEASURE)` with `phase_start_unix_ms`
    9. Sleep until barrier time, then set `measuring = true`
    10. Snapshot goroutine: every 10s, encode histograms → send `SnapshotMessage`
    11. After measure_seconds: set `measuring = false`
    12. Cancel connections, wait for goroutines
    13. Encode all results → send `FinalReport`
    14. Receive `PhaseCommand(DONE)` → exit
  - Handle `ReshardCommand`: add/remove groups dynamically
  - Handle `StopCommand`: graceful emergency shutdown
- [ ] **Step 3:** Create `internal/stats/serialize.go` (same as Approach A)
- [ ] **Step 4:** Create `cmd/worker/main.go`:
  - CLI flags: `-coordinator`, `-worker-id`, `-cpuset` (CPU pinning hint)
  - Optionally set `runtime.GOMAXPROCS()` based on cpuset
  - Create Runner, call `Run()`
  - Signal handling: SIGINT → send `WorkerError{fatal: true}` → graceful exit

**Acceptance Criteria:**
- Worker connects, receives config, runs connections
- Heartbeat sent every 10s
- Barrier synchronization: measurement starts within ±5ms of coordinator's target time
- Snapshots sent every 10s during measurement
- Final report sent with all connection details + encoded histograms
- Handles reshard commands

---

#### Phase 5: Integration test + deployment (4h)

**Goal:** Full E2E test with coordinator + 3 workers (1 remote), Docker deployment.

**Files:**
- Create: `run-distributed-benchmark.sh`
- Create: `Dockerfile.worker`
- Create: `Dockerfile.coordinator`
- Create: `docker-compose.benchmark.yml`

- [ ] **Step 1:** Create `run-distributed-benchmark.sh` — same as Approach A but with worker health check loop
- [ ] **Step 2:** Create Dockerfiles and compose file with keepalive settings
- [ ] **Step 3:** E2E test: coordinator + 3 workers (2 localhost + 1 remote if available), 3 conns, 30s warmup, 60s measure
- [ ] **Step 4:** Fault injection test: kill 1 worker mid-test → verify coordinator detects → verify remaining workers' report is correct
- [ ] **Step 5:** Live stats validation: verify coordinator prints merged throughput every 30s
- [ ] **Step 6:** Compare distributed report with single-process baseline

**Acceptance Criteria:**
- Full E2E with multiple workers succeeds
- Fault tolerance: coordinator handles worker death gracefully
- Barrier synchronization verified (timestamps within ±5ms)
- Live stats display shows aggregated data
- Docker deployment works

---

### Effort Estimate (Approach B)

| Phase | Effort | Description |
|-------|--------|-------------|
| Phase 1 | 4h | Refactor into packages (shared with A) |
| Phase 2 | 3h | Proto + stream client |
| Phase 3 | 7h | Coordinator (bidi + health + state machine) |
| Phase 4 | 6h | Worker (bidi + barrier + heartbeat) |
| Phase 5 | 4h | Integration + fault tolerance testing |
| **Total** | **24h** | |

### Pros

1. **Barrier synchronization** — all workers start measuring at the same instant (±5ms)
2. **Worker health monitoring** — dead workers detected and evicted automatically
3. **Live aggregated stats** — coordinator shows real-time merged throughput across all workers
4. **Dynamic resharding** — groups can be redistributed if a worker fails
5. **Phase state machine** — coordinator controls phase transitions, workers are simple executors
6. **Production-ready** — keepalive, reconnection, graceful shutdown, error reporting
7. **Extensible** — `oneof payload` pattern makes adding new messages trivial

### Cons

1. **More complex** — bidi streams require careful goroutine lifecycle management
2. **Higher implementation risk** — stream reconnection, backpressure, and race conditions are harder to debug
3. **More code** — ~40% more code than Approach A
4. **Harder to test** — need integration tests for stream behavior
5. **Keepalive tuning** — inappropriate keepalive params can cause false disconnections
6. **Overkill for 2-3 workers on LAN** — the complexity doesn't justify the benefit at small scale

### Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Bidi stream goroutine leak | Medium | High | Careful lifecycle management, leak detection in tests |
| Stream backpressure | Low | Medium | Buffered send channels, non-blocking sends with drop |
| Keepalive false disconnects | Medium | Medium | Tune keepalive params per network (LAN vs WAN) |
| Race conditions in phase transitions | Medium | High | Strict mutex discipline, go race detector in CI |
| Reshard complexity | High | Low | Ship without reshard first, add later |
| Barrier clock skew across machines | Low | Low | NTP sync, 500ms barrier delta for LAN |

---

## Recommendation

### **Recommended: Approach A (Minimal Viable Split)**

**Why:**

1. **The current use case is small-scale.** The benchmark runs 9 groups × 90 max connections = 810 connections total. This fits on 2-3 machines. The full distributed architecture (barrier sync, health monitoring, dynamic resharding) solves problems that don't exist at this scale.

2. **Time to value.** Approach A ships in 19h vs 24h for Approach B. The 5h savings is significant for what is ultimately a testing tool, not production infrastructure.

3. **Barrier sync is the one thing worth borrowing.** The unary approach already includes `measure_start_unix_ms` in `RegisterResponse`. Workers sleep until that timestamp. This gives ±5ms synchronization — identical to Approach B's barrier — without the bidi stream complexity.

4. **Incremental path to Approach B.** Approach A's proto uses `oneof` in `WorkerMessage`/`CoordinatorCommand` (or can be extended to). Upgrading to bidi streams later is a focused change: swap unary RPCs for `ConnectWorker` stream, add heartbeat goroutine. The refactored packages are identical in both approaches.

5. **Single-process fallback is preserved.** `main.go` continues to work. This is a safety net that Approach B also provides, but it's more valuable when the distributed version is simpler.

6. **Debugging is easier.** Unary RPCs show up clearly in gRPC logs. Each call is an independent request. Bidi streams require correlating send/receive across goroutines, which is harder to trace.

**When to upgrade to Approach B:**
- Workers spread across WAN (needs heartbeat + reconnection)
- Scale beyond 10 workers (needs dynamic sharding)
- Need real-time dashboard (needs live aggregated stats)
- Long-running soak tests (needs health monitoring)

---

## Implementation Order (Approach A)

```
Phase 1: Refactor ──────────── 4h
    │  (validate: single-process still works)
    ▼
Phase 2: Proto ─────────────── 3h
    │  (validate: codegen compiles)
    ▼
Phase 3: Coordinator ───────── 5h
    │  (validate: server starts, accepts connections)
    ▼
Phase 4: Worker ────────────── 4h
    │  (validate: worker connects, runs, reports)
    ▼
Phase 5: Integration ───────── 3h
    (validate: 2 workers, report matches baseline)
```

**Commit strategy:** One commit per phase with conventional commit format:
- `refactor: extract internal packages from main.go`
- `feat: add control plane proto schema`
- `feat: add coordinator binary`
- `feat: add worker binary`
- `feat: add distributed benchmark scripts and Docker deployment`

**Testing strategy:**
- Phase 1: Run single-process benchmark, compare output with original
- Phase 3-4: Unit tests for serialize/deserialize round-trip
- Phase 5: E2E test comparing distributed vs single-process results

---

## Appendix: Key Design Decisions

### Q: Should coordinator generate load?
**No.** Locust pattern — coordinator only orchestrates. This eliminates noise in measurements and keeps the coordinator simple.

### Q: Unary vs bidi streaming?
**Unary for Approach A.** Simpler to implement, easier to debug. Bidi streaming adds value for real-time control but isn't needed for the current use case.

### Q: How to handle histogram transfer?
**`hdrhistogram.Encode()` → `bytes` field in protobuf.** Encoded histograms are ~1.5KB compressed. The `Merge()` operation is lossless when histograms have the same config (same lowest/highest trackable value, same significant digits). Our config is consistent: 1µs–1hr, 3 sig digits.

### Q: How to split groups across workers?
**Static assignment at registration time.** Group-based sharding (not connection-level) is simpler and preserves per-group latency accuracy. If 9 groups / 3 workers = 3 groups per worker. Remainder distributed to first workers.

### Q: Should we keep `coder/websocket`?
**Yes.** Switching to `gorilla/websocket` would complicate the refactor for marginal throughput gain. The benchmark measures SUT performance, not client library performance. If the client library becomes a bottleneck, that's a separate optimization.

### Q: Single go.mod or multi-module?
**Single `go.mod`.** The coordinator and worker share 90% of their code (stats, worker connection logic, report formatting). Multi-module would create unnecessary duplication and version management overhead.
