package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"benchmark-client/internal/coordinator"
	"benchmark-client/internal/stats"

	pb "benchmark-client/proto/control"

	"github.com/HdrHistogram/hdrhistogram-go"
	"google.golang.org/grpc"
)

func main() {
	workers := flag.Int("workers", 2, "expected number of workers")
	warmup := flag.Int("warmup", 30, "warmup seconds")
	duration := flag.Int("duration", 120, "measurement seconds")
	conns := flag.Int("conns", 1, "connections per group")
	listen := flag.String("listen", ":50000", "gRPC listen address")
	flag.Parse()

	groups := []stats.Group{
		{Name: "WS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8090", "ws://127.0.0.1:8090", "ws://127.0.0.1:8090"}},
		{Name: "uWS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8091", "ws://127.0.0.1:8091", "ws://127.0.0.1:8091"}},
		{Name: "Go WS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8092", "ws://127.0.0.1:8093", "ws://127.0.0.1:8094"}},
		{Name: "uWS bridge", Type: "ws", Endpoints: []string{"ws://127.0.0.1:50061", "ws://127.0.0.1:50061", "ws://127.0.0.1:50061"}},
		{Name: "Go WS bridge", Type: "ws", Endpoints: []string{"ws://127.0.0.1:50071", "ws://127.0.0.1:50071", "ws://127.0.0.1:50071"}},
		{Name: "uWS host", Type: "ws", Endpoints: []string{"ws://127.0.0.1:60061", "ws://127.0.0.1:60062", "ws://127.0.0.1:60063"}},
		{Name: "Go WS host", Type: "ws", Endpoints: []string{"ws://127.0.0.1:60071", "ws://127.0.0.1:60072", "ws://127.0.0.1:60073"}},
		{Name: "gRPC bridge", Type: "grpc", Endpoints: []string{"localhost:50051", "localhost:50051", "localhost:50051"}},
		{Name: "gRPC host", Type: "grpc", Endpoints: []string{"localhost:60051", "localhost:60052", "localhost:60053"}},
	}

	sharded := coordinator.ShardGroups(groups, *workers)

	pbShards := make([][]pb.GroupAssignment, len(sharded))
	for i, shard := range sharded {
		for _, g := range shard {
			pbShards[i] = append(pbShards[i], pb.GroupAssignment{
				Name:        g.Name,
				Type:        g.Type,
				Endpoints:   g.Endpoints,
				Connections: int32(*conns),
			})
		}
	}

	phase := coordinator.NewPhaseManager(*workers, *warmup, *duration)
	phase.ComputeBarrierTime()
	aggregator := coordinator.NewAggregator()
	srv := coordinator.NewServer(phase, aggregator, pbShards, *workers)

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("[coordinator] Failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	coordinator.RegisterServer(grpcServer, srv)

	go func() {
		log.Printf("[coordinator] Listening on %s, expecting %d workers", *listen, *workers)
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("[coordinator] gRPC server error: %v", err)
		}
	}()

	log.Printf("[coordinator] Waiting for %d workers to register (timeout 5m)...", *workers)
	if err := phase.WaitForWorkers(context.Background(), 5*time.Minute); err != nil {
		log.Fatalf("[coordinator] Timeout waiting for workers: %v", err)
	}
	log.Printf("[coordinator] All workers registered. Measurement barrier at %d",
		phase.GetMeasureStartMs())

	measureSec := phase.GetMeasureDuration()
	barrierTime := time.UnixMilli(phase.GetMeasureStartMs())
	waitDuration := time.Until(barrierTime) + time.Duration(measureSec)*time.Second + 30*time.Second

	log.Printf("[coordinator] Waiting for results (timeout %v)...", waitDuration)
	if err := phase.WaitForResults(context.Background(), waitDuration); err != nil {
		log.Printf("[coordinator] Timeout waiting for results: %v", err)
	}

	grpcServer.GracefulStop()

	log.SetOutput(os.Stderr)
	fmt.Fprintf(os.Stderr, "\n")

	groupStats := aggregator.MergeToGroupStats()
	measureDuration := float64(measureSec)

	allGroupStats := make([]*stats.GroupStats, len(groups))
	for i, g := range groups {
		if gs, ok := groupStats[g.Name]; ok {
			allGroupStats[i] = gs
		} else {
			allGroupStats[i] = stats.NewGroupStats(0)
		}
	}

	printDistributedReport(groups, allGroupStats, measureDuration, *conns, *workers)
}

func printDistributedReport(groups []stats.Group, allStats []*stats.GroupStats, measureDuration float64, conns int, numWorkers int) {
	fmt.Print("\n=== THROUGHPUT RESULTS (processed) ===\n\n")
	fmt.Printf("%-16s %8s %12s %10s %12s\n", "Group", "Conns", "Msgs", "MB/s", "msg/s")
	fmt.Println(strings.Repeat("-", 60))

	throughputs := make([]float64, len(groups))
	for gi := range groups {
		totalMsgs, totalBytes, _, _ := stats.AggregateGroup(allStats[gi])
		mbps := float64(totalBytes) / 1024 / 1024 / measureDuration
		msgPerSec := float64(totalMsgs) / measureDuration
		throughputs[gi] = mbps
		fmt.Printf("%-16s %8d %12d %10.2f %12.0f\n", groups[gi].Name, conns*numWorkers, totalMsgs, mbps, msgPerSec)
	}

	fmt.Print("\n=== RAW RECV STATS ===\n\n")
	fmt.Printf("%-16s %10s %10s %12s %10s\n", "Group", "Raw Msgs", "Raw MB/s", "Raw msg/s", "Drop %")
	fmt.Println(strings.Repeat("-", 60))
	for gi := range groups {
		totalMsgs, _, totalRaw, totalRawBytes := stats.AggregateGroup(allStats[gi])
		rawMbs := float64(totalRawBytes) / 1024 / 1024 / measureDuration
		rawMsgSec := float64(totalRaw) / measureDuration
		dropPct := 0.0
		if totalRaw > 0 {
			dropPct = float64(totalRaw-totalMsgs) / float64(totalRaw) * 100
		}
		fmt.Printf("%-16s %10d %10.2f %12.0f %9.1f%%\n", groups[gi].Name, totalRaw, rawMbs, rawMsgSec, dropPct)
	}

	wsThroughput := throughputs[0]
	if wsThroughput > 0 {
		fmt.Println(strings.Repeat("-", 60))
		for gi := 1; gi < len(groups); gi++ {
			delta := (throughputs[gi] - wsThroughput) / wsThroughput * 100
			sign := ""
			if delta >= 0 {
				sign = "+"
			}
			fmt.Printf("%-16s %s%.1f%% vs WS throughput\n", groups[gi].Name, sign, delta)
		}
	}

	fmt.Print("\n=== LATENCY RESULTS ===\n\n")
	groupMerged := make([]*hdrhistogram.Histogram, len(groups))
	fmt.Printf("%-16s %12s\n", "Group", "Latency samples")
	fmt.Println(strings.Repeat("-", 30))
	for gi := range groups {
		groupMerged[gi] = stats.MergeGroupHistogram(allStats[gi])
		fmt.Printf("%-16s %12d\n", groups[gi].Name, groupMerged[gi].TotalCount())
	}
	fmt.Println()

	percentiles := []string{"p50", "p75", "p90", "p95", "p99", "p99.9"}
	pctValues := []float64{50, 75, 90, 95, 99, 99.9}

	fmt.Printf("%-10s", "Pctl")
	for gi := range groups {
		fmt.Printf(" %14s", groups[gi].Name)
	}
	fmt.Println()
	fmt.Print(strings.Repeat("-", 10))
	for range groups {
		fmt.Print(strings.Repeat("-", 15))
	}
	fmt.Println()

	for i := range percentiles {
		fmt.Printf("%-10s", percentiles[i])
		for gi := range groups {
			val := float64(groupMerged[gi].ValueAtPercentile(pctValues[i])) / 1000.0
			fmt.Printf(" %14.3f", val)
		}
		fmt.Println()
	}

	fmt.Print("\nDelta vs WS (host/PM2):\n")
	fmt.Printf("%-10s", "Pctl")
	for gi := 1; gi < len(groups); gi++ {
		fmt.Printf(" %14s", groups[gi].Name)
	}
	fmt.Println()
	for i := range percentiles {
		fmt.Printf("%-10s", percentiles[i])
		wsVal := float64(groupMerged[0].ValueAtPercentile(pctValues[i])) / 1000.0
		for gi := 1; gi < len(groups); gi++ {
			val := float64(groupMerged[gi].ValueAtPercentile(pctValues[i])) / 1000.0
			fmt.Printf(" %+14.3f", val-wsVal)
		}
		fmt.Println()
	}

	fmt.Print("\n=== CONNECTION STABILITY ===\n\n")
	fmt.Printf("%-16s %10s %10s %12s %12s %12s\n", "Group", "Disconnects", "Reconnects", "Reconn p50", "Reconn p99", "Reconn max")
	fmt.Println(strings.Repeat("-", 76))
	for gi := range groups {
		totalDisc, totalReconn := int64(0), int64(0)
		mergedReconn := hdrhistogram.New(1, 30000000, 3)
		for _, cs := range allStats[gi].Conns {
			totalDisc += cs.DisconnectCount.Load()
			totalReconn += cs.ReconnectCount.Load()
			mergedReconn.Merge(cs.ReconnectHist)
		}
		p50 := float64(mergedReconn.ValueAtPercentile(50)) / 1000.0
		p99 := float64(mergedReconn.ValueAtPercentile(99)) / 1000.0
		maxVal := float64(mergedReconn.Max()) / 1000.0
		if totalDisc == 0 {
			fmt.Printf("%-16s %10d %10d %12s %12s %12s\n", groups[gi].Name, totalDisc, totalReconn, "-", "-", "-")
		} else {
			fmt.Printf("%-16s %10d %10d %10.1fms %10.1fms %10.1fms\n", groups[gi].Name, totalDisc, totalReconn, p50, p99, maxVal)
		}
	}

	fmt.Printf("\nAggregate across %d workers: %d groups x %d conns\n", numWorkers, len(groups), conns)
	fmt.Printf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}
