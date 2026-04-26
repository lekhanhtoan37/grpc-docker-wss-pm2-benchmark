package coordinator

import (
	"testing"

	"benchmark-client/internal/stats"

	pb "benchmark-client/proto/control"

	"github.com/HdrHistogram/hdrhistogram-go"
)

func makeFakeReport(workerID, groupName string, msgCount, byteCount int64, latencies []int64) *pb.FinalReport {
	cs := stats.NewConnStats()
	cs.Count.Store(msgCount)
	cs.Bytes.Store(byteCount)
	cs.RawCount.Store(msgCount)
	cs.RawBytes.Store(byteCount)
	cs.FirstMsg.Store(true)
	cs.ConnActive.Store(true)
	for _, lat := range latencies {
		cs.Hist.RecordValue(lat)
	}

	cr, _ := stats.ConnStatsToProto(cs, 0, "ws://test")

	mergedHist := stats.MergeGroupHistogram(&stats.GroupStats{Conns: []*stats.ConnStats{cs}})
	mergedBytes, _ := stats.EncodeHistogram(mergedHist)

	return &pb.FinalReport{
		WorkerId:          workerID,
		MeasureDurationMs: 60000,
		Groups: []*pb.GroupResult{
			{
				Name:             groupName,
				Connections:      []*pb.ConnResult{cr},
				LatencyHistogram: mergedBytes,
			},
		},
	}
}

func TestAggregator_SingleWorker(t *testing.T) {
	agg := NewAggregator()

	agg.AddWorkerResult("w1", makeFakeReport("w1", "group-a", 1000, 50000, []int64{100, 200, 300}))

	if agg.ResultCount() != 1 {
		t.Fatalf("expected 1 result, got %d", agg.ResultCount())
	}

	gs := agg.MergeToGroupStats()
	ga, ok := gs["group-a"]
	if !ok {
		t.Fatal("group-a not found")
	}
	if len(ga.Conns) != 1 {
		t.Fatalf("expected 1 conn, got %d", len(ga.Conns))
	}
	if ga.Conns[0].Count.Load() != 1000 {
		t.Errorf("count: got %d, want 1000", ga.Conns[0].Count.Load())
	}
	if ga.Conns[0].Bytes.Load() != 50000 {
		t.Errorf("bytes: got %d, want 50000", ga.Conns[0].Bytes.Load())
	}
}

func TestAggregator_TwoWorkers_SameGroup(t *testing.T) {
	agg := NewAggregator()

	agg.AddWorkerResult("w1", makeFakeReport("w1", "group-a", 500, 25000, []int64{100, 150}))
	agg.AddWorkerResult("w2", makeFakeReport("w2", "group-a", 700, 35000, []int64{200, 250}))

	gs := agg.MergeToGroupStats()
	ga := gs["group-a"]

	if len(ga.Conns) != 1 {
		t.Fatalf("expected 1 conn slot, got %d", len(ga.Conns))
	}

	totalMsgs, totalBytes, _, _ := stats.AggregateGroup(ga)
	if totalMsgs != 1200 {
		t.Errorf("totalMsgs: got %d, want 1200 (500+700)", totalMsgs)
	}
	if totalBytes != 60000 {
		t.Errorf("totalBytes: got %d, want 60000 (25000+35000)", totalBytes)
	}
}

func TestAggregator_HistogramMerge(t *testing.T) {
	agg := NewAggregator()

	agg.AddWorkerResult("w1", makeFakeReport("w1", "g1", 100, 5000, []int64{100, 200}))
	agg.AddWorkerResult("w2", makeFakeReport("w2", "g1", 100, 5000, []int64{300, 400}))

	hists := agg.MergeToGroupHistograms()
	h, ok := hists["g1"]
	if !ok {
		t.Fatal("g1 not in histogram map")
	}

	if h.TotalCount() != 4 {
		t.Errorf("total histogram samples: got %d, want 4", h.TotalCount())
	}

	p50 := h.ValueAtPercentile(50)
	if p50 < 100 || p50 > 400 {
		t.Errorf("p50 out of range: %d", p50)
	}

	p99 := h.ValueAtPercentile(99)
	if p99 < 300 || p99 > 400 {
		t.Errorf("p99 out of range: %d", p99)
	}
}

func TestAggregator_DifferentGroups(t *testing.T) {
	agg := NewAggregator()

	agg.AddWorkerResult("w1", makeFakeReport("w1", "group-a", 100, 5000, []int64{100}))
	agg.AddWorkerResult("w2", makeFakeReport("w2", "group-b", 200, 10000, []int64{200}))

	gs := agg.MergeToGroupStats()

	if len(gs) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(gs))
	}

	ga := gs["group-a"]
	gb := gs["group-b"]

	msgsA, _, _, _ := stats.AggregateGroup(ga)
	msgsB, _, _, _ := stats.AggregateGroup(gb)

	if msgsA != 100 {
		t.Errorf("group-a msgs: got %d, want 100", msgsA)
	}
	if msgsB != 200 {
		t.Errorf("group-b msgs: got %d, want 200", msgsB)
	}
}

func TestAggregator_Empty(t *testing.T) {
	agg := NewAggregator()
	if agg.ResultCount() != 0 {
		t.Errorf("empty aggregator should have 0 results")
	}
	gs := agg.MergeToGroupStats()
	if len(gs) != 0 {
		t.Errorf("empty aggregator should produce empty map")
	}
}

func TestAggregator_LosslessHistogramMerge(t *testing.T) {
	agg := NewAggregator()

	h1 := hdrhistogram.New(1, 3600000000, 3)
	h1.RecordValue(100)
	h1.RecordValue(200)
	h1.RecordValue(300)

	h2 := hdrhistogram.New(1, 3600000000, 3)
	h2.RecordValue(150)
	h2.RecordValue(250)
	h2.RecordValue(350)

	enc1, _ := stats.EncodeHistogram(h1)
	enc2, _ := stats.EncodeHistogram(h2)

	agg.AddWorkerResult("w1", &pb.FinalReport{
		WorkerId: "w1",
		Groups: []*pb.GroupResult{{
			Name:             "g",
			LatencyHistogram: enc1,
		}},
	})
	agg.AddWorkerResult("w2", &pb.FinalReport{
		WorkerId: "w2",
		Groups: []*pb.GroupResult{{
			Name:             "g",
			LatencyHistogram: enc2,
		}},
	})

	merged := agg.MergeToGroupHistograms()["g"]

	if merged.TotalCount() != 6 {
		t.Errorf("total count: got %d, want 6", merged.TotalCount())
	}

	p50 := merged.ValueAtPercentile(50)
	if p50 < 100 || p50 > 350 {
		t.Errorf("p50 unreasonable: %d", p50)
	}
}
