#!/bin/bash
set -e

echo "=== 1GB/s Throughput Benchmark ==="

BASEDIR="$(cd "$(dirname "$0")" && pwd)"
WARMUP="${WARMUP:-30}"
DURATION="${DURATION:-120}"
RUNS="${RUNS:-3}"
TARGET_MBPS="${TARGET_MBPS:-1000}"
RESULTS_DIR="$BASEDIR/results"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

mkdir -p "$RESULTS_DIR"

echo ""
echo "--- Step 1: Verify systemd Kafka ---"
if ! systemctl is-active --quiet kafka 2>/dev/null; then
  echo "Kafka not running. Start with: sudo systemctl start kafka"
  exit 1
fi
echo "Kafka systemd service: active"
nc -z localhost 9091 && echo "Port 9091: open" || { echo "Port 9091: CLOSED"; exit 1; }

echo ""
echo "--- Step 2: Verify benchmark topic ---"
/opt/kafka/bin/kafka-topics.sh --describe \
  --topic benchmark-messages \
  --bootstrap-server localhost:9091 2>/dev/null || {
  echo "Topic not found. Creating..."
  bash "$BASEDIR/infra/create-topic.sh"
}

echo ""
echo "--- Step 3: Build gRPC Docker images ---"
cd "$BASEDIR/grpc-server" && docker compose build
cd "$BASEDIR/grpc-server" && docker compose -f docker-compose.host.yml build
cd "$BASEDIR"

echo ""
echo "--- Step 4: Start gRPC servers (bridge) ---"
cd "$BASEDIR/grpc-server" && docker compose up -d
cd "$BASEDIR"
echo "Waiting 10s for gRPC bridge containers..."
sleep 10

echo ""
echo "--- Step 5: Start gRPC servers (host) ---"
cd "$BASEDIR/grpc-server" && docker compose -f docker-compose.host.yml up -d
cd "$BASEDIR"
echo "Waiting 5s for gRPC host containers..."
sleep 5

echo ""
echo "--- Step 6: Start WS servers (PM2) ---"
cd "$BASEDIR/ws-server"
if pm2 describe ws-benchmark &>/dev/null; then
  pm2 restart ws-benchmark
else
  npm install --silent
  pm2 start ecosystem.config.js
fi
cd "$BASEDIR"
echo "Waiting 5s for WS workers..."
sleep 5

echo ""
echo "--- Step 7: Health check ---"
bash "$BASEDIR/health-check-1gb.sh"

echo ""
echo "--- Step 8: Install benchmark client deps ---"
npm install --silent --prefix "$BASEDIR/benchmark-client"

PRODUCER_PID=""

start_producer() {
  echo ""
  echo "--- Starting producer (target: ${TARGET_MBPS} MB/s) ---"
  KAFKA_BROKER=localhost:9091 TARGET_MBPS="$TARGET_MBPS" \
    node "$BASEDIR/producer/producer-rdkafka.js" &
  PRODUCER_PID=$!
  echo "Producer PID: $PRODUCER_PID"
  echo "Waiting 5s for producer to ramp up..."
  sleep 5
}

stop_producer() {
  if [ -n "$PRODUCER_PID" ] && kill -0 "$PRODUCER_PID" 2>/dev/null; then
    echo "Stopping producer (PID: $PRODUCER_PID)..."
    kill "$PRODUCER_PID" 2>/dev/null || true
    wait "$PRODUCER_PID" 2>/dev/null || true
  fi
}

cleanup() {
  stop_producer
  cd "$BASEDIR/grpc-server" && docker compose down 2>/dev/null || true
  cd "$BASEDIR/grpc-server" && docker compose -f docker-compose.host.yml down 2>/dev/null || true
  pm2 stop ws-benchmark 2>/dev/null || true
}
trap cleanup EXIT

echo ""
echo "--- Step 9: Running $RUNS benchmark runs ---"

for run in $(seq 1 "$RUNS"); do
  echo ""
  echo "=== Run $run/$RUNS ($(date)) ==="

  start_producer

  node "$BASEDIR/benchmark-client/client-throughput.js" \
    --warmup "$WARMUP" --duration "$DURATION" \
    2>&1 | tee "$RESULTS_DIR/1gb-run-${run}-${TIMESTAMP}.log"
  echo ""

  stop_producer

  if [ "$run" -lt "$RUNS" ]; then
    echo "Cooling down 15s..."
    sleep 15
  fi
done

echo ""
echo "--- Step 10: Collect system info ---"
{
  echo "=== System Info ==="
  echo "Date: $(date)"
  echo "Kernel: $(uname -a)"
  echo "Node: $(node --version)"
  echo "Docker: $(docker --version)"
  echo "PM2: $(pm2 --version)"
  echo "Kafka: systemd ($(systemctl is-active kafka))"
  echo ""
  echo "=== Kafka Config ==="
  /opt/kafka/bin/kafka-broker-api-versions.sh --bootstrap-server localhost:9091 2>/dev/null | head -5
  echo ""
  echo "=== Topic Info ==="
  /opt/kafka/bin/kafka-topics.sh --describe --topic benchmark-messages --bootstrap-server localhost:9091
  echo ""
  echo "=== PM2 Metrics ==="
  pm2 show ws-benchmark 2>/dev/null
  echo ""
  echo "=== Docker Stats ==="
  docker stats --no-stream grpc-server-1 grpc-server-2 grpc-server-3 grpc-host-1 grpc-host-2 grpc-host-3 2>/dev/null || true
  echo ""
  echo "=== Disk Info ==="
  df -h /var/lib/kafka/data 2>/dev/null || df -h .
} > "$RESULTS_DIR/system-info-${TIMESTAMP}.log"

echo ""
echo "=== Benchmark complete ==="
echo "Results: $RESULTS_DIR/"
echo "Runs: $RUNS × ${DURATION}s measurement"
echo "Producer target: ${TARGET_MBPS} MB/s"
