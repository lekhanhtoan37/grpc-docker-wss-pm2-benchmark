package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"benchmark-client/internal/worker"
)

func main() {
	coordinatorAddr := flag.String("coordinator", "localhost:50000", "coordinator address")
	workerID := flag.String("worker-id", "", "worker ID (auto-generated if empty)")
	flag.Parse()

	if *workerID == "" {
		*workerID = fmt.Sprintf("worker-%d", os.Getpid())
	}

	log.Printf("[worker %s] Connecting to coordinator at %s", *workerID, *coordinatorAddr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Printf("[worker %s] Received signal, shutting down...", *workerID)
		cancel()
	}()

	runner := &worker.Runner{
		CoordinatorAddr: *coordinatorAddr,
		WorkerID:        *workerID,
	}

	if err := runner.Run(ctx); err != nil {
		log.Fatalf("[worker %s] Error: %v", *workerID, err)
	}
}
