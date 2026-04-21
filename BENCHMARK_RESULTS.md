# Benchmark Results

## Linger.ms Micro-Batching (LINGER_MS=5, 30 connections)

| Group | Msgs | MB/s | msg/s | vs WS |
|-------|------|------|-------|-------|
| WS (host/PM2) | 17.2M | 141.51 | 143,530 | baseline |
| gRPC bridge | 91.0M | 760.03 | 758,355 | **+437%** |
| gRPC host | 91.1M | 761.02 | 759,366 | **+438%** |

### Key Finding

**gRPC với linger.ms micro-batching (5ms) cho throughput cao hơn WS ~5.4 lần.**

Cơ chế: Kafka `linger.ms` pattern — accumulate messages trong buffer, flush khi timer hết hoặc buffer đầy (BATCH_MAX=500). Gom nhiều Kafka batches thành 1 `call.write()` duy nhất → giảm HTTP/2 frame overhead.

### Linger.ms Tuning Guide

| LINGER_MS | Throughput | Latency thêm | Use case |
|-----------|-----------|-------------|----------|
| 0 | Thấp nhất | 0ms | Realtime tuyệt đối |
| 2 | Cao | +2ms | Trading, live feed |
| **5** | **Rất cao** | **+5ms** | **Chat, notifications, dashboard** |
| 10 | Cao nhất | +10ms | Bulk sync, data pipeline |

### Raw vs Processed Drop Explanation

"Drop %" trong benchmark KHÔNG phải message loss thật. Nguyên nhân:

- `rawCount` bắt đầu đếm từ lúc connection established (trước warmup)
- `count` (processed) chỉ đếm trong measurement phase
- Warmup=30s + Measure=120s → raw bao gồm ~150s, processed chỉ 120s
- Tỷ lệ: 120/150 = 80% → "drop" ≈ 20% là artifact, KHÔNG phải data loss

### Test Config

- Connections: 30/group (round-robin across 3 endpoints)
- Warmup: 30s, Measurement: 120s
- Producers: 10 × ~72 MB/s = ~720 MB/s total
- JSON.parse: enabled on both WS and gRPC servers
- Batched proto: `repeated MessageEntry messages = 1`
- gRPC HTTP/2 window: 640MB/1.28GB
- gRPC server drain handling: no timeout (streams stay alive)
