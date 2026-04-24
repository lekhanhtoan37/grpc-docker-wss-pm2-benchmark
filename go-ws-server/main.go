package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/segmentio/kafka-go"
)

var (
	PORT      = envStr("PORT", "8092")
	BROKER    = envStr("KAFKA_BROKER", "192.168.0.9:9091")
	TOPIC     = envStr("KAFKA_TOPIC", "benchmark-messages")
	INSTANCE  = envStr("NODE_APP_INSTANCE", envStr("CONTAINER_ID", envStr("HOSTNAME", strconv.Itoa(os.Getpid()))))
	GROUP_ID  = "go-ws-benchmark-worker-" + INSTANCE
	BATCH_MAX = envInt("BATCH_MAX", 20)
	LINGER_MS = envInt("LINGER_MS", 5)
)

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

type Server struct {
	clients    map[*websocket.Conn]bool
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
	payload := strings.Join(entries, "\n")
	data := []byte(payload)

	s.clientsMu.Lock()
	var dead []*websocket.Conn
	for ws := range s.clients {
		err := ws.WriteMessage(websocket.TextMessage, data)
		if err != nil {
			dead = append(dead, ws)
		}
	}
	for _, ws := range dead {
		delete(s.clients, ws)
		ws.Close()
	}
	s.clientsMu.Unlock()

	s.flushMu.Lock()
	s.flushing = false
	s.flushMu.Unlock()
}

func (s *Server) appendToBuffer(msg string) {
	s.flushMu.Lock()
	s.buffer = append(s.buffer, msg)
	atomic.AddInt64(&s.msgsIn, 1)
	atomic.AddInt64(&s.batchCount, 1)

	if len(s.buffer) >= BATCH_MAX {
		s.flushMu.Unlock()
		s.flushToClients()
		return
	}
	if s.timer == nil && !s.flushing {
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
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.clientsMu.Lock()
	s.clients[ws] = true
	count := len(s.clients)
	s.clientsMu.Unlock()
	log.Printf("[go-ws:%s] conn connected (total: %d)", INSTANCE, count)

	for {
		_, _, err := ws.ReadMessage()
		if err != nil {
			s.clientsMu.Lock()
			delete(s.clients, ws)
			count = len(s.clients)
			s.clientsMu.Unlock()
			ws.Close()
			log.Printf("[go-ws:%s] conn disconnected (total: %d)", INSTANCE, count)
			break
		}
	}
}

func (s *Server) startKafkaConsumer(ctx context.Context) {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:          []string{BROKER},
		Topic:            TOPIC,
		GroupID:          GROUP_ID,
		MinBytes:         1,
		MaxBytes:         50 * 1024 * 1024,
		MaxWait:          500 * time.Millisecond,
		SessionTimeout:   30 * time.Second,
		RebalanceTimeout: 30 * time.Second,
	})
	defer r.Close()

	log.Printf("[go-ws:%s] Kafka consumer connected (group: %s)", INSTANCE, GROUP_ID)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		m, err := r.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[go-ws:%s] Kafka read error: %v, retrying...", INSTANCE, err)
			time.Sleep(5 * time.Second)
			continue
		}
		if s.shutdown.Load() {
			return
		}
		s.appendToBuffer(string(m.Value))
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
		clients:   make(map[*websocket.Conn]bool),
		startTime: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go s.startKafkaConsumer(ctx)
	go s.printStats()

	http.HandleFunc("/ws", s.handleWS)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})

	server := &http.Server{Addr: ":" + PORT}

	go func() {
		log.Printf("[go-ws:%s] Listening on :%s", INSTANCE, PORT)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[go-ws:%s] Listen failed: %v", INSTANCE, err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Printf("[go-ws:%s] Shutting down...", INSTANCE)
	s.shutdown.Store(true)
	s.flushToClients()

	s.clientsMu.Lock()
	for ws := range s.clients {
		ws.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(1001, "server shutdown"))
		ws.Close()
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
