package coordinator

import (
	"sync"

	"benchmark-client/internal/stats"

	pb "benchmark-client/proto/control"

	"github.com/HdrHistogram/hdrhistogram-go"
)

type WorkerResult struct {
	WorkerID string
	Report   *pb.FinalReport
}

type Aggregator struct {
	mu      sync.Mutex
	results map[string]*WorkerResult
}

func NewAggregator() *Aggregator {
	return &Aggregator{
		results: make(map[string]*WorkerResult),
	}
}

func (a *Aggregator) AddWorkerResult(workerID string, report *pb.FinalReport) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.results[workerID] = &WorkerResult{
		WorkerID: workerID,
		Report:   report,
	}
}

func (a *Aggregator) ResultCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.results)
}

func (a *Aggregator) MergeToGroupStats() map[string]*stats.GroupStats {
	a.mu.Lock()
	defer a.mu.Unlock()

	type groupAccum struct {
		totalMsgs  int64
		totalBytes int64
		totalRaw   int64
		totalRawB  int64
		hist       *hdrhistogram.Histogram
	}

	accums := make(map[string]*groupAccum)

	for _, wr := range a.results {
		for _, gr := range wr.Report.Groups {
			acc, ok := accums[gr.Name]
			if !ok {
				acc = &groupAccum{
					hist: hdrhistogram.New(1, 3600000000, 3),
				}
				accums[gr.Name] = acc
			}

			for _, cr := range gr.Connections {
				acc.totalMsgs += cr.MsgCount
				acc.totalBytes += cr.ByteCount
				acc.totalRaw += cr.RawCount
				acc.totalRawB += cr.RawBytes
			}

			if len(gr.LatencyHistogram) > 0 {
				if h, err := stats.DecodeHistogram(gr.LatencyHistogram); err == nil {
					acc.hist.Merge(h)
				}
			}
		}
	}

	result := make(map[string]*stats.GroupStats)
	for name, acc := range accums {
		gs := stats.NewGroupStats(1)
		gs.Conns[0].Count.Store(acc.totalMsgs)
		gs.Conns[0].Bytes.Store(acc.totalBytes)
		gs.Conns[0].RawCount.Store(acc.totalRaw)
		gs.Conns[0].RawBytes.Store(acc.totalRawB)
		gs.Conns[0].FirstMsg.Store(acc.totalMsgs > 0)
		gs.Conns[0].ConnActive.Store(true)
		gs.Conns[0].Hist.Merge(acc.hist)
		result[name] = gs
	}

	return result
}

func (a *Aggregator) MergeToGroupHistograms() map[string]*hdrhistogram.Histogram {
	a.mu.Lock()
	defer a.mu.Unlock()

	merged := make(map[string]*hdrhistogram.Histogram)

	for _, wr := range a.results {
		for _, gr := range wr.Report.Groups {
			h, exists := merged[gr.Name]
			if !exists {
				h = hdrhistogram.New(1, 3600000000, 3)
				merged[gr.Name] = h
			}
			if len(gr.LatencyHistogram) > 0 {
				if gh, err := stats.DecodeHistogram(gr.LatencyHistogram); err == nil {
					h.Merge(gh)
				}
			}
		}
	}

	return merged
}
