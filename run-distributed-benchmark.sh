#!/bin/bash
set -euo pipefail

WORKERS="${1:-2}"
WARMUP="${2:-30}"
DURATION="${3:-120}"
CONNS="${4:-1}"
LISTEN="${5:-:50000}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GO_DIR="$SCRIPT_DIR/benchmark-client/go-client"

echo "=== Building binaries ==="
cd "$GO_DIR"
go build -o coordinator ./cmd/coordinator/
go build -o worker ./cmd/worker/
echo "Build complete."

LOGDIR="$SCRIPT_DIR/logs-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$LOGDIR"

echo "=== Starting coordinator (expecting $WORKERS workers, warmup=${WARMUP}s, measure=${DURATION}s, conns=$CONNS) ==="
"$GO_DIR/coordinator" \
  -workers "$WORKERS" \
  -warmup "$WARMUP" \
  -duration "$DURATION" \
  -conns "$CONNS" \
  -listen "$LISTEN" \
  > "$LOGDIR/coordinator.log" 2>&1 &
COORD_PID=$!
echo "Coordinator PID=$COORD_PID"

sleep 2

for i in $(seq 1 "$WORKERS"); do
  echo "=== Starting worker $i ==="
  "$GO_DIR/worker" \
    -coordinator "localhost${LISTEN#:}" \
    -worker-id "worker-$i" \
    > "$LOGDIR/worker-$i.log" 2>&1 &
done

echo "=== Waiting for coordinator to finish ==="
wait "$COORD_PID" 2>/dev/null || true
echo "Coordinator exited."

echo "=== Waiting for workers ==="
wait 2>/dev/null || true

echo "=== Done. Logs in $LOGDIR ==="
echo "=== Coordinator log: ==="
cat "$LOGDIR/coordinator.log"
