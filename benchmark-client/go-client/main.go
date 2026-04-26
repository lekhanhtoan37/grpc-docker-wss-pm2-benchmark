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
	{Name: "Go WS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8092", "ws://127.0.0.1:8093", "ws://127.0.0.1:8094"}},
	{Name: "uWS bridge", Type: "ws", Endpoints: []string{"ws://127.0.0.1:50061", "ws://127.0.0.1:50061", "ws://127.0.0.1:50061"}},
	{Name: "Go WS bridge", Type: "ws", Endpoints: []string{"ws://127.0.0.1:50071", "ws://127.0.0.1:50071", "ws://127.0.0.1:50071"}},
	{Name: "uWS host", Type: "ws", Endpoints: []string{"ws://127.0.0.1:60061", "ws://127.0.0.1:60062", "ws://127.0.0.1:60063"}},
	{Name: "Go WS host", Type: "ws", Endpoints: []string{"ws://127.0.0.1:60071", "ws://127.0.0.1:60072", "ws://127.0.0.1:60073"}},
	{Name: "gRPC bridge", Type: "grpc", Endpoints: []string{"localhost:50051", "localhost:50051", "localhost:50051"}},
	{Name: "gRPC host", Type: "grpc", Endpoints: []string{"localhost:60051", "localhost:60052", "localhost:60053"}},
}

func newConnStats() *ConnStats {
	return &ConnStats{
		hist:          hdrhistogram.New(1, 3600000000, 3),
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

var timestampKey = []byte(`"timestamp":`)

var latencySlicePool = sync.Pool{
	New: func() any {
		// BATCH_MAX=20, để dư chút cho an toàn
		return make([]int64, 0, 64)
	},
}

type wsFrameEvent struct {
	msgCount int64
	byteSize int64
	samples  []int64
}

func getLatencySlice() []int64 {
	return latencySlicePool.Get().([]int64)[:0]
}

func putLatencySlice(v []int64) {
	if v == nil {
		return
	}
	if cap(v) > 1024 {
		v = make([]int64, 0, 64)
	} else {
		v = v[:0]
	}
	latencySlicePool.Put(v)
}

func readFrameReusable(r io.Reader, dst []byte) ([]byte, error) {
	dst = dst[:0]
	var scratch [32 * 1024]byte

	for {
		n, err := r.Read(scratch[:])
		if n > 0 {
			dst = append(dst, scratch[:n]...)
		}
		if err == io.EOF {
			return dst, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func extractTimestampInt64(msg []byte) int64 {
	idx := bytes.Index(msg, timestampKey)
	if idx < 0 {
		return 0
	}

	i := idx + len(timestampKey)
	for i < len(msg) {
		c := msg[i]
		if c == ' ' || c == '\t' {
			i++
			continue
		}
		break
	}

	var n int64
	found := false
	for i < len(msg) {
		c := msg[i]
		if c < '0' || c > '9' {
			break
		}
		found = true
		n = n*10 + int64(c-'0')
		i++
	}

	if !found {
		return 0
	}
	return n
}

func wsStatsWorker(cs *ConnStats, in <-chan wsFrameEvent, wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	var localMsgs int64
	var localBytes int64

	flush := func() {
		if localMsgs != 0 {
			cs.count.Add(localMsgs)
			cs.rawCount.Add(localMsgs)
			localMsgs = 0
		}
		if localBytes != 0 {
			cs.bytes.Add(localBytes)
			cs.rawBytes.Add(localBytes)
			localBytes = 0
		}
	}

	for {
		select {
		case ev, ok := <-in:
			if !ok {
				flush()
				return
			}

			localMsgs += ev.msgCount
			localBytes += ev.byteSize

			if len(ev.samples) > 0 {
				for _, lat := range ev.samples {
					_ = cs.hist.RecordValue(lat)
				}
				putLatencySlice(ev.samples)
			}

			if localMsgs >= 32000 || localBytes >= 8*1024*1024 {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}

func connectWS(ctx context.Context, gi, ci int, endpoint string, stats []*GroupStats, measuring *atomic.Bool, wg *sync.WaitGroup) {
	defer wg.Done()

	cs := stats[gi].conns[ci]

	events := make(chan wsFrameEvent, 2048)

	var statsWG sync.WaitGroup
	statsWG.Add(1)
	go wsStatsWorker(cs, events, &statsWG)

	defer func() {
		close(events)
		statsWG.Wait()
	}()

	frameBuf := make([]byte, 0, 64*1024)

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

		connectMicros := time.Since(connectStart).Microseconds()
		firstConnect := !cs.firstMsg.Load()
		if firstConnect {
			log.Printf("[client] %s conn#%d connected to %s (%.2fms)", groups[gi].Name, ci+1, endpoint, float64(connectMicros)/1000.0)
		} else {
			cs.reconnectCount.Add(1)
			_ = cs.reconnectHist.RecordValue(connectMicros)
		}
		cs.connActive.Store(true)

		for {
			_, reader, err := conn.Reader(ctx)
			if err != nil {
				if cs.disconnectCount.Load() < 3 {
					log.Printf("[client] %s conn#%d read error: %v", groups[gi].Name, ci+1, err)
				}
				cs.disconnectCount.Add(1)
				cs.connActive.Store(false)
				_ = conn.Close(websocket.StatusInternalError, "read error")
				break
			}

			frameBuf, err = readFrameReusable(reader, frameBuf)
			if err != nil {
				if cs.disconnectCount.Load() < 3 {
					log.Printf("[client] %s conn#%d read frame error: %v", groups[gi].Name, ci+1, err)
				}
				cs.disconnectCount.Add(1)
				cs.connActive.Store(false)
				_ = conn.Close(websocket.StatusInternalError, "read frame error")
				break
			}

			if len(frameBuf) == 0 {
				continue
			}

			measuringNow := measuring.Load()
			var samples []int64
			var nowMicros int64

			if measuringNow {
				samples = getLatencySlice()
				nowMicros = time.Now().UnixMicro()
			}

			msgCount := 0
			start := 0

			for start < len(frameBuf) {
				end := start
				for end < len(frameBuf) && frameBuf[end] != '\n' {
					end++
				}

				if end > start {
					line := frameBuf[start:end]
					msgCount++

					if !cs.firstMsg.Load() {
						cs.firstMsg.Store(true)
					}

					if measuringNow {
						ts := extractTimestampInt64(line)
						if ts > 0 {
							lat := nowMicros - ts*1000
							if lat < 1 {
								lat = 1
							}
							samples = append(samples, lat)
						}
					}
				}

				start = end + 1
			}

			// Warmup: chỉ dùng để ổn định kết nối, không cộng throughput/latency
			if !measuringNow {
				continue
			}

			if msgCount == 0 {
				putLatencySlice(samples)
				continue
			}

			events <- wsFrameEvent{
				msgCount: int64(msgCount),
				byteSize: int64(len(frameBuf)),
				samples:  samples,
			}
		}

		conn.CloseNow()

		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
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
				if ts == 0 {
					continue
				}
				tsMicros := int64(ts * 1000)
				latencyMicros := nowMicros - tsMicros
				if latencyMicros > 0 && latencyMicros < 3600000000 {
					cs.hist.RecordValue(latencyMicros)
				}
			}
		}

		conn.Close()
		time.Sleep(500 * time.Millisecond)
	}
}

func mergeGroupHistogram(gs *GroupStats) *hdrhistogram.Histogram {
	merged := hdrhistogram.New(1, 3600000000, 3)
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

	log.SetOutput(io.Discard)
	time.Sleep(100 * time.Millisecond)
	log.SetOutput(os.Stderr)

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
