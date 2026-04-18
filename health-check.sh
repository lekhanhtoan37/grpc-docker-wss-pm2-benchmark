#!/bin/bash
set -e

echo "=== WS vs gRPC Latency Benchmark - Health Check ==="
echo ""

KAFKA_BROKER="${KAFKA_BROKER:-localhost:9092}"
PASS=0
FAIL=0

check() {
  local label="$1"
  local cmd="$2"
  if eval "$cmd" &>/dev/null; then
    echo "  ✓ $label"
    PASS=$((PASS + 1))
  else
    echo "  ✗ $label"
    FAIL=$((FAIL + 1))
  fi
}

echo "--- Docker ---"
check "Docker running" "docker info"

echo ""
echo "--- Kafka ---"
check "Kafka container" "docker ps | grep benchmark-kafka"
check "Zookeeper container" "docker ps | grep benchmark-zookeeper"

echo ""
echo "--- Kafka Topic ---"
check "Topic benchmark-messages" "docker exec benchmark-kafka kafka-topics --describe --topic benchmark-messages --bootstrap-server $KAFKA_BROKER"

echo ""
echo "--- gRPC Servers ---"
for port in 50051 50052 50053; do
  check "gRPC :$port" "nc -z localhost $port"
done

echo ""
echo "--- PM2 WS ---"
check "PM2 ws-benchmark" "pm2 describe ws-benchmark"

echo ""
echo "--- WS Connectivity ---"
check "WS :8080" "cd '$(dirname "$0")/ws-server' && node -e \"const ws=new(require('ws'))('ws://localhost:8080');ws.on('open',()=>{process.exit(0)});setTimeout(()=>process.exit(1),3000)\""

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
  echo "Fix failed checks before running benchmark."
  exit 1
fi
