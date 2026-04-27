package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"nhooyr.io/websocket"
	"github.com/IBM/sarama"
)

var (
	BASE_PORT = envInt("PORT", 8092)
	BROKER    = envStr("KAFKA_BROKER", "192.168.0.9:9091")
	TOPIC     = envStr("KAFKA_TOPIC", "benchmark-messages")
	INSTANCE  = envStr("NODE_APP_INSTANCE", envStr("CONTAINER_ID", envStr("HOSTNAME", strconv.Itoa(os.Getpid()))))
	PORT      = calcPort(BASE_PORT, INSTANCE)
	GROUP_ID  = envStr("GROUP_ID", "go-ws-benchmark-worker-"+INSTANCE)
	BATCH_MAX = envInt("BATCH_MAX", 20)
	LINGER_MS = envInt("LINGER_MS", 5)
)

func calcPort(base int, instance string) int {
	idx, err := strconv.Atoi(instance)
	if err != nil {
		return base
	}
	return base + idx
}

const MAX_BACKPRESSURE = 256 * 1024 * 1024
const WRITE_TIMEOUT = 2 * time.Second

type Client struct {
	conn     *websocket.Conn
	sendCh   chan []byte
	buffered int64
	alive    atomic.Bool
}

func newClient(conn *websocket.Conn) *Client {
	c := &Client{
		conn:   conn,
		sendCh: make(chan []byte, 4000),
	}
	c.alive.Store(true)
	go c.writePump()
	return c
}

func (c *Client) isAlive() bool {
	return c.alive.Load()
}

func (c *Client) markDead() bool {
	return c.alive.CompareAndSwap(true, false)
}

func (c *Client) writePump() {
	for msg := range c.sendCh {
		ctx, cancel := context.WithTimeout(context.Background(), WRITE_TIMEOUT)
		err := c.conn.Write(ctx, websocket.MessageText, msg)
		cancel()

		atomic.AddInt64(&c.buffered, -int64(len(msg)))

		if err != nil {
			if c.markDead() {
				_ = c.conn.Close(1001, "write error")
			}
			return
		}
	}
}

func (c *Client) trySend(msg []byte) bool {
	if !c.isAlive() {
		return false
	}

	msgSize := int64(len(msg))
	newBuf := atomic.AddInt64(&c.buffered, msgSize)
	if newBuf > MAX_BACKPRESSURE {
		atomic.AddInt64(&c.buffered, -msgSize)
		return false
	}

	select {
	case c.sendCh <- msg:
		return true
	default:
		atomic.AddInt64(&c.buffered, -msgSize)
		return false
	}
}

func (c *Client) closeWithReason(code websocket.StatusCode, reason string) {
	if !c.markDead() {
		return
	}
	close(c.sendCh)
	_ = c.conn.Close(code, reason)
}

func (c *Client) close() {
	c.alive.Store(false)
	close(c.sendCh)
	c.conn.Close(1001, "server shutdown")
}

type Server struct {
	clients    map[*Client]bool
	clientsMu  sync.RWMutex
	buffer     []string
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

func (s *Server) flushToClients() {
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
	atomic.AddInt64(&s.msgsOut, int64(len(entries)))
	payload := []byte(strings.Join(entries, "\n"))

	s.clientsMu.Lock()
	var dead []*Client
	for c := range s.clients {
		if !c.isAlive() {
			dead = append(dead, c)
			continue
		}

		ok := c.trySend(payload)
		if !ok {
			dead = append(dead, c)
		}
	}

	for _, c := range dead {
		delete(s.clients, c)
		c.closeWithReason(1001, "backpressure")
		log.Printf("[go-ws:%s] conn closed due to backpressure (total: %d)", INSTANCE, len(s.clients))
	}
	s.clientsMu.Unlock()

	s.flushMu.Lock()
	s.flushing = false
	s.flushMu.Unlock()
}

func (s *Server) appendBatch(entries []string) {
	s.flushMu.Lock()
	atomic.AddInt64(&s.msgsIn, int64(len(entries)))
	atomic.AddInt64(&s.batchCount, 1)

	s.buffer = append(s.buffer, entries...)

	if len(s.buffer) >= BATCH_MAX {
		s.flushMu.Unlock()
		s.flushToClients()
		return
	}

	if len(s.buffer) > 0 && s.timer == nil && !s.flushing {
		s.timer = time.AfterFunc(time.Duration(LINGER_MS)*time.Millisecond, func() {
			s.flushMu.Lock()
			s.timer = nil
			s.flushMu.Unlock()
			s.flushToClients()
		})
	}
	s.flushMu.Unlock()
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	c := newClient(conn)
	s.clientsMu.Lock()
	s.clients[c] = true
	count := len(s.clients)
	s.clientsMu.Unlock()
	log.Printf("[go-ws:%s] conn connected (total: %d)", INSTANCE, count)

	for {
		_, _, err := conn.Read(context.Background())
		if err != nil {
			s.clientsMu.Lock()
			delete(s.clients, c)
			count = len(s.clients)
			s.clientsMu.Unlock()
			c.close()
			log.Printf("[go-ws:%s] conn disconnected (total: %d)", INSTANCE, count)
			break
		}
	}
}

type consumerHandler struct {
	server *Server
	ctx    context.Context
}

func (h *consumerHandler) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (h *consumerHandler) Cleanup(sarama.ConsumerGroupSession) error { return nil }
func (h *consumerHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	entries := make([]string, 0, 4096)
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
			entries = append(entries, string(msg.Value))
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
					entries = append(entries, string(msg2.Value))
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
				entries = make([]string, 0, 4096)
				lastMsg = nil
			}
		}
	}
}

func (s *Server) startKafkaConsumer(ctx context.Context) {
	config := sarama.NewConfig()
	config.ClientID = "go-ws-" + INSTANCE
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
		log.Fatalf("[go-ws:%s] Kafka consumer group error: %v", INSTANCE, err)
	}
	defer group.Close()

	handler := &consumerHandler{server: s, ctx: ctx}

	log.Printf("[go-ws:%s] Kafka consumer connected (group: %s)", INSTANCE, GROUP_ID)

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
			log.Printf("[go-ws:%s] Kafka consume error: %v, retrying...", INSTANCE, err)
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
			s.clientsMu.RLock()
			cc := len(s.clients)
			s.clientsMu.RUnlock()
			log.Printf("[go-ws:%s] batches=%d in=%d out=%d flushes=%d in/s=%.0f out/s=%.0f clients=%d buffer=%d",
				INSTANCE,
				atomic.LoadInt64(&s.batchCount),
				atomic.LoadInt64(&s.msgsIn),
				atomic.LoadInt64(&s.msgsOut),
				atomic.LoadInt64(&s.flushes),
				float64(atomic.LoadInt64(&s.msgsIn))/elapsed,
				float64(atomic.LoadInt64(&s.msgsOut))/elapsed,
				cc,
				len(s.buffer))
		}
	}
}

func main() {
	s := &Server{
		clients:   make(map[*Client]bool),
		startTime: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go s.startKafkaConsumer(ctx)
	go s.printStats()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", s.handleWS)

	server := &http.Server{Handler: mux}

	addr := ":" + strconv.Itoa(PORT)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("[go-ws:%s] Listen on %s failed: %v", INSTANCE, addr, err)
	}

	go func() {
		log.Printf("[go-ws:%s] Listening on %s", INSTANCE, addr)
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[go-ws:%s] Serve failed: %v", INSTANCE, err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Printf("[go-ws:%s] Shutting down...", INSTANCE)
	s.shutdown.Store(true)
	s.flushToClients()

	s.clientsMu.Lock()
	for c := range s.clients {
		c.close()
	}
	s.clientsMu.Unlock()

	cancel()
	server.Shutdown(context.Background())
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
