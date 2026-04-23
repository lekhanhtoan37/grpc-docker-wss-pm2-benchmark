package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	pb "benchmark-client/proto"
	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/coder/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Group struct {
	Name      string
	Type      string
	Endpoints []string
}

type ConnStats struct {
	hist            *hdrhistogram.Histogram
	reconnectHist   *hdrhistogram.Histogram
	count           atomic.Int64
	bytes           atomic.Int64
	rawCount        atomic.Int64
	rawBytes        atomic.Int64
	firstMsg        atomic.Bool
	connActive      atomic.Bool
	disconnectCount atomic.Int64
	reconnectCount  atomic.Int64
}

type GroupStats struct {
	conns []*ConnStats
}

var groups = []Group{
	{Name: "WS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8090", "ws://127.0.0.1:8090", "ws://127.0.0.1:8090"}},
	{Name: "uWS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8091", "ws://127.0.0.1:8091", "ws://127.0.0.1:8091"}},
	{Name: "uWS bridge", Type: "ws", Endpoints: []string{"ws://127.0.0.1:50061", "ws://127.0.0.1:50061", "ws://127.0.0.1:50061"}},
	{Name: "uWS host", Type: "ws", Endpoints: []string{"ws://127.0.0.1:60061", "ws://127.0.0.1:60062", "ws://127.0.0.1:60063"}},
	{Name: "gRPC bridge", Type: "grpc", Endpoints: []string{"localhost:50051", "localhost:50051", "localhost:50051"}},
	{Name: "gRPC host", Type: "grpc", Endpoints: []string{"localhost:60051", "localhost:60052", "localhost:60053"}},
}

func newConnStats() *ConnStats {
	return &ConnStats{
		hist:          hdrhistogram.New(1, 60000000, 3),
		reconnectHist: hdrhistogram.New(1, 30000000, 3),
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

func connectWS(ctx context.Context, gi, ci int, endpoint string, stats []*GroupStats, measuring *atomic.Bool, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		opts := &websocket.DialOptions{
			CompressionMode:      websocket.CompressionDisabled,
			CompressionThreshold: 0,
		}
		connectStart := time.Now()
		conn, _, err := websocket.Dial(ctx, endpoint, opts)
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}
		conn.SetReadLimit(128 * 1024 * 1024)
		connectMs := time.Since(connectStart).Milliseconds()

		firstConnect := !stats[gi].conns[ci].firstMsg.Load()
		if firstConnect {
			log.Printf("[client] %s conn#%d connected to %s (%dms)", groups[gi].Name, ci+1, endpoint, connectMs)
		}
		stats[gi].conns[ci].connActive.Store(true)
		if !firstConnect {
			stats[gi].conns[ci].reconnectCount.Add(1)
			stats[gi].conns[ci].reconnectHist.RecordValue(connectMs * 1000)
		}

		var localCount, localBytes int64
		lastFlush := time.Now()

		for {
			_, reader, err := conn.Reader(ctx)
			if err != nil {
				stats[gi].conns[ci].disconnectCount.Add(1)
				stats[gi].conns[ci].connActive.Store(false)
				stats[gi].conns[ci].count.Add(localCount)
				stats[gi].conns[ci].bytes.Add(localBytes)
				stats[gi].conns[ci].rawCount.Add(localCount)
				stats[gi].conns[ci].rawBytes.Add(localBytes)
				conn.Close(websocket.StatusInternalError, "read error")
				break
			}

			msg, err := io.ReadAll(reader)
			if err != nil {
				stats[gi].conns[ci].disconnectCount.Add(1)
				stats[gi].conns[ci].connActive.Store(false)
				stats[gi].conns[ci].count.Add(localCount)
				stats[gi].conns[ci].bytes.Add(localBytes)
				stats[gi].conns[ci].rawCount.Add(localCount)
				stats[gi].conns[ci].rawBytes.Add(localBytes)
				conn.Close(websocket.StatusInternalError, "read error")
				break
			}

			localBytes += int64(len(msg))

			remaining := msg
			for len(remaining) > 0 {
				idx := bytes.IndexByte(remaining, '\n')
				var line []byte
				if idx >= 0 {
					line = remaining[:idx]
					remaining = remaining[idx+1:]
				} else {
					line = remaining
					remaining = nil
				}
				if len(line) == 0 {
					continue
				}
				localCount++

				if measuring.Load() {
					ts := extractTimestamp(line)
					if ts > 0 {
						latencyMicros := time.Now().UnixMicro() - int64(ts*1000)
						if latencyMicros < 1 {
							latencyMicros = 1
						}
						stats[gi].conns[ci].hist.RecordValue(latencyMicros)
					}
				}
			}

			if localCount%2000 == 0 {
				now := time.Now()
				if now.Sub(lastFlush) >= 100*time.Millisecond {
					stats[gi].conns[ci].count.Add(localCount)
					stats[gi].conns[ci].bytes.Add(localBytes)
					stats[gi].conns[ci].rawCount.Add(localCount)
					stats[gi].conns[ci].rawBytes.Add(localBytes)
					localCount = 0
					localBytes = 0
					lastFlush = now
				}
			}
		}

		conn.CloseNow()
		time.Sleep(500 * time.Millisecond)
	}
}

func extractTimestamp(msg []byte) float64 {
	idx := bytes.Index(msg, []byte(`"timestamp":`))
	if idx < 0 {
		return 0
	}
	start := idx + len(`"timestamp":`)
	if start >= len(msg) {
		return 0
	}
	end := start
	for end < len(msg) && msg[end] != ',' && msg[end] != '}' {
		end++
	}
	val, err := strconv.ParseFloat(string(msg[start:end]), 64)
	if err != nil {
		return 0
	}
	return val
}

func connectGRPC(ctx context.Context, gi, ci int, endpoint string, stats []*GroupStats, measuring *atomic.Bool, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		connectStart := time.Now()
		conn, err := grpc.NewClient(endpoint,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithInitialWindowSize(671088640),
			grpc.WithInitialConnWindowSize(1342177280),
			grpc.WithDefaultCallOptions(
				grpc.MaxCallRecvMsgSize(104857600),
				grpc.MaxCallSendMsgSize(104857600),
			),
		)
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}

		client := pb.NewBenchmarkServiceClient(conn)
		stream, err := client.StreamMessages(ctx, &pb.StreamRequest{ClientId: fmt.Sprintf("bench-%d-%d", gi, ci)})
		if err != nil {
			conn.Close()
			time.Sleep(3 * time.Second)
			continue
		}

		firstConnect := !stats[gi].conns[ci].firstMsg.Load()
		if firstConnect {
			log.Printf("[client] %s conn#%d connected to %s (%dms)", groups[gi].Name, ci+1, endpoint, time.Since(connectStart).Milliseconds())
		}
		stats[gi].conns[ci].connActive.Store(true)
		if !firstConnect {
			stats[gi].conns[ci].reconnectCount.Add(1)
			stats[gi].conns[ci].reconnectHist.RecordValue(time.Since(connectStart).Milliseconds() * 1000)
		}

		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				stats[gi].conns[ci].disconnectCount.Add(1)
				stats[gi].conns[ci].connActive.Store(false)
				conn.Close()
				break
			}
			if err != nil {
				stats[gi].conns[ci].disconnectCount.Add(1)
				stats[gi].conns[ci].connActive.Store(false)
				break
			}

			cs := stats[gi].conns[ci]
			entries := resp.GetMessages()
			batchLen := int64(len(entries))

			if !cs.firstMsg.Load() && batchLen > 0 {
				cs.firstMsg.Store(true)
			}

			if !measuring.Load() {
				continue
			}

			var payloadBytes int64
			for _, e := range entries {
				payloadBytes += int64(len(e.GetPayload()))
			}

			cs.rawCount.Add(batchLen)
			cs.rawBytes.Add(payloadBytes)
			cs.count.Add(batchLen)
			cs.bytes.Add(payloadBytes)

			nowMicros := time.Now().UnixMicro()
			for _, e := range entries {
				ts := e.GetTimestamp()
				tsMicros := int64(ts * 1000)
				latencyMicros := nowMicros - tsMicros
				if latencyMicros < 1 {
					latencyMicros = 1
				}
				cs.hist.RecordValue(latencyMicros)
			}
		}

		conn.Close()
		time.Sleep(500 * time.Millisecond)
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

func printLiveStats(groups []Group, stats []*GroupStats, measuring *atomic.Bool) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if !measuring.Load() {
			continue
		}
		for gi := range groups {
			totalRaw, totalMsgs := int64(0), int64(0)
			active := 0
			for _, cs := range stats[gi].conns {
				totalRaw += cs.rawCount.Load()
				totalMsgs += cs.count.Load()
				if cs.connActive.Load() {
					active++
				}
			}
			log.Printf("[live] %s: active=%d raw=%d processed=%d", groups[gi].Name, active, totalRaw, totalMsgs)
		}
	}
}

func main() {
	warmup := flag.Int("warmup", 30, "warmup seconds")
	duration := flag.Int("duration", 120, "measurement seconds")
	conns := flag.Int("conns", 1, "connections per group")
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

	activeConns := 0
	for gi := range groups {
		for _, cs := range stats[gi].conns {
			if cs.connActive.Load() {
				activeConns++
			}
		}
	}
	log.Printf("[client] %d/%d connections active after 5s", activeConns, totalConns)

	if activeConns == 0 {
		log.Printf("[client] WARNING: no active connections! Waiting 10s more...")
		time.Sleep(10 * time.Second)
		activeConns = 0
		for gi := range groups {
			for _, cs := range stats[gi].conns {
				if cs.connActive.Load() {
					activeConns++
				}
			}
		}
		log.Printf("[client] %d/%d connections active after retry", activeConns, totalConns)
	}

	go printLiveStats(groups, stats, &measuring)

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
	wg.Wait()

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

	fmt.Println("\n=== RAW RECV STATS ===\n")
	fmt.Printf("%-16s %10s %10s %12s %10s\n", "Group", "Raw Msgs", "Raw MB/s", "Raw msg/s", "Drop %")
	fmt.Println(strings.Repeat("-", 60))
	for gi := range groups {
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
	fmt.Printf("%-16s %12s\n", "Group", "Latency samples")
	fmt.Println(strings.Repeat("-", 30))
	for gi := range groups {
		groupMerged[gi] = mergeGroupHistogram(stats[gi])
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

	fmt.Println("\nDelta vs WS (host/PM2):")
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

	fmt.Println("\nPer-connection breakdown:")
	for gi := range groups {
		for ci := range stats[gi].conns {
			cs := stats[gi].conns[ci]
			count := cs.count.Load()
			bts := cs.bytes.Load()
			raw := cs.rawCount.Load()
			active := cs.connActive.Load()
			epIdx := ci % len(groups[gi].Endpoints)
			mbps := float64(bts) / 1024 / 1024 / measureDuration
			p50 := float64(cs.hist.ValueAtPercentile(50)) / 1000.0
			p99 := float64(cs.hist.ValueAtPercentile(99)) / 1000.0
			status := "ACTIVE"
			if !active {
				status = "DEAD"
			}
			first := "yes"
			if !cs.firstMsg.Load() {
				first = "NO"
			}
			extra := ""
			if groups[gi].Type == "grpc" && raw > 0 {
				dropPct := float64(raw-count) / float64(raw) * 100
				extra = fmt.Sprintf(", raw=%d, drop=%.1f%%", raw, dropPct)
			}
			disc := cs.disconnectCount.Load()
			reconn := cs.reconnectCount.Load()
			extra += fmt.Sprintf(", disc=%d, reconn=%d", disc, reconn)
			if reconn > 0 {
				extra += fmt.Sprintf(", reconn_p50=%.1fms, reconn_p99=%.1fms",
					float64(cs.reconnectHist.ValueAtPercentile(50))/1000.0,
					float64(cs.reconnectHist.ValueAtPercentile(99))/1000.0)
			}
			fmt.Printf("  %s conn#%d (ep#%d %s) [%s][first=%s]: %d msgs, %.2f MB/s, p50=%.3f p99=%.3f%s\n",
				groups[gi].Name, ci+1, epIdx+1, groups[gi].Endpoints[epIdx], status, first,
				count, mbps, p50, p99, extra)
		}
	}

	fmt.Println("\n=== CONNECTION STABILITY ===\n")
	fmt.Printf("%-16s %10s %10s %12s %12s %12s\n", "Group", "Disconnects", "Reconnects", "Reconn p50", "Reconn p99", "Reconn max")
	fmt.Println(strings.Repeat("-", 76))
	for gi := range groups {
		totalDisc, totalReconn := int64(0), int64(0)
		mergedReconn := hdrhistogram.New(1, 30000000, 3)
		for _, cs := range stats[gi].conns {
			totalDisc += cs.disconnectCount.Load()
			totalReconn += cs.reconnectCount.Load()
			mergedReconn.Merge(cs.reconnectHist)
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
