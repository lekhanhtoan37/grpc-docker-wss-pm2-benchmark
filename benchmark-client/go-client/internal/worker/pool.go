package worker

import "sync"

var LatencySlicePool = sync.Pool{
	New: func() any {
		return make([]int64, 0, 64)
	},
}

func GetLatencySlice() []int64 {
	return LatencySlicePool.Get().([]int64)[:0]
}

func PutLatencySlice(v []int64) {
	if v == nil {
		return
	}
	if cap(v) > 1024 {
		v = make([]int64, 0, 64)
	} else {
		v = v[:0]
	}
	LatencySlicePool.Put(v)
}
