package worker

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"benchmark-client/internal/stats"

	"github.com/nats-io/nats.go"
)

func ConnectNATS(ctx context.Context, group stats.Group, gi, ci int, endpoint string, allStats []*stats.GroupStats, measuring *atomic.Bool, wg *sync.WaitGroup) {
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

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		nc, err := nats.Connect(endpoint,
			nats.Name(fmt.Sprintf("bench-%d-%d", gi, ci)),
			nats.ReconnectWait(2*time.Second),
			nats.MaxReconnects(-1),
		)
		if err != nil {
			log.Printf("[client] %s conn#%d NATS connect error: %v", group.Name, ci+1, err)
			time.Sleep(3 * time.Second)
			continue
		}

		ch := make(chan *nats.Msg, 8192)
		var sub *nats.Subscription
		if group.QueueGroup != "" {
			sub, err = nc.ChanQueueSubscribe(group.Subject, group.QueueGroup, ch)
		} else {
			sub, err = nc.ChanSubscribe(group.Subject, ch)
		}
		if err != nil {
			log.Printf("[client] %s conn#%d NATS subscribe error: %v", group.Name, ci+1, err)
			nc.Close()
			time.Sleep(3 * time.Second)
			continue
		}

		firstConnect := !cs.FirstMsg.Load()
		if firstConnect {
			log.Printf("[client] %s conn#%d connected to NATS %s (subject=%s)", group.Name, ci+1, endpoint, group.Subject)
		} else {
			cs.ReconnectCount.Add(1)
		}
		cs.ConnActive.Store(true)

		for {
			select {
			case <-ctx.Done():
				sub.Unsubscribe()
				nc.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					cs.DisconnectCount.Add(1)
					cs.ConnActive.Store(false)
					break
				}

				data := msg.Data
				if len(data) == 0 {
					continue
				}

				if !cs.FirstMsg.Load() {
					cs.FirstMsg.Store(true)
				}

				if !measuring.Load() {
					continue
				}

				var samples []int64
				nowMicros := time.Now().UnixMicro()
				msgCount := 0
				start := 0

				for start < len(data) {
					end := start
					for end < len(data) && data[end] != '\n' {
						end++
					}
					if end > start {
						msgCount++
						ts := ExtractTimestampInt64(data[start:end])
						if ts > 0 {
							lat := nowMicros - ts*1000
							if lat < 1 {
								lat = 1
							}
							samples = append(samples, lat)
						}
					}
					start = end + 1
				}

				if msgCount > 0 {
					events <- WSFrameEvent{
						MsgCount: int64(msgCount),
						ByteSize: int64(len(data)),
						Samples:  samples,
					}
				}
			}
		}

		nc.Close()
		time.Sleep(500 * time.Millisecond)
	}
}
