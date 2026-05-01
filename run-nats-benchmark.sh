#!/bin/bash
set -e

BASEDIR="$(cd "$(dirname "$0")" && pwd)"
NATS_URL="${NATS_URL:-nats://localhost:4222}"
WARMUP="${WARMUP:-30}"
DURATION="${DURATION:-120}"

if ! curl -sf http://localhost:8222/healthz > /dev/null 2>&1; then
  echo "Starting NATS..."
  cd "$BASEDIR/infra" && docker compose up -d nats
  sleep 5
fi

echo "=== Running Standalone NATS Benchmark ==="
cd "$BASEDIR/nats-bench"
go run main.go \
  -nats-url "$NATS_URL" \
  -subject "bench.test" \
  -mode both \
  -warmup "$WARMUP" \
  -duration "$DURATION" \
  -msg-size 1024
