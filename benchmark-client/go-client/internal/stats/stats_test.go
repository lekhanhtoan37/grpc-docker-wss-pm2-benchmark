package stats

import (
	"testing"

	"github.com/HdrHistogram/hdrhistogram-go"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	h := hdrhistogram.New(1, 3600000000, 3)
	for i := int64(100); i < 200; i++ {
		if err := h.RecordValue(i); err != nil {
			t.Fatal(err)
		}
	}

	encoded, err := EncodeHistogram(h)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	if len(encoded) == 0 {
		t.Fatal("encoded bytes should not be empty")
	}

	decoded, err := DecodeHistogram(encoded)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if decoded.TotalCount() != h.TotalCount() {
		t.Errorf("TotalCount mismatch: got %d, want %d", decoded.TotalCount(), h.TotalCount())
	}

	for _, p := range []float64{50, 90, 95, 99, 99.9} {
		orig := h.ValueAtPercentile(p)
		got := decoded.ValueAtPercentile(p)
		if orig != got {
			t.Errorf("p%.1f mismatch: got %d, want %d", p, got, orig)
		}
	}
}

func TestMergeGroupHistogram(t *testing.T) {
	gs := NewGroupStats(3)

	gs.Conns[0].Hist.RecordValue(100)
	gs.Conns[0].Hist.RecordValue(200)
	gs.Conns[1].Hist.RecordValue(150)
	gs.Conns[1].Hist.RecordValue(250)
	gs.Conns[2].Hist.RecordValue(300)

	merged := MergeGroupHistogram(gs)

	if merged.TotalCount() != 5 {
		t.Errorf("expected 5 total samples, got %d", merged.TotalCount())
	}

	p50 := merged.ValueAtPercentile(50)
	if p50 < 100 || p50 > 300 {
		t.Errorf("p50 out of range: %d", p50)
	}
}

func TestAggregateGroup(t *testing.T) {
	gs := NewGroupStats(2)
	gs.Conns[0].Count.Store(100)
	gs.Conns[0].Bytes.Store(5000)
	gs.Conns[0].RawCount.Store(110)
	gs.Conns[0].RawBytes.Store(5500)
	gs.Conns[1].Count.Store(200)
	gs.Conns[1].Bytes.Store(10000)
	gs.Conns[1].RawCount.Store(220)
	gs.Conns[1].RawBytes.Store(11000)

	totalMsgs, totalBytes, totalRaw, totalRawBytes := AggregateGroup(gs)

	if totalMsgs != 300 {
		t.Errorf("totalMsgs: got %d, want 300", totalMsgs)
	}
	if totalBytes != 15000 {
		t.Errorf("totalBytes: got %d, want 15000", totalBytes)
	}
	if totalRaw != 330 {
		t.Errorf("totalRaw: got %d, want 330", totalRaw)
	}
	if totalRawBytes != 16500 {
		t.Errorf("totalRawBytes: got %d, want 16500", totalRawBytes)
	}
}

func TestConnStatsToProto(t *testing.T) {
	cs := NewConnStats()
	cs.Count.Store(1000)
	cs.Bytes.Store(50000)
	cs.RawCount.Store(1100)
	cs.RawBytes.Store(55000)
	cs.FirstMsg.Store(true)
	cs.ConnActive.Store(true)
	cs.DisconnectCount.Store(2)
	cs.ReconnectCount.Store(1)
	cs.Hist.RecordValue(150)
	cs.ReconnectHist.RecordValue(5000)

	cr, err := ConnStatsToProto(cs, 3, "ws://localhost:8090")
	if err != nil {
		t.Fatalf("ConnStatsToProto failed: %v", err)
	}

	if cr.ConnIndex != 3 {
		t.Errorf("ConnIndex: got %d, want 3", cr.ConnIndex)
	}
	if cr.Endpoint != "ws://localhost:8090" {
		t.Errorf("Endpoint: got %s", cr.Endpoint)
	}
	if cr.MsgCount != 1000 {
		t.Errorf("MsgCount: got %d, want 1000", cr.MsgCount)
	}
	if cr.ByteCount != 50000 {
		t.Errorf("ByteCount: got %d, want 50000", cr.ByteCount)
	}
	if !cr.FirstMsgReceived {
		t.Error("FirstMsgReceived should be true")
	}
	if !cr.ConnActive {
		t.Error("ConnActive should be true")
	}
	if cr.DisconnectCount != 2 {
		t.Errorf("DisconnectCount: got %d, want 2", cr.DisconnectCount)
	}
	if len(cr.LatencyHistogram) == 0 {
		t.Error("LatencyHistogram should not be empty")
	}
	if len(cr.ReconnectHistogram) == 0 {
		t.Error("ReconnectHistogram should not be empty")
	}
}

func TestGroupStatsToProto(t *testing.T) {
	gs := NewGroupStats(2)
	gs.Conns[0].Count.Store(100)
	gs.Conns[0].Bytes.Store(5000)
	gs.Conns[0].FirstMsg.Store(true)
	gs.Conns[0].Hist.RecordValue(100)
	gs.Conns[0].Hist.RecordValue(200)
	gs.Conns[1].Count.Store(200)
	gs.Conns[1].Bytes.Store(10000)
	gs.Conns[1].FirstMsg.Store(true)
	gs.Conns[1].Hist.RecordValue(150)
	gs.Conns[1].Hist.RecordValue(250)

	gr, err := GroupStatsToProto(gs, "test-group", []string{"ep1", "ep2"}, 2)
	if err != nil {
		t.Fatalf("GroupStatsToProto failed: %v", err)
	}

	if gr.Name != "test-group" {
		t.Errorf("Name: got %s", gr.Name)
	}
	if len(gr.Connections) != 2 {
		t.Errorf("Connections: got %d, want 2", len(gr.Connections))
	}
	if len(gr.LatencyHistogram) == 0 {
		t.Error("group LatencyHistogram should not be empty")
	}

	if gr.Connections[0].Endpoint != "ep1" {
		t.Errorf("conn 0 endpoint: got %s, want ep1", gr.Connections[0].Endpoint)
	}
	if gr.Connections[1].Endpoint != "ep2" {
		t.Errorf("conn 1 endpoint: got %s, want ep2", gr.Connections[1].Endpoint)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	original := NewConnStats()
	for i := int64(50); i < 500; i += 7 {
		original.Hist.RecordValue(i)
	}
	original.Count.Store(12345)
	original.Bytes.Store(999999)
	original.RawCount.Store(13000)
	original.RawBytes.Store(1000000)
	original.FirstMsg.Store(true)
	original.ConnActive.Store(true)
	original.DisconnectCount.Store(3)
	original.ReconnectCount.Store(1)
	original.ReconnectHist.RecordValue(5000)

	cr, err := ConnStatsToProto(original, 0, "ws://test")
	if err != nil {
		t.Fatalf("ConnStatsToProto: %v", err)
	}

	if cr.MsgCount != 12345 {
		t.Errorf("MsgCount roundtrip: got %d, want 12345", cr.MsgCount)
	}

	decoded, err := DecodeHistogram(cr.LatencyHistogram)
	if err != nil {
		t.Fatalf("DecodeHistogram: %v", err)
	}
	if decoded.TotalCount() != original.Hist.TotalCount() {
		t.Errorf("TotalCount roundtrip: got %d, want %d", decoded.TotalCount(), original.Hist.TotalCount())
	}

	for _, p := range []float64{50, 90, 95, 99} {
		orig := original.Hist.ValueAtPercentile(p)
		got := decoded.ValueAtPercentile(p)
		if orig != got {
			t.Errorf("p%.1f roundtrip: got %d, want %d", p, got, orig)
		}
	}
}
