package worker

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"benchmark-client/internal/stats"

	"github.com/coder/websocket"
)

func ConnectWS(ctx context.Context, group stats.Group, gi, ci int, endpoint string, allStats []*stats.GroupStats, measuring *atomic.Bool, wg *sync.WaitGroup) {
	defer wg.Done()

	cs := allStats[gi].Conns[ci]

	events := make(chan WSFrameEvent, 2048)

	var statsWG sync.WaitGroup
	statsWG.Add(1)
	go WSStatsWorker(cs, events, &statsWG)

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
		firstConnect := !cs.FirstMsg.Load()
		if firstConnect {
			log.Printf("[client] %s conn#%d connected to %s (%.2fms)", group.Name, ci+1, endpoint, float64(connectMicros)/1000.0)
		} else {
			cs.ReconnectCount.Add(1)
			_ = cs.ReconnectHist.RecordValue(connectMicros)
		}
		cs.ConnActive.Store(true)

		for {
			_, reader, err := conn.Reader(ctx)
			if err != nil {
				if cs.DisconnectCount.Load() < 3 {
					log.Printf("[client] %s conn#%d read error: %v", group.Name, ci+1, err)
				}
				cs.DisconnectCount.Add(1)
				cs.ConnActive.Store(false)
				_ = conn.Close(websocket.StatusInternalError, "read error")
				break
			}

			frameBuf, err = ReadFrameReusable(reader, frameBuf)
			if err != nil {
				if cs.DisconnectCount.Load() < 3 {
					log.Printf("[client] %s conn#%d read frame error: %v", group.Name, ci+1, err)
				}
				cs.DisconnectCount.Add(1)
				cs.ConnActive.Store(false)
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
				samples = GetLatencySlice()
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

					if !cs.FirstMsg.Load() {
						cs.FirstMsg.Store(true)
					}

					if measuringNow {
						ts := ExtractTimestampInt64(line)
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

			if !measuringNow {
				continue
			}

			if msgCount == 0 {
				PutLatencySlice(samples)
				continue
			}

			events <- WSFrameEvent{
				MsgCount: int64(msgCount),
				ByteSize: int64(len(frameBuf)),
				Samples:  samples,
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
