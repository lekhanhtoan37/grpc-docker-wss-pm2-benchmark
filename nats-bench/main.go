package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/nats-io/nats.go"
)

func main() {
	natsURL := flag.String("nats-url", "nats://localhost:4222", "NATS server URL")
	subject := flag.String("subject", "bench.test", "NATS subject")
	mode := flag.String("mode", "both", "Mode: both, pub, sub")
	duration := flag.Int("duration", 120, "Measurement duration (seconds)")
	warmup := flag.Int("warmup", 30, "Warmup duration (seconds)")
	msgSize := flag.Int("msg-size", 1024, "Message size (bytes)")
	batchSize := flag.Int("batch-size", 20, "Messages per batch")
	queueGroup := flag.String("queue", "", "Queue group name (empty = no queue)")
	flag.Parse()

	nc, err := nats.Connect(*natsURL,
		nats.Name("nats-bench"),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		log.Fatalf("NATS connect error: %v", err)
	}
	defer nc.Close()

	log.Printf("[nats-bench] Connected to %s, subject=%s, mode=%s", *natsURL, *subject, *mode)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	var measuring atomic.Bool

	pubHist := hdrhistogram.New(1, 3600000000, 3)
	subHist := hdrhistogram.New(1, 3600000000, 3)
	var subCount atomic.Int64
	var subBytes atomic.Int64

	if *mode == "both" || *mode == "sub" {
		handler := func(msg *nats.Msg) {
			now := time.Now().UnixMicro()
			data := msg.Data

			if !measuring.Load() {
				subCount.Add(1)
				subBytes.Add(int64(len(data)))
				return
			}

			msgCount := 0
			start := 0
			for start < len(data) {
				end := start
				for end < len(data) && data[end] != '\n' {
					end++
				}
				if end > start {
					msgCount++
					ts := extractTimestamp(data[start:end])
					if ts > 0 {
						lat := now - ts*1000
						if lat > 0 {
							subHist.RecordValue(lat)
						}
					}
				}
				start = end + 1
			}

			subCount.Add(int64(msgCount))
			subBytes.Add(int64(len(data)))
		}

		if *queueGroup != "" {
			_, err = nc.QueueSubscribe(*subject, *queueGroup, handler)
		} else {
			_, err = nc.Subscribe(*subject, handler)
		}
		if err != nil {
			log.Fatalf("Subscribe error: %v", err)
		}
		log.Printf("[nats-bench] Subscribed to %s", *subject)
	}

	if *mode == "both" || *mode == "pub" {
		data := strings.Repeat("x", *msgSize-40)
		go func() {
			seq := int64(0)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				var payload strings.Builder
				for i := 0; i < *batchSize; i++ {
					ts := time.Now().UnixMicro()
					fmt.Fprintf(&payload, `{"timestamp":%d,"seq":%d,"data":"%s"}`, ts, seq, data)
					if i < *batchSize-1 {
						payload.WriteByte('\n')
					}
					seq++
				}

				start := time.Now()
				if err := nc.Publish(*subject, []byte(payload.String())); err != nil {
					log.Printf("Publish error: %v", err)
					time.Sleep(100 * time.Millisecond)
					continue
				}

				if measuring.Load() {
					pubHist.RecordValue(time.Since(start).Microseconds())
				}
			}
		}()
	}

	time.Sleep(1 * time.Second)

	log.Printf("[nats-bench] Warmup for %ds...", *warmup)
	select {
	case <-time.After(time.Duration(*warmup) * time.Second):
	case <-ctx.Done():
		return
	}

	log.Printf("[nats-bench] Measurement phase (%ds)...", *duration)
	measuring.Store(true)
	measureStart := time.Now()

	statsTicker := time.NewTicker(5 * time.Second)
	go func() {
		for range statsTicker.C {
			elapsed := time.Since(measureStart).Seconds()
			if elapsed > 0 {
				log.Printf("[nats-bench] msgs=%d MB=%.2f msg/s=%.0f",
					subCount.Load(),
					float64(subBytes.Load())/1024/1024,
					float64(subCount.Load())/elapsed)
			}
		}
	}()

	select {
	case <-time.After(time.Duration(*duration) * time.Second):
	case <-ctx.Done():
	}
	measuring.Store(false)
	measureDuration := time.Since(measureStart).Seconds()
	statsTicker.Stop()

	cancel()
	nc.Flush()
	time.Sleep(500 * time.Millisecond)

	fmt.Println()
	fmt.Println("=== NATS BENCHMARK RESULTS ===")
	fmt.Println()
	fmt.Printf("Duration: %.1fs\n", measureDuration)
	fmt.Printf("Messages: %d\n", subCount.Load())
	fmt.Printf("MB/s:     %.2f\n", float64(subBytes.Load())/1024/1024/measureDuration)
	fmt.Printf("msg/s:    %.0f\n", float64(subCount.Load())/measureDuration)
	fmt.Println()
	fmt.Println("Latency (microseconds):")
	for _, p := range []float64{50, 75, 90, 95, 99, 99.9} {
		val := subHist.ValueAtPercentile(p)
		fmt.Printf("  p%.1f:  %.1f\n", p, float64(val))
	}

	if *mode == "both" || *mode == "pub" {
		fmt.Println()
		fmt.Println("Publish Latency (microseconds):")
		for _, p := range []float64{50, 75, 90, 95, 99, 99.9} {
			val := pubHist.ValueAtPercentile(p)
			fmt.Printf("  p%.1f:  %.1f\n", p, float64(val))
		}
	}
}

func extractTimestamp(msg []byte) int64 {
	key := []byte(`"timestamp":`)
	idx := findBytes(msg, key)
	if idx < 0 {
		return 0
	}
	i := idx + len(key)
	for i < len(msg) && (msg[i] == ' ' || msg[i] == '\t') {
		i++
	}
	var n int64
	for i < len(msg) && msg[i] >= '0' && msg[i] <= '9' {
		n = n*10 + int64(msg[i]-'0')
		i++
	}
	return n
}

func findBytes(data, pattern []byte) int {
	for i := 0; i <= len(data)-len(pattern); i++ {
		match := true
		for j := range pattern {
			if data[i+j] != pattern[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
