package worker

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	pb "benchmark-client/proto/control"

	"benchmark-client/internal/stats"
)

type Runner struct {
	CoordinatorAddr string
	WorkerID        string
}

func (r *Runner) Run(ctx context.Context) error {
	conn, err := connectCoordinator(r.CoordinatorAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pb.NewCoordinatorServiceClient(conn)

	resp, err := client.RegisterWorker(ctx, &pb.RegisterRequest{
		WorkerId: r.WorkerID,
		Hostname: "localhost",
	})
	if err != nil {
		return err
	}

	if len(resp.Groups) == 0 {
		log.Printf("[worker %s] No groups assigned, exiting", r.WorkerID)
		return nil
	}

	groups := make([]stats.Group, len(resp.Groups))
	for i, ga := range resp.Groups {
		groups[i] = stats.Group{
			Name:      ga.Name,
			Type:      ga.Type,
			Endpoints: ga.Endpoints,
		}
	}

	allStats := make([]*stats.GroupStats, len(groups))
	for i := range groups {
		allStats[i] = stats.NewGroupStats(int(resp.Groups[i].Connections))
	}

	connCtx, connCancel := context.WithCancel(context.Background())
	var measuring atomic.Bool
	var wg sync.WaitGroup

	totalConns := 0
	for gi, g := range groups {
		conns := int(resp.Groups[gi].Connections)
		epCount := len(g.Endpoints)
		for ci := 0; ci < conns; ci++ {
			endpoint := g.Endpoints[ci%epCount]
			wg.Add(1)
			totalConns++
			if g.Type == "ws" {
				go ConnectWS(connCtx, g, gi, ci, endpoint, allStats, &measuring, &wg)
			} else {
				go ConnectGRPC(connCtx, g, gi, ci, endpoint, allStats, &measuring, &wg)
			}
		}
	}

	log.Printf("[worker %s] %d total connections across %d groups connecting...", r.WorkerID, totalConns, len(groups))
	time.Sleep(5 * time.Second)

	barrierTime := time.UnixMilli(resp.MeasureStartUnixMs)
	waitDuration := time.Until(barrierTime)
	if waitDuration > 0 {
		log.Printf("[worker %s] Waiting %.1fs for measurement barrier...", r.WorkerID, waitDuration.Seconds())
		select {
		case <-time.After(waitDuration):
		case <-ctx.Done():
			connCancel()
			wg.Wait()
			return ctx.Err()
		}
	}

	log.Printf("[worker %s] Warmup for %ds...", r.WorkerID, resp.WarmupSeconds)
	select {
	case <-time.After(time.Duration(resp.WarmupSeconds) * time.Second):
	case <-ctx.Done():
		connCancel()
		wg.Wait()
		return ctx.Err()
	}

	log.Printf("[worker %s] Measurement phase (%ds)...", r.WorkerID, resp.MeasureSeconds)
	measuring.Store(true)
	measureStart := time.Now()

	select {
	case <-time.After(time.Duration(resp.MeasureSeconds) * time.Second):
	case <-ctx.Done():
	}
	measuring.Store(false)
	measureEnd := time.Now()

	connCancel()
	wg.Wait()

	measureDurationMs := measureEnd.Sub(measureStart).Milliseconds()

	groupResults := make([]*pb.GroupResult, len(groups))
	for gi, g := range groups {
		gr, err := stats.GroupStatsToProto(allStats[gi], g.Name, g.Endpoints, int(resp.Groups[gi].Connections))
		if err != nil {
			log.Printf("[worker %s] Error serializing group %s: %v", r.WorkerID, g.Name, err)
			continue
		}
		groupResults[gi] = gr
	}

	reportCtx, reportCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer reportCancel()
	_, err = client.ReportFinal(reportCtx, &pb.FinalReport{
		WorkerId:          r.WorkerID,
		MeasureDurationMs: measureDurationMs,
		Groups:            groupResults,
	})
	if err != nil {
		return err
	}

	log.Printf("[worker %s] Final report sent, shutting down", r.WorkerID)
	return nil
}
