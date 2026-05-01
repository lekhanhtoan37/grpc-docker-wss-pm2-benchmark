package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/IBM/sarama"
	"github.com/nats-io/nats.go"
)

var (
	BASE_PORT   = envInt("PORT", 8095)
	BROKER      = envStr("KAFKA_BROKER", "192.168.0.9:9091")
	TOPIC       = envStr("KAFKA_TOPIC", "benchmark-messages")
	INSTANCE    = envStr("NODE_APP_INSTANCE", envStr("CONTAINER_ID", envStr("HOSTNAME", strconv.Itoa(os.Getpid()))))
	PORT        = envInt("LISTEN_PORT", calcPort(BASE_PORT, INSTANCE))
	GROUP_ID    = envStr("GROUP_ID", "nats-benchmark-worker-"+INSTANCE)
	BATCH_MAX   = envInt("BATCH_MAX", 20)
	LINGER_MS   = envInt("LINGER_MS", 5)
	NATS_URL    = envStr("NATS_URL", "nats://localhost:4222")
	NATS_SUBJECT = envStr("NATS_SUBJECT", "benchmark.messages")
)

func calcPort(base int, instance string) int {
	idx, err := strconv.Atoi(instance)
	if err != nil {
		if len(instance) > 0 {
			lastChar := instance[len(instance)-1]
			if digit, err := strconv.Atoi(string(lastChar)); err == nil && digit >= 1 {
				return base + digit - 1
			}
		}
		return base
	}
	return base + idx
}

type Server struct {
	nc         *nats.Conn
	buffer     [][]byte
	flushMu    sync.Mutex
	timer      *time.Timer
	flushing   bool
	shutdown   atomic.Bool
	batchCount int64
	msgsIn     int64
	msgsOut    int64
	flushes    int64
	startTime  time.Time
}

func (s *Server) flushToNATS() {
	s.flushMu.Lock()
	if s.flushing || len(s.buffer) == 0 {
		s.flushMu.Unlock()
		return
	}
	s.flushing = true
	entries := s.buffer
	s.buffer = nil
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.flushMu.Unlock()

	atomic.AddInt64(&s.flushes, 1)

	totalLen := 0
	for _, e := range entries {
		totalLen += len(e) + 1
	}
	payload := make([]byte, 0, totalLen)
	for i, e := range entries {
		payload = append(payload, e...)
		if i < len(entries)-1 {
			payload = append(payload, '\n')
		}
	}
	atomic.AddInt64(&s.msgsOut, int64(len(entries)))

	if err := s.nc.Publish(NATS_SUBJECT, payload); err != nil {
		log.Printf("[nats-worker:%s] publish error: %v", INSTANCE, err)
	}
}

func (s *Server) appendBatch(entries [][]byte) {
	s.flushMu.Lock()
	atomic.AddInt64(&s.msgsIn, int64(len(entries)))
	atomic.AddInt64(&s.batchCount, 1)

	s.buffer = append(s.buffer, entries...)

	if len(s.buffer) >= BATCH_MAX {
		s.flushMu.Unlock()
		s.flushToNATS()
		return
	}

	if len(s.buffer) > 0 && s.timer == nil && !s.flushing {
		s.timer = time.AfterFunc(time.Duration(LINGER_MS)*time.Millisecond, func() {
			s.flushMu.Lock()
			s.timer = nil
			s.flushMu.Unlock()
			s.flushToNATS()
		})
	}
	s.flushMu.Unlock()
}

type consumerHandler struct {
	server *Server
	ctx    context.Context
}

func (h *consumerHandler) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (h *consumerHandler) Cleanup(sarama.ConsumerGroupSession) error { return nil }
func (h *consumerHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	entries := make([][]byte, 0, 4096)
	var lastMsg *sarama.ConsumerMessage

	for {
		select {
		case <-h.ctx.Done():
			if len(entries) > 0 {
				h.server.appendBatch(entries)
				if lastMsg != nil {
					session.MarkMessage(lastMsg, "")
				}
			}
			return nil
		case msg, ok := <-claim.Messages():
			if !ok {
				if len(entries) > 0 {
					h.server.appendBatch(entries)
					if lastMsg != nil {
						session.MarkMessage(lastMsg, "")
					}
				}
				return nil
			}
			if h.server.shutdown.Load() {
				return nil
			}
			entries = append(entries, msg.Value)
			lastMsg = msg

		drain:
			for {
				select {
				case msg2, ok2 := <-claim.Messages():
					if !ok2 {
						break drain
					}
					if h.server.shutdown.Load() {
						if len(entries) > 0 {
							h.server.appendBatch(entries)
						}
						return nil
					}
					entries = append(entries, msg2.Value)
					lastMsg = msg2
				default:
					break drain
				}
			}

			if len(entries) > 0 {
				h.server.appendBatch(entries)
				if lastMsg != nil {
					session.MarkMessage(lastMsg, "")
				}
				entries = make([][]byte, 0, 4096)
				lastMsg = nil
			}
		}
	}
}

func (s *Server) startKafkaConsumer(ctx context.Context) {
	config := sarama.NewConfig()
	config.ClientID = "nats-worker-" + INSTANCE
	config.Consumer.Group.Rebalance.GroupStrategies = []sarama.BalanceStrategy{sarama.NewBalanceStrategyRange()}
	config.Consumer.MaxProcessingTime = 100 * time.Millisecond
	config.Consumer.Fetch.Max = 50 * 1024 * 1024
	config.Consumer.Fetch.Min = 1
	config.Consumer.Fetch.Default = 1 * 1024 * 1024
	config.Consumer.MaxWaitTime = 100 * time.Millisecond
	config.Consumer.Group.Session.Timeout = 30 * time.Second
	config.Consumer.Group.Rebalance.Timeout = 30 * time.Second
	config.ChannelBufferSize = 100000

	group, err := sarama.NewConsumerGroup([]string{BROKER}, GROUP_ID, config)
	if err != nil {
		log.Fatalf("[nats-worker:%s] Kafka consumer group error: %v", INSTANCE, err)
	}
	defer group.Close()

	handler := &consumerHandler{server: s, ctx: ctx}

	log.Printf("[nats-worker:%s] Kafka consumer connected (group: %s)", INSTANCE, GROUP_ID)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := group.Consume(ctx, []string{TOPIC}, handler); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[nats-worker:%s] Kafka consume error: %v, retrying...", INSTANCE, err)
			time.Sleep(5 * time.Second)
		}
	}
}

func (s *Server) printStats() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		elapsed := time.Since(s.startTime).Seconds()
		if elapsed > 0 {
			log.Printf("[nats-worker:%s] batches=%d in=%d out=%d flushes=%d in/s=%.0f out/s=%.0f buffer=%d",
				INSTANCE,
				atomic.LoadInt64(&s.batchCount),
				atomic.LoadInt64(&s.msgsIn),
				atomic.LoadInt64(&s.msgsOut),
				atomic.LoadInt64(&s.flushes),
				float64(atomic.LoadInt64(&s.msgsIn))/elapsed,
				float64(atomic.LoadInt64(&s.msgsOut))/elapsed,
				len(s.buffer))
		}
	}
}

func main() {
	nc, err := nats.Connect(NATS_URL,
		nats.Name("nats-worker-"+INSTANCE),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
		nats.FlusherTimeout(10*time.Second),
	)
	if err != nil {
		log.Fatalf("[nats-worker:%s] NATS connect error: %v", INSTANCE, err)
	}
	defer nc.Close()

	log.Printf("[nats-worker:%s] Connected to NATS %s", INSTANCE, NATS_URL)

	s := &Server{
		nc:        nc,
		startTime: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go s.startKafkaConsumer(ctx)
	go s.printStats()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if nc.IsConnected() {
			w.WriteHeader(200)
			w.Write([]byte("ok"))
		} else {
			w.WriteHeader(503)
			w.Write([]byte("nats disconnected"))
		}
	})

	addr := ":" + strconv.Itoa(PORT)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("[nats-worker:%s] Listen on %s failed: %v", INSTANCE, addr, err)
	}

	go func() {
		log.Printf("[nats-worker:%s] Listening on %s", INSTANCE, addr)
		if err := http.Serve(ln, mux); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[nats-worker:%s] Serve failed: %v", INSTANCE, err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Printf("[nats-worker:%s] Shutting down...", INSTANCE)
	s.shutdown.Store(true)
	s.flushToNATS()
	nc.Flush()
	cancel()
	ln.Close()
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
