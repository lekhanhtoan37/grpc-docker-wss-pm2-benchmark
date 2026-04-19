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

	pb "benchmark-client/proto"
)

type Group struct {
	Name      string
	Type      string
	Endpoints []string
}

type EndpointStats struct {
	hist   *hdrhistogram.Histogram
	count  atomic.Int64
	bytes  atomic.Int64
}

type GroupStats struct {
	endpoints [3]*EndpointStats
}

var groups = []Group{
	{Name: "WS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8090", "ws://127.0.0.1:8090", "ws://127.0.0.1:8090"}},
	{Name: "gRPC bridge", Type: "grpc", Endpoints: []string{"localhost:50051", "localhost:50052", "localhost:50053"}},
	{Name: "gRPC host", Type: "grpc", Endpoints: []string{"localhost:60051", "localhost:60052", "localhost:60053"}},
}

func newEndpointStats() *EndpointStats {
	return &EndpointStats{
		hist: hdrhistogram.New(1, 60000000, 3),
	}
}

func newGroupStats() *GroupStats {
	gs := &GroupStats{}
	for i := range gs.endpoints {
		gs.endpoints[i] = newEndpointStats()
	}
	return gs
}

func recordLatency(es *EndpointStats, latencyMicros int64, payloadLen int) {
	es.count.Add(1)
	es.bytes.Add(int64(payloadLen))
	es.hist.RecordValue(latencyMicros)
}

func connectWS(ctx context.Context, gi, ei int, endpoint string, stats []*GroupStats, measuring *atomic.Bool, wg *sync.WaitGroup) {
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
			log.Printf("[client] %s #%d connect failed: %v, retrying in 3s...", groups[gi].Name, ei+1, err)
			time.Sleep(3 * time.Second)
			continue
		}

		log.Printf("[client] %s #%d connected", groups[gi].Name, ei+1)

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Printf("[client] %s #%d disconnected: %v, reconnecting...", groups[gi].Name, ei+1, err)
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

			now := float64(time.Now().UnixMilli())
			latencyMicros := int64((now - msg.Timestamp) * 1000)
			if latencyMicros > 0 {
				recordLatency(stats[gi].endpoints[ei], latencyMicros, len(message))
			}
		}

		conn.Close()
		time.Sleep(2 * time.Second)
	}
}

func connectGRPC(ctx context.Context, gi, ei int, endpoint string, stats []*GroupStats, measuring *atomic.Bool, wg *sync.WaitGroup) {
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
			log.Printf("[client] %s #%d dial failed: %v, retrying in 3s...", groups[gi].Name, ei+1, err)
			time.Sleep(3 * time.Second)
			continue
		}

		client := pb.NewBenchmarkServiceClient(conn)
		stream, err := client.StreamMessages(ctx, &pb.StreamRequest{ClientId: fmt.Sprintf("bench-%d-%d", gi, ei)})
		if err != nil {
			log.Printf("[client] %s #%d stream failed: %v, retrying in 3s...", groups[gi].Name, ei+1, err)
			conn.Close()
			time.Sleep(3 * time.Second)
			continue
		}

		log.Printf("[client] %s #%d connected", groups[gi].Name, ei+1)

		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				conn.Close()
				break
			}
			if err != nil {
				log.Printf("[client] %s #%d recv error: %v, reconnecting...", groups[gi].Name, ei+1, err)
				break
			}

			if !measuring.Load() {
				continue
			}

			now := float64(time.Now().UnixMilli())
			ts := float64(resp.GetTimestamp())
			latencyMicros := int64((now - ts) * 1000)
			if latencyMicros > 0 {
				payload := resp.GetPayload()
				recordLatency(stats[gi].endpoints[ei], latencyMicros, len(payload))
			}
		}

		conn.Close()
		time.Sleep(2 * time.Second)
	}
}

func mergeEndpointHistograms(gs *GroupStats) *hdrhistogram.Histogram {
	merged := hdrhistogram.New(1, 60000000, 3)
	for _, es := range gs.endpoints {
		merged.Merge(es.hist)
	}
	return merged
}

func main() {
	warmup := flag.Int("warmup", 30, "warmup seconds")
	duration := flag.Int("duration", 120, "measurement seconds")
	flag.Parse()

	log.Printf("[client] Warmup: %ds, Measurement: %ds", *warmup, *duration)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var measuring atomic.Bool
	stats := make([]*GroupStats, len(groups))
	for i := range stats {
		stats[i] = newGroupStats()
	}

	var wg sync.WaitGroup

	for gi := range groups {
		for ei := range groups[gi].Endpoints {
			wg.Add(1)
			if groups[gi].Type == "ws" {
				go connectWS(ctx, gi, ei, groups[gi].Endpoints[ei], stats, &measuring, &wg)
			} else {
				go connectGRPC(ctx, gi, ei, groups[gi].Endpoints[ei], stats, &measuring, &wg)
			}
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("[client] %d endpoints connecting...", len(groups)*3)
	time.Sleep(3 * time.Second)

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

	fmt.Println("\n=== THROUGHPUT RESULTS ===\n")
	fmt.Printf("%-16s %10s %10s %12s\n", "Group", "Msgs", "MB/s", "msg/s")
	fmt.Println(strings.Repeat("-", 50))

	throughputs := make([]float64, len(groups))
	for gi := range groups {
		var totalMsgs, totalBytes int64
		for ei := 0; ei < 3; ei++ {
			totalMsgs += stats[gi].endpoints[ei].count.Load()
			totalBytes += stats[gi].endpoints[ei].bytes.Load()
		}
		mbps := float64(totalBytes) / 1024 / 1024 / measureDuration
		msgPerSec := float64(totalMsgs) / measureDuration
		throughputs[gi] = mbps
		fmt.Printf("%-16s %10d %10.2f %12.0f\n", groups[gi].Name, totalMsgs, mbps, msgPerSec)
	}

	wsThroughput := throughputs[0]
	if wsThroughput > 0 {
		fmt.Println(strings.Repeat("-", 50))
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
		groupMerged[gi] = mergeEndpointHistograms(stats[gi])
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

	fmt.Println("\nPer-endpoint breakdown:")
	for gi := range groups {
		for ei := 0; ei < 3; ei++ {
			es := stats[gi].endpoints[ei]
			count := es.count.Load()
			bytes := es.bytes.Load()
			mbps := float64(bytes) / 1024 / 1024 / measureDuration
			p50 := float64(es.hist.ValueAtPercentile(50)) / 1000.0
			p99 := float64(es.hist.ValueAtPercentile(99)) / 1000.0
			fmt.Printf("  %s #%d: %d msgs, %.2f MB/s, p50=%.3f p99=%.3f\n",
				groups[gi].Name, ei+1, count, mbps, p50, p99)
		}
	}

	var totalMsgs, totalBytes int64
	for gi := range groups {
		for ei := 0; ei < 3; ei++ {
			totalMsgs += stats[gi].endpoints[ei].count.Load()
			totalBytes += stats[gi].endpoints[ei].bytes.Load()
		}
	}
	fmt.Printf("\nAggregate: %d msgs, %.2f MB\n", totalMsgs, float64(totalBytes)/1024/1024)
	fmt.Printf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}

func pad(s string, w int) string {
	for len(s) < w {
		s = " " + s
	}
	return s
}