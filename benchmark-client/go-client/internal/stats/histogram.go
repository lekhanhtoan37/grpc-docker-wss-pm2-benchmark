package stats

import (
	"github.com/HdrHistogram/hdrhistogram-go"
)

func MergeGroupHistogram(gs *GroupStats) *hdrhistogram.Histogram {
	merged := hdrhistogram.New(1, 3600000000, 3)
	for _, cs := range gs.Conns {
		merged.Merge(cs.Hist)
	}
	return merged
}

func AggregateGroup(gs *GroupStats) (totalMsgs, totalBytes, totalRaw, totalRawBytes int64) {
	for _, cs := range gs.Conns {
		totalMsgs += cs.Count.Load()
		totalBytes += cs.Bytes.Load()
		totalRaw += cs.RawCount.Load()
		totalRawBytes += cs.RawBytes.Load()
	}
	return
}

func EncodeHistogram(h *hdrhistogram.Histogram) ([]byte, error) {
	return h.Encode(hdrhistogram.V2CompressedEncodingCookieBase)
}

func DecodeHistogram(data []byte) (*hdrhistogram.Histogram, error) {
	return hdrhistogram.Decode(data)
}
