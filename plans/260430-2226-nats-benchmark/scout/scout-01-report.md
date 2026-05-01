# Scout Report — Codebase Structure

## Relevant Files

### Go WS Server (reference for NATS worker)
- `go-ws-server/main.go` — Kafka consumer + WS server. IBM/sarama, linger-batch (BATCH_MAX=20, LINGER_MS=5ms). Messages joined with `\n`, sent as single WS frame.
- `go-ws-server/go.mod` — Go 1.22, IBM/sarama v1.45.1, nhooyr.io/websocket
- `go-ws-server/Dockerfile` — Multi-stage golang:1.23 build
- `go-ws-server/docker-compose.yml` — Bridge: 3 replicas + nginx
- `go-ws-server/docker-compose.host.yml` — Host: 3 containers, ports 60071-73
- `go-ws-server/nginx.conf` — WS reverse proxy
- `go-ws-server/ecosystem.config.js` — PM2 fork mode, 3 instances

### Benchmark Client
- `benchmark-client/go-client/main.go` — 9 groups, single-process mode
- `benchmark-client/go-client/cmd/coordinator/main.go` — Distributed coordinator
- `benchmark-client/go-client/cmd/worker/main.go` — Distributed worker
- `benchmark-client/go-client/internal/worker/ws.go` — WS client, ReadFrameReusable, ExtractTimestampInt64
- `benchmark-client/go-client/internal/worker/grpc.go` — gRPC client, StreamMessages RPC
- `benchmark-client/go-client/internal/worker/pool.go` — sync.Pool for latency slices
- `benchmark-client/go-client/internal/worker/frame.go` — Fast timestamp extraction from JSON
- `benchmark-client/go-client/internal/worker/ws_stats.go` — Stats flusher goroutine (200ms / 32K msgs / 8MB)
- `benchmark-client/go-client/internal/stats/stats.go` — ConnStats (Count, Bytes, RawCount, RawBytes, Hist)
- `benchmark-client/go-client/internal/stats/histogram.go` — HDR histogram merge
- `benchmark-client/go-client/internal/stats/serialize.go` — HDR histogram serialization
- `benchmark-client/go-client/internal/report/report.go` — Throughput/latency report tables
- `benchmark-client/go-client/internal/coordinator/aggregator.go` — WorkerResult aggregation
- `benchmark-client/go-client/internal/coordinator/phase.go` — Warmup/measure/stop phases
- `benchmark-client/go-client/internal/coordinator/server.go` — gRPC coordinator server
- `benchmark-client/go-client/internal/coordinator/shard.go` — Group sharding across workers

### Proto Files
- `proto/benchmark.proto` — MessageEntry (timestamp, seq, payload), StreamResponse (repeated)
- `grpc-server/proto/benchmark.proto` — Same
- `benchmark-client/go-client/proto/benchmark.proto` — Client mirror
- `benchmark-client/go-client/proto/control.proto` — Distributed control protocol

### gRPC Server (reference for Kafka→protocol bridge)
- `grpc-server/server.js` — KafkaJS consumer → gRPC StreamResponse protobuf. Same linger-batch pattern.

### Infrastructure
- `docker-compose.benchmark.yml` — Distributed coordinator + worker setup
- `run-benchmark-1gb.sh` — Full orchestration (Kafka, Docker, PM2, producers, benchmark)
- `run-benchmark.sh` — Simpler orchestration
- `run-distributed-benchmark.sh` — Distributed mode orchestration

### Producer
- `producer/producer-rdkafka.js` — node-rdkafka, 1KB JSON msgs, timestamp+seq+padding

## Key Patterns to Follow
1. **Kafka consumer groups**: Each worker instance = unique group ID → receives ALL messages
2. **Linger batching**: BATCH_MAX=20 msgs or LINGER_MS=5ms → flush buffer
3. **Latency**: Producer stamps `timestamp` (ms), client computes `nowMicros - timestamp*1000`
4. **Stats**: hdrhistogram, ConnStats, per-group aggregation, percentile tables
5. **Deployment**: Bridge (3 replicas + nginx) + Host (3 containers) + PM2 fork
6. **Docker**: Multi-stage Go build, alpine nginx reverse proxy
