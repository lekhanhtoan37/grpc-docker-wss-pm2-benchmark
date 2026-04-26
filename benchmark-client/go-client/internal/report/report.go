package report

import (
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"benchmark-client/internal/stats"

	"github.com/HdrHistogram/hdrhistogram-go"
)

func PrintLiveStats(groups []stats.Group, allStats []*stats.GroupStats, measuring *atomic.Bool) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if !measuring.Load() {
			continue
		}
		for gi := range groups {
			totalRaw, totalMsgs := int64(0), int64(0)
			active := 0
			for _, cs := range allStats[gi].Conns {
				totalRaw += cs.RawCount.Load()
				totalMsgs += cs.Count.Load()
				if cs.ConnActive.Load() {
					active++
				}
			}
			log.Printf("[live] %s: active=%d raw=%d processed=%d", groups[gi].Name, active, totalRaw, totalMsgs)
		}
	}
}

func PrintReport(groups []stats.Group, allStats []*stats.GroupStats, measureDuration float64, conns int) {
	fmt.Print("\n=== THROUGHPUT RESULTS (processed) ===\n\n")
	fmt.Printf("%-16s %8s %12s %10s %12s\n", "Group", "Conns", "Msgs", "MB/s", "msg/s")
	fmt.Println(strings.Repeat("-", 60))

	throughputs := make([]float64, len(groups))
	for gi := range groups {
		totalMsgs, totalBytes, _, _ := stats.AggregateGroup(allStats[gi])
		mbps := float64(totalBytes) / 1024 / 1024 / measureDuration
		msgPerSec := float64(totalMsgs) / measureDuration
		throughputs[gi] = mbps
		fmt.Printf("%-16s %8d %12d %10.2f %12.0f\n", groups[gi].Name, conns, totalMsgs, mbps, msgPerSec)
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

	fmt.Print("\nPer-connection breakdown:\n")
	for gi := range groups {
		for ci := range allStats[gi].Conns {
			cs := allStats[gi].Conns[ci]
			count := cs.Count.Load()
			bts := cs.Bytes.Load()
			raw := cs.RawCount.Load()
			active := cs.ConnActive.Load()
			epIdx := ci % len(groups[gi].Endpoints)
			mbps := float64(bts) / 1024 / 1024 / measureDuration
			p50 := float64(cs.Hist.ValueAtPercentile(50)) / 1000.0
			p99 := float64(cs.Hist.ValueAtPercentile(99)) / 1000.0
			status := "ACTIVE"
			if !active {
				status = "DEAD"
			}
			first := "yes"
			if !cs.FirstMsg.Load() {
				first = "NO"
			}
			extra := ""
			if groups[gi].Type == "grpc" && raw > 0 {
				dropPct := float64(raw-count) / float64(raw) * 100
				extra = fmt.Sprintf(", raw=%d, drop=%.1f%%", raw, dropPct)
			}
			disc := cs.DisconnectCount.Load()
			reconn := cs.ReconnectCount.Load()
			extra += fmt.Sprintf(", disc=%d, reconn=%d", disc, reconn)
			if reconn > 0 {
				extra += fmt.Sprintf(", reconn_p50=%.1fms, reconn_p99=%.1fms",
					float64(cs.ReconnectHist.ValueAtPercentile(50))/1000.0,
					float64(cs.ReconnectHist.ValueAtPercentile(99))/1000.0)
			}
			fmt.Printf("  %s conn#%d (ep#%d %s) [%s][first=%s]: %d msgs, %.2f MB/s, p50=%.3f p99=%.3f%s\n",
				groups[gi].Name, ci+1, epIdx+1, groups[gi].Endpoints[epIdx], status, first,
				count, mbps, p50, p99, extra)
		}
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

	var grandMsgs, grandBytes int64
	for gi := range groups {
		msgs, bts, _, _ := stats.AggregateGroup(allStats[gi])
		grandMsgs += msgs
		grandBytes += bts
	}
	fmt.Printf("\nAggregate: %d msgs, %.2f MB across %d groups x %d conns\n",
		grandMsgs, float64(grandBytes)/1024/1024, len(groups), conns)
	fmt.Printf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}
