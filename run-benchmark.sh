#!/bin/bash
set -e

echo "=== Starting WS vs gRPC Benchmark ==="

BASEDIR="$(cd "$(dirname "$0")" && pwd)"
KAFKA_BROKER="${KAFKA_BROKER:-localhost:9092}"
WARMUP="${WARMUP:-60}"
DURATION="${DURATION:-300}"
RUNS="${RUNS:-3}"
RESULTS_DIR="$BASEDIR/results"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

mkdir -p "$RESULTS_DIR"

echo ""
echo "--- Step 1: Starting Kafka ---"
if ! docker ps | grep -q benchmark-kafka; then
  cd "$BASEDIR/infra" && docker compose up -d
  echo "Waiting 15s for Kafka startup..."
  sleep 15
fi

echo ""
echo "--- Step 2: Creating topic ---"
docker exec benchmark-kafka kafka-topics --create \
  --topic benchmark-messages \
  --partitions 1 \
  --replication-factor 1 \
  --if-not-exists \
  --bootstrap-server localhost:9092
docker exec benchmark-kafka kafka-topics --describe \
  --topic benchmark-messages \
  --bootstrap-server localhost:9092

echo ""
echo "--- Step 2b: Installing producer deps ---"
cd "$BASEDIR/producer"
npm install --silent

echo ""
echo "--- Step 3: Starting gRPC servers ---"
cd "$BASEDIR/grpc-server"
docker compose up -d --build
echo "Waiting 10s for gRPC containers..."
sleep 10

echo ""
echo "--- Step 4: Starting WS servers ---"
cd "$BASEDIR/ws-server"
if ! pm2 describe ws-benchmark &>/dev/null; then
  npm install --silent
  pm2 start ecosystem.config.js
  echo "Waiting 5s for WS workers..."
  sleep 5
fi

echo ""
echo "--- Step 5: Installing benchmark client deps ---"
cd "$BASEDIR/benchmark-client"
npm install --silent

echo ""
echo "--- Step 6: Running health check ---"
cd "$BASEDIR"
bash health-check.sh

PRODUCER_PID=""

start_producer() {
  echo ""
  echo "--- Starting producer (background) ---"
  KAFKAJS_NO_PARTITIONER_WARNING=1 node "$BASEDIR/producer/producer.js" &
  PRODUCER_PID=$!
  echo "Producer PID: $PRODUCER_PID"
  sleep 3
}

stop_producer() {
  if [ -n "$PRODUCER_PID" ] && kill -0 "$PRODUCER_PID" 2>/dev/null; then
    echo "Stopping producer (PID: $PRODUCER_PID)..."
    kill "$PRODUCER_PID" 2>/dev/null || true
    wait "$PRODUCER_PID" 2>/dev/null || true
  fi
}

trap stop_producer EXIT

echo ""
echo "--- Step 7: Running $RUNS benchmark runs ---"

for run in $(seq 1 "$RUNS"); do
  echo ""
  echo "=== Run $run/$RUNS ($(date)) ==="

  start_producer

  node "$BASEDIR/benchmark-client/client.js" --warmup "$WARMUP" --duration "$DURATION" 2>&1 | tee "$RESULTS_DIR/run-${run}-${TIMESTAMP}.log"
  echo ""

  stop_producer

  if [ "$run" -lt "$RUNS" ]; then
    echo "Cooling down 10s before next run..."
    sleep 10
  fi
done

echo ""
echo "--- Step 8: Collecting system info ---"
{
  echo "=== System Info ==="
  echo "Date: $(date)"
  echo "Kernel: $(uname -a)"
  echo "Node: $(node --version)"
  echo "Docker: $(docker --version)"
  echo "PM2: $(pm2 --version)"
  echo ""
  echo "=== PM2 Metrics ==="
  pm2 show ws-benchmark
  echo ""
  echo "=== Docker Stats ==="
  docker stats --no-stream grpc-server-1 grpc-server-2 grpc-server-3 2>/dev/null || true
} > "$RESULTS_DIR/system-info-${TIMESTAMP}.log"

echo ""
echo "=== Benchmark complete ==="
echo "Results saved to $RESULTS_DIR/"
