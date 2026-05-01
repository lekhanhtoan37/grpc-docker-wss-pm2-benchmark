---
title: "Phase 04: NATS Subscriber in Benchmark Client"
phase: 4-of-5
status: pending
priority: P1
effort: 3h
blocked-by: [phase-01, phase-02]
blocks: [phase-05]
---

# Phase 04: NATS Subscriber in Benchmark Client

## Context

- Benchmark client at `benchmark-client/go-client/`
- Existing workers: `worker/ws.go` (WebSocket), `worker/grpc.go` (gRPC)
- Stats pipeline: `stats/stats.go` → `stats/histogram.go` → `report/report.go`
- Main entry: `main.go` defines 9 groups, dispatches to `ConnectWS` or `ConnectGRPC`
- Distributed mode: `worker/runner.go` handles group type dispatch

## Overview

Add `ConnectNATS()` function to benchmark client worker package. Add NATS groups to main.go and runner.go. Reuse existing stats pipeline (ConnStats, GroupStats, HDR histogram).

## Requirements

### Functional
- Subscribe to NATS subject `benchmark.messages`
- Support queue groups for load-balanced subscription
- Parse batch messages (split by `\n`, same as WS frames)
- Extract timestamp → compute latency (same formula as WS)
- Track ConnStats: Count, Bytes, Hist, ConnActive, etc.
- Auto-reconnect on NATS connection loss
- Work with existing warmup/measurement phases

### Non-Functional
- Same performance characteristics as WS worker
- Zero allocation in hot path (reuse buffers)
- Use `nats.ChanSubscribe()` for throughput (avoids callback overhead)
- Handle backpressure gracefully

## Architecture

### New File: `worker/nats.go`

```
ConnectNATS(ctx, group, gi, ci, natsURL, subject, allStats, measuring, wg)
  ├── nc, _ = nats.Connect(natsURL, opts...)
  ├── ch := make(chan *nats.Msg, 4096)
  ├── sub, _ = nc.ChanSubscribe(subject, ch)
  │   (or nc.ChanQueueSubscribe for queue groups)
  ├── Stats worker goroutine (same as WSStatsWorker pattern)
  └── Main loop:
      ├── msg := <-ch
      ├── split msg.Data by \n
      ├── ExtractTimestampInt64 per line
      ├── latency = nowMicros - ts*1000
      ├── Send NATSFrameEvent to stats worker
      └── Handle ctx.Done() → sub.Unsubscribe() → nc.Close()
```

### NATS-Specific Stats Worker: `worker/nats_stats.go`

Same pattern as `ws_stats.go`:

```go
type NATSFrameEvent struct {
    MsgCount int64
    ByteSize int64
    Samples  []int64
}

func NATSStatsWorker(cs *stats.ConnStats, in <-chan NATSFrameEvent, wg *sync.WaitGroup) {
    // Same logic as WSStatsWorker — batch atomic updates + histogram records
}
```

Actually, `NATSFrameEvent` is identical to `WSFrameEvent`. Reuse `WSFrameEvent` and `WSStatsWorker` directly. Just rename to `FrameEvent` and `FrameStatsWorker` — OR keep WS naming and use same types.

**Decision: Reuse WSFrameEvent and WSStatsWorker.** They are transport-agnostic. Just send events from NATS subscriber using same types.

## Implementation Steps

1. **Add `github.com/nats-io/nats.go` to `benchmark-client/go-client/go.mod`**

   ```bash
   cd benchmark-client/go-client
   go get github.com/nats-io/nats.go@latest
   ```

2. **Create `benchmark-client/go-client/internal/worker/nats.go`**

   ```go
   package worker

   import (
       "context"
       "log"
       "sync"
       "sync/atomic"
       "time"

       "benchmark-client/internal/stats"
       "github.com/nats-io/nats.go"
   )

   func ConnectNATS(ctx context.Context, group stats.Group, gi, ci int,
       natsURL, subject string, allStats []*stats.GroupStats,
       measuring *atomic.Bool, wg *sync.WaitGroup) {

       defer wg.Done()
       cs := allStats[gi].Conns[ci]

       events := make(chan WSFrameEvent, 2048)
       var statsWG sync.WaitGroup
       statsWG.Add(1)
       go WSStatsWorker(cs, events, &statsWG)
       defer func() {
           close(events)
           statsWG.Wait()
       }()

       for {
           select {
           case <-ctx.Done():
               return
           default:
           }

           nc, err := nats.Connect(natsURL,
               nats.Name(fmt.Sprintf("bench-%d-%d", gi, ci)),
               nats.ReconnectWait(2*time.Second),
               nats.MaxReconnects(-1),
               nats.BufferSize(8*1024*1024),
           )
           if err != nil {
               log.Printf("[client] %s conn#%d NATS connect error: %v", group.Name, ci+1, err)
               time.Sleep(3 * time.Second)
               continue
           }

           ch := make(chan *nats.Msg, 8192)
           sub, err := nc.ChanSubscribe(subject, ch)
           if err != nil {
               log.Printf("[client] %s conn#%d NATS subscribe error: %v", group.Name, ci+1, err)
               nc.Close()
               time.Sleep(3 * time.Second)
               continue
           }

           cs.ConnActive.Store(true)
           cs.FirstMsg.Store(false) // will set on first message
           log.Printf("[client] %s conn#%d connected to NATS %s", group.Name, ci+1, natsURL)

           for {
               select {
               case <-ctx.Done():
                   sub.Unsubscribe()
                   nc.Close()
                   return
               case msg, ok := <-ch:
                   if !ok {
                       cs.DisconnectCount.Add(1)
                       cs.ConnActive.Store(false)
                       break
                   }

                   if !cs.FirstMsg.Load() {
                       cs.FirstMsg.Store(true)
                   }

                   if !measuring.Load() {
                       continue
                   }

                   data := msg.Data
                   if len(data) == 0 {
                       continue
                   }

                   var samples []int64
                   nowMicros := time.Now().UnixMicro()
                   msgCount := 0
                   start := 0

                   for start < len(data) {
                       end := start
                       for end < len(data) && data[end] != '\n' {
                           end++
                       }
                       if end > start {
                           msgCount++
                           ts := ExtractTimestampInt64(data[start:end])
                           if ts > 0 {
                               lat := nowMicros - ts*1000
                               if lat < 1 {
                                   lat = 1
                               }
                               samples = append(samples, lat)
                           }
                       }
                       start = end + 1
                   }

                   if msgCount > 0 {
                       events <- WSFrameEvent{
                           MsgCount: int64(msgCount),
                           ByteSize: int64(len(data)),
                           Samples:  samples,
                       }
                   }
               }
           }

           nc.Close()
           time.Sleep(500 * time.Millisecond)
       }
   }
   ```

3. **Modify `benchmark-client/go-client/main.go`** — add NATS groups

   Add 3 new groups after existing 9:

   ```go
   {Name: "NATS bridge", Type: "nats", Endpoints: []string{"nats://localhost:4222"}},
   {Name: "NATS host", Type: "nats", Endpoints: []string{"nats://localhost:4222", "nats://localhost:4222", "nats://localhost:4222"}},
   {Name: "NATS PM2", Type: "nats", Endpoints: []string{"nats://localhost:4222", "nats://localhost:4222", "nats://localhost:4222"}},
   ```

   Note: All NATS subscribers connect to same NATS server. Endpoints list controls how many subscriptions per group. Queue group name differentiates bridge/host/PM2 subscribers.

   NATS-specific: endpoint string = NATS URL, subject = `benchmark.messages`. Pass subject as additional config (env var or group metadata).

   **Refined approach**: Add `Subject` and `QueueGroup` fields to `stats.Group`:

   ```go
   type Group struct {
       Name      string
       Type      string
       Endpoints []string
       Subject   string   // NATS subject (empty for ws/grpc)
       QueueGroup string  // NATS queue group (empty for no queue)
   }
   ```

4. **Modify dispatch in `main.go`**

   ```go
   if groups[gi].Type == "ws" {
       go worker.ConnectWS(ctx, groups[gi], gi, ci, endpoint, allStats, &measuring, &wg)
   } else if groups[gi].Type == "grpc" {
       go worker.ConnectGRPC(ctx, groups[gi], gi, ci, endpoint, allStats, &measuring, &wg)
   } else if groups[gi].Type == "nats" {
       go worker.ConnectNATS(ctx, groups[gi], gi, ci, endpoint, groups[gi].Subject, allStats, &measuring, &wg)
   }
   ```

5. **Modify `worker/runner.go`** — add NATS dispatch

   Same pattern as main.go dispatch. Add `"nats"` case in runner's connection loop.

6. **Update `stats.Group` struct** — add Subject + QueueGroup fields

   ```go
   type Group struct {
       Name       string
       Type       string
       Endpoints  []string
       Subject    string
       QueueGroup string
   }
   ```

7. **Update `stats/serialize.go`** — handle new Group fields in proto conversion

   The `GroupResult` and group assignment protos need Subject + QueueGroup fields. Add to proto definition.

8. **Update proto/control.proto** — add NATS fields

   ```protobuf
   message GroupAssignment {
       string name = 1;
       string type = 2;
       repeated string endpoints = 3;
       int32 connections = 4;
       string subject = 5;       // NEW
       string queue_group = 6;   // NEW
   }
   ```

9. **Regenerate protobuf code**

   ```bash
   cd benchmark-client/go-client
   protoc --go_out=. --go-grpc_out=. proto/control/control.proto
   ```

10. **Update `report/report.go`** — NATS groups appear automatically

    No changes needed. Report iterates groups by index, prints stats per group. NATS groups will appear alongside WS/gRPC.

## Files

| File | Action |
|------|--------|
| `benchmark-client/go-client/internal/worker/nats.go` | CREATE |
| `benchmark-client/go-client/internal/stats/stats.go` | MODIFY — add Subject, QueueGroup to Group |
| `benchmark-client/go-client/main.go` | MODIFY — add NATS groups + dispatch |
| `benchmark-client/go-client/internal/worker/runner.go` | MODIFY — add NATS dispatch |
| `benchmark-client/go-client/go.mod` | MODIFY — add nats.go dependency |
| `benchmark-client/go-client/proto/control/control.proto` | MODIFY — add subject, queue_group |
| `benchmark-client/go-client/proto/control/control.pb.go` | REGENERATE |
| `benchmark-client/go-client/proto/control/control_grpc.pb.go` | REGENERATE |

## Todo Checklist

- [ ] Add nats.go dependency to go.mod
- [ ] Create worker/nats.go (ConnectNATS function)
- [ ] Add Subject, QueueGroup fields to stats.Group
- [ ] Add NATS groups to main.go
- [ ] Add NATS dispatch case in main.go
- [ ] Add NATS dispatch case in runner.go
- [ ] Update proto/control.proto with NATS fields
- [ ] Regenerate protobuf code
- [ ] Test: single NATS group with local NATS server
- [ ] Test: NATS group alongside WS/gRPC groups
- [ ] Verify report includes NATS latency/throughput
- [ ] Verify delta comparison vs WS baseline includes NATS

## Success Criteria

- `ConnectNATS()` subscribes to NATS subject
- Receives messages published by nats-worker
- Latency measurements in HDR histogram
- Stats appear in benchmark report alongside WS/gRPC
- No regression to existing WS/gRPC functionality
- Reconnection on NATS disconnect works

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| NATS subscriber too fast (overwhelms stats) | Low | Low | Use channel buffer + stats worker pattern |
| Proto changes break distributed mode | Medium | High | Test coordinator+worker with NATS groups |
| Group struct change breaks existing code | Low | High | Empty Subject/QueueGroup for WS/gRPC groups |
| Queue group semantics differ from WS fanout | Medium | Medium | Document: NATS queue = load-balanced, WS = broadcast |
