package worker

import (
	"sync"
	"time"

	"benchmark-client/internal/stats"
)

type WSFrameEvent struct {
	MsgCount int64
	ByteSize int64
	Samples  []int64
}

func WSStatsWorker(cs *stats.ConnStats, in <-chan WSFrameEvent, wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	var localMsgs int64
	var localBytes int64

	flush := func() {
		if localMsgs != 0 {
			cs.Count.Add(localMsgs)
			cs.RawCount.Add(localMsgs)
			localMsgs = 0
		}
		if localBytes != 0 {
			cs.Bytes.Add(localBytes)
			cs.RawBytes.Add(localBytes)
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

			localMsgs += ev.MsgCount
			localBytes += ev.ByteSize

			if len(ev.Samples) > 0 {
				for _, lat := range ev.Samples {
					_ = cs.Hist.RecordValue(lat)
				}
				PutLatencySlice(ev.Samples)
			}

			if localMsgs >= 32000 || localBytes >= 8*1024*1024 {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}
