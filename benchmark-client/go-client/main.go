package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/gorilla/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	pb "benchmark-client/proto"
)

type Group struct {
	Name      string
	Type      string
	Endpoints []string
}

type ConnStats struct {
	hist     *hdrhistogram.Histogram
	count    atomic.Int64
	bytes    atomic.Int64
	rawCount atomic.Int64
	rawBytes atomic.Int64
}

type GroupStats struct {
	conns []*ConnStats
}

var groups = []Group{
	{Name: "WS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8090", "ws://127.0.0.1:8090", "ws://127.0.0.1:8090"}},
	{Name: "gRPC bridge", Type: "grpc", Endpoints: []string{"localhost:50051", "localhost:50052", "localhost:50053"}},
	{Name: "gRPC host", Type: "grpc", Endpoints: []string{"localhost:60051", "localhost:60052", "localhost:60053"}},
}

func newConnStats() *ConnStats {
	return &ConnStats{
		hist: hdrhistogram.New(1, 60000000, 3),
	}
}

func newGroupStats(conns int) *GroupStats {
	gs := &GroupStats{
		conns: make([]*ConnStats, conns),
	}
	for i := range gs.conns {
		gs.conns[i] = newConnStats()
	}
	return gs
}

func recordLatency(cs *ConnStats, latencyMicros int64, payloadLen int) {
	cs.count.Add(1)
	cs.bytes.Add(int64(payloadLen))
	cs.hist.RecordValue(latencyMicros)
}

func connectWS(ctx context.Context, gi, ci int, endpoint string, stats []*GroupStats, measuring *atomic.Bool, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		dialer := websocket.DefaultDialer
		dialer.EnableCompression = false
		conn, _, err := dialer.DialContext(ctx, endpoint, http.Header{})
		if err != nil {
			log.Printf("[client] %s conn#%d connect failed: %v, retrying in 3s...", groups[gi].Name, ci+1, err)
			time.Sleep(3 * time.Second)
			continue
		}

		log.Printf("[client] %s conn#%d connected to %s", groups[gi].Name, ci+1, endpoint)

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Printf("[client] %s conn#%d disconnected: %v, reconnecting...", groups[gi].Name, ci+1, err)
				conn.Close()
				break
			}

			if !measuring.Load() {
				continue
			}

			var msg struct {
				Timestamp float64 `json:"timestamp"`
			}
			if err := json.Unmarshal(message, &msg); err != nil {
				continue
			}

			nowMicros := time.Now().UnixMicro()
			tsMicros := int64(msg.Timestamp * 1000)
			latencyMicros := nowMicros - tsMicros
			if latencyMicros > 0 {
				recordLatency(stats[gi].conns[ci], latencyMicros, len(message))
			}
		}

		conn.Close()
		time.Sleep(2 * time.Second)
	}
}

func connectGRPC(ctx context.Context, gi, ci int, endpoint string, stats []*GroupStats, measuring *atomic.Bool, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, err := grpc.NewClient(endpoint,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithInitialWindowSize(67108864),
			grpc.WithInitialConnWindowSize(134217728),
			grpc.WithDefaultCallOptions(
				grpc.MaxCallRecvMsgSize(10485760),
				grpc.MaxCallSendMsgSize(10485760),
			),
		)
		if err != nil {
			log.Printf("[client] %s conn#%d dial failed: %v, retrying in 3s...", groups[gi].Name, ci+1, err)
			time.Sleep(3 * time.Second)
			continue
		}

		client := pb.NewBenchmarkServiceClient(conn)
		stream, err := client.StreamMessages(ctx, &pb.StreamRequest{ClientId: fmt.Sprintf("bench-%d-%d", gi, ci)})
		if err != nil {
			log.Printf("[client] %s conn#%d stream failed: %v, retrying in 3s...", groups[gi].Name, ci+1, err)
			conn.Close()
			time.Sleep(3 * time.Second)
			continue
		}

		log.Printf("[client] %s conn#%d connected to %s", groups[gi].Name, ci+1, endpoint)

		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				conn.Close()
				break
			}
			if err != nil {
				log.Printf("[client] %s conn#%d recv error: %v, reconnecting...", groups[gi].Name, ci+1, err)
				break
			}

			cs := stats[gi].conns[ci]
			cs.rawCount.Add(1)
			cs.rawBytes.Add(int64(proto.Size(resp)))

			if !measuring.Load() {
				continue
			}

			raw := resp.GetPayload()
			nowMicros := time.Now().UnixMicro()
			tsMicros := int64(resp.GetTimestamp() * 1000)
			latencyMicros := nowMicros - tsMicros
			if latencyMicros > 0 {
				recordLatency(cs, latencyMicros, len(raw))
			}
		}

		conn.Close()
		time.Sleep(2 * time.Second)
	}
}

func mergeGroupHistogram(gs *GroupStats) *hdrhistogram.Histogram {
	merged := hdrhistogram.New(1, 60000000, 3)
	for _, cs := range gs.conns {
		merged.Merge(cs.hist)
	}
	return merged
}

func aggregateGroup(gs *GroupStats) (totalMsgs, totalBytes, totalRaw, totalRawBytes int64) {
	for _, cs := range gs.conns {
		totalMsgs += cs.count.Load()
		totalBytes += cs.bytes.Load()
		totalRaw += cs.rawCount.Load()
		totalRawBytes += cs.rawBytes.Load()
	}
	return
}

func main() {
	warmup := flag.Int("warmup", 30, "warmup seconds")
	duration := flag.Int("duration", 120, "measurement seconds")
	conns := flag.Int("conns", 20, "connections per group")
	flag.Parse()

	log.Printf("[client] Warmup: %ds, Measurement: %ds, Conns/group: %d", *warmup, *duration, *conns)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var measuring atomic.Bool
	stats := make([]*GroupStats, len(groups))
	for i := range stats {
		stats[i] = newGroupStats(*conns)
	}

	totalConns := 0
	var wg sync.WaitGroup

	for gi := range groups {
		epCount := len(groups[gi].Endpoints)
		for ci := 0; ci < *conns; ci++ {
			endpoint := groups[gi].Endpoints[ci%epCount]
			wg.Add(1)
			totalConns++
			if groups[gi].Type == "ws" {
				go connectWS(ctx, gi, ci, endpoint, stats, &measuring, &wg)
			} else {
				go connectGRPC(ctx, gi, ci, endpoint, stats, &measuring, &wg)
			}
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("[client] %d total connections across %d groups connecting...", totalConns, len(groups))
	time.Sleep(5 * time.Second)

	log.Printf("[client] Warmup for %ds...", *warmup)
	select {
	case <-time.After(time.Duration(*warmup) * time.Second):
	case <-sigCh:
		cancel()
		return
	}

	log.Printf("[client] Measurement phase (%ds)...", *duration)
	measuring.Store(true)
	measureStart := time.Now()

	select {
	case <-time.After(time.Duration(*duration) * time.Second):
	case <-sigCh:
	}
	measuring.Store(false)
	measureEnd := time.Now()
	cancel()

	measureDuration := measureEnd.Sub(measureStart).Seconds()

	fmt.Println("\n=== THROUGHPUT RESULTS (processed) ===\n")
	fmt.Printf("%-16s %8s %12s %10s %12s\n", "Group", "Conns", "Msgs", "MB/s", "msg/s")
	fmt.Println(strings.Repeat("-", 60))

	throughputs := make([]float64, len(groups))
	for gi := range groups {
		totalMsgs, totalBytes, _, _ := aggregateGroup(stats[gi])
		mbps := float64(totalBytes) / 1024 / 1024 / measureDuration
		msgPerSec := float64(totalMsgs) / measureDuration
		throughputs[gi] = mbps
		fmt.Printf("%-16s %8d %12d %10.2f %12.0f\n", groups[gi].Name, *conns, totalMsgs, mbps, msgPerSec)
	}

	fmt.Println("\n=== RAW RECV STATS (gRPC only) ===\n")
	fmt.Printf("%-16s %10s %10s %12s %10s\n", "Group", "Raw Msgs", "Raw MB/s", "Raw msg/s", "Drop %")
	fmt.Println(strings.Repeat("-", 60))
	for gi := range groups {
		if groups[gi].Type != "grpc" {
			continue
		}
		totalMsgs, _, totalRaw, totalRawBytes := aggregateGroup(stats[gi])
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

	fmt.Println("\n=== LATENCY RESULTS ===\n")
	groupMerged := make([]*hdrhistogram.Histogram, len(groups))
	for gi := range groups {
		groupMerged[gi] = mergeGroupHistogram(stats[gi])
	}

	percentiles := []string{"p50", "p75", "p90", "p95", "p99", "p99.9"}
	pctValues := []float64{50, 75, 90, 95, 99, 99.9}

	fmt.Printf("╔══════════╦════════════╦══════════════╦══════════════╦════════════╦════════════╗\n")
	fmt.Printf("║ Pctl     ║%s║%s║%s║%s║%s║\n",
		pad("WS (host/PM2)", 12), pad("gRPC bridge", 14), pad("gRPC host", 14), pad("bridge-WS Δ", 12), pad("host-WS Δ", 12))
	fmt.Printf("╠══════════╬════════════╬══════════════╬══════════════╬════════════╬════════════╣\n")

	for i := range percentiles {
		vals := make([]float64, len(groups))
		for gi := range groups {
			vals[gi] = float64(groupMerged[gi].ValueAtPercentile(pctValues[i])) / 1000.0
		}
		d1 := vals[1] - vals[0]
		d2 := vals[2] - vals[0]
		fmt.Printf("║ %s ║%s║%s║%s║%s║%s║\n",
			pad(percentiles[i], 8),
			pad(fmt.Sprintf("%.3f", vals[0]), 12),
			pad(fmt.Sprintf("%.3f", vals[1]), 14),
			pad(fmt.Sprintf("%.3f", vals[2]), 14),
			pad(fmt.Sprintf("%+.3f", d1), 12),
			pad(fmt.Sprintf("%+.3f", d2), 12))
	}
	fmt.Printf("╚══════════╩════════════╩══════════════╩══════════════╩════════════╩════════════╝\n")

	fmt.Println("\nPer-group connection summary:")
	for gi := range groups {
		totalMsgs, totalBytes, totalRaw, _ := aggregateGroup(stats[gi])
		fmt.Printf("  %s (%d conns): %d msgs, %.2f MB/s",
			groups[gi].Name, *conns, totalMsgs, float64(totalBytes)/1024/1024/measureDuration)
		if groups[gi].Type == "grpc" && totalRaw > 0 {
			dropPct := float64(totalRaw-totalMsgs) / float64(totalRaw) * 100
			fmt.Printf(", raw=%d, drop=%.1f%%", totalRaw, dropPct)
		}
		fmt.Println()
	}

	var grandMsgs, grandBytes int64
	for gi := range groups {
		msgs, bts, _, _ := aggregateGroup(stats[gi])
		grandMsgs += msgs
		grandBytes += bts
	}
	fmt.Printf("\nAggregate: %d msgs, %.2f MB across %d groups x %d conns\n",
		grandMsgs, float64(grandBytes)/1024/1024, len(groups), *conns)
	fmt.Printf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}

func pad(s string, w int) string {
	for len(s) < w {
		s = " " + s
	}
	return s
}
