package worker

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	pb "benchmark-client/proto"

	"benchmark-client/internal/stats"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func ConnectGRPC(ctx context.Context, group stats.Group, gi, ci int, endpoint string, allStats []*stats.GroupStats, measuring *atomic.Bool, wg *sync.WaitGroup) {
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

		firstConnect := !allStats[gi].Conns[ci].FirstMsg.Load()
		if firstConnect {
			log.Printf("[client] %s conn#%d connected to %s (%dms)", group.Name, ci+1, endpoint, time.Since(connectStart).Milliseconds())
		}
		allStats[gi].Conns[ci].ConnActive.Store(true)
		if !firstConnect {
			allStats[gi].Conns[ci].ReconnectCount.Add(1)
			allStats[gi].Conns[ci].ReconnectHist.RecordValue(time.Since(connectStart).Milliseconds() * 1000)
		}

		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				allStats[gi].Conns[ci].DisconnectCount.Add(1)
				allStats[gi].Conns[ci].ConnActive.Store(false)
				conn.Close()
				break
			}
			if err != nil {
				allStats[gi].Conns[ci].DisconnectCount.Add(1)
				allStats[gi].Conns[ci].ConnActive.Store(false)
				break
			}

			cs := allStats[gi].Conns[ci]
			entries := resp.GetMessages()
			batchLen := int64(len(entries))

			if !cs.FirstMsg.Load() && batchLen > 0 {
				cs.FirstMsg.Store(true)
			}

			if !measuring.Load() {
				continue
			}

			var payloadBytes int64
			for _, e := range entries {
				payloadBytes += int64(len(e.GetPayload()))
			}

			cs.RawCount.Add(batchLen)
			cs.RawBytes.Add(payloadBytes)
			cs.Count.Add(batchLen)
			cs.Bytes.Add(payloadBytes)

			nowMicros := time.Now().UnixMicro()
			for _, e := range entries {
				ts := e.GetTimestamp()
				if ts == 0 {
					continue
				}
				tsMicros := int64(ts * 1000)
				latencyMicros := nowMicros - tsMicros
				if latencyMicros > 0 && latencyMicros < 3600000000 {
					cs.Hist.RecordValue(latencyMicros)
				}
			}
		}

		conn.Close()
		time.Sleep(500 * time.Millisecond)
	}
}
