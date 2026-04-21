---
phase: 3
title: "Go Client Update"
description: "Add uWS benchmark groups to the Go benchmark client"
depends_on: [phase-01, phase-02]
---

# Phase 3: Go Client Update

## Goal

Add 3 new WS groups for uWS endpoints to the Go benchmark client's `groups` slice. The existing `connectWS` function works unchanged — uWS is protocol-compatible with standard WebSocket.

## File to Modify

### `benchmark-client/go-client/main.go`

#### Change 1: Add uWS groups (line ~47-51)

**Current:**
```go
var groups = []Group{
    {Name: "WS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8090", "ws://127.0.0.1:8090", "ws://127.0.0.1:8090"}},
    {Name: "gRPC bridge", Type: "grpc", Endpoints: []string{"localhost:50051", "localhost:50052", "localhost:50053"}},
    {Name: "gRPC host", Type: "grpc", Endpoints: []string{"localhost:60051", "localhost:60052", "localhost:60053"}},
}
```

**New:**
```go
var groups = []Group{
    {Name: "WS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8090", "ws://127.0.0.1:8090", "ws://127.0.0.1:8090"}},
    {Name: "uWS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8091", "ws://127.0.0.1:8091", "ws://127.0.0.1:8091"}},
    {Name: "uWS bridge", Type: "ws", Endpoints: []string{"ws://127.0.0.1:50061", "ws://127.0.0.1:50062", "ws://127.0.0.1:50063"}},
    {Name: "uWS host", Type: "ws", Endpoints: []string{"ws://127.0.0.1:60061", "ws://127.0.0.1:60062", "ws://127.0.0.1:60063"}},
    {Name: "gRPC bridge", Type: "grpc", Endpoints: []string{"localhost:50051", "localhost:50052", "localhost:50053"}},
    {Name: "gRPC host", Type: "grpc", Endpoints: []string{"localhost:60051", "localhost:60052", "localhost:60053"}},
}
```

#### Change 2: Update latency table header (lines ~405-425)

The latency results table has hardcoded column headers for 3 groups. With 6 groups, the table needs restructuring.

**Option A: Dynamic table** — Generate columns from `groups` slice at runtime.

**Option A is recommended** — hardcoded table is fragile. Use a loop:

```go
// Header
fmt.Printf("%-16s", "Pctl")
for gi := range groups {
    fmt.Printf(" %14s", groups[gi].Name)
}
fmt.Println()

// Separator
fmt.Printf("%-16s", "")
for range groups {
    fmt.Printf(" %14s", strings.Repeat("-", 14))
}
fmt.Println()

// Percentile rows
for i := range percentiles {
    fmt.Printf("%-16s", percentiles[i])
    for gi := range groups {
        val := float64(groupMerged[gi].ValueAtPercentile(pctValues[i])) / 1000.0
        fmt.Printf(" %14.3f", val)
    }
    fmt.Println()
}
```

#### Change 3: Update throughput comparison baseline (lines ~383-394)

Current code uses `wsThroughput := throughputs[0]` (group index 0 = WS) and compares all others against it. This should remain — group 0 is still the ws baseline. The delta calculation loop should skip WS groups when comparing:

```go
wsThroughput := throughputs[0]
if wsThroughput > 0 {
    fmt.Println(strings.Repeat("-", 60))
    for gi := 1; gi < len(groups); gi++ {
        delta := (throughputs[gi] - wsThroughput) / wsThroughput * 100
        sign := ""
        if delta >= 0 { sign = "+" }
        fmt.Printf("%-16s %s%.1f%% vs WS throughput\n", groups[gi].Name, sign, delta)
    }
}
```

No change needed — the loop already iterates from index 1 and the comparison is correct.

#### Change 4: No changes needed to `connectWS`

The `connectWS` function (lines 69-134) works unchanged because:
- uWS is standard WebSocket protocol
- `gorilla/websocket` dialer connects without issues
- Message format is identical (JSON with `timestamp` field)
- Latency measurement via `msg.Timestamp * 1000` works the same

## Acceptance Criteria

- [ ] `go build` succeeds
- [ ] Client connects to all 6 groups (3 WS + 3 uWS endpoints... wait, 6 groups total: 1 ws + 3 uWS + 2 gRPC)
- [ ] WS and uWS connections use same `connectWS` code path
- [ ] Latency histogram recorded for uWS connections
- [ ] Throughput comparison shows uWS vs WS delta
- [ ] Per-connection breakdown lists all groups
