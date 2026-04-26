package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"benchmark-client/internal/report"
	"benchmark-client/internal/stats"
	"benchmark-client/internal/worker"
)

func main() {
	warmup := flag.Int("warmup", 30, "warmup seconds")
	duration := flag.Int("duration", 120, "measurement seconds")
	conns := flag.Int("conns", 1, "connections per group")
	flag.Parse()

	groups := []stats.Group{
		{Name: "WS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8090", "ws://127.0.0.1:8090", "ws://127.0.0.1:8090"}},
		{Name: "uWS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8091", "ws://127.0.0.1:8091", "ws://127.0.0.1:8091"}},
		{Name: "Go WS (host/PM2)", Type: "ws", Endpoints: []string{"ws://127.0.0.1:8092", "ws://127.0.0.1:8093", "ws://127.0.0.1:8094"}},
		{Name: "uWS bridge", Type: "ws", Endpoints: []string{"ws://127.0.0.1:50061", "ws://127.0.0.1:50061", "ws://127.0.0.1:50061"}},
		{Name: "Go WS bridge", Type: "ws", Endpoints: []string{"ws://127.0.0.1:50071", "ws://127.0.0.1:50071", "ws://127.0.0.1:50071"}},
		{Name: "uWS host", Type: "ws", Endpoints: []string{"ws://127.0.0.1:60061", "ws://127.0.0.1:60062", "ws://127.0.0.1:60063"}},
		{Name: "Go WS host", Type: "ws", Endpoints: []string{"ws://127.0.0.1:60071", "ws://127.0.0.1:60072", "ws://127.0.0.1:60073"}},
		{Name: "gRPC bridge", Type: "grpc", Endpoints: []string{"localhost:50051", "localhost:50051", "localhost:50051"}},
		{Name: "gRPC host", Type: "grpc", Endpoints: []string{"localhost:60051", "localhost:60052", "localhost:60053"}},
	}

	log.Printf("[client] Warmup: %ds, Measurement: %ds, Conns/group: %d", *warmup, *duration, *conns)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var measuring atomic.Bool
	allStats := make([]*stats.GroupStats, len(groups))
	for i := range allStats {
		allStats[i] = stats.NewGroupStats(*conns)
	}

	totalConns := 0
	var wg sync.WaitGroup

	for gi := range groups {
		epCount := len(groups[gi].Endpoints)
		for ci := 0; ci < *conns; ci++ {
			endpoint := groups[gi].Endpoints[ci%epCount]
			wg.Add(1)
			totalConns++
			if groups[gi].Type == "ws" {
				go worker.ConnectWS(ctx, groups[gi], gi, ci, endpoint, allStats, &measuring, &wg)
			} else {
				go worker.ConnectGRPC(ctx, groups[gi], gi, ci, endpoint, allStats, &measuring, &wg)
			}
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("[client] %d total connections across %d groups connecting...", totalConns, len(groups))
	time.Sleep(5 * time.Second)

	activeConns := 0
	for gi := range groups {
		for _, cs := range allStats[gi].Conns {
			if cs.ConnActive.Load() {
				activeConns++
			}
		}
	}
	log.Printf("[client] %d/%d connections active after 5s", activeConns, totalConns)

	if activeConns == 0 {
		log.Printf("[client] WARNING: no active connections! Waiting 10s more...")
		time.Sleep(10 * time.Second)
		activeConns = 0
		for gi := range groups {
			for _, cs := range allStats[gi].Conns {
				if cs.ConnActive.Load() {
					activeConns++
				}
			}
		}
		log.Printf("[client] %d/%d connections active after retry", activeConns, totalConns)
	}

	go report.PrintLiveStats(groups, allStats, &measuring)

	log.Printf("[client] Warmup for %ds...", *warmup)
	select {
	case <-time.After(time.Duration(*warmup) * time.Second):
	case <-sigCh:
		cancel()
		return
	}

	log.Printf("[client] Measurement phase (%ds)...", *duration)
	measuring.Store(true)
	measureStart := time.Now()

	select {
	case <-time.After(time.Duration(*duration) * time.Second):
	case <-sigCh:
	}
	measuring.Store(false)
	measureEnd := time.Now()
	cancel()
	wg.Wait()

	log.SetOutput(io.Discard)
	time.Sleep(100 * time.Millisecond)
	log.SetOutput(os.Stderr)

	measureDuration := measureEnd.Sub(measureStart).Seconds()
	report.PrintReport(groups, allStats, measureDuration, *conns)
}
