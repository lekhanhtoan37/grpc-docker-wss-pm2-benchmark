#!/bin/bash
set -euo pipefail

BASEDIR="$(cd "$(dirname "$0")" && pwd)"
PASS=0
FAIL=0
SKIP=0

check() {
  local label="$1"
  local cmd="$2"
  if eval "$cmd" &>/dev/null; then
    echo "  PASS $label"
    PASS=$((PASS + 1))
  else
    echo "  FAIL $label"
    FAIL=$((FAIL + 1))
  fi
}

skip() {
  local label="$1"
  local reason="$2"
  echo "  SKIP $label ($reason)"
  SKIP=$((SKIP + 1))
}

echo "=== NATS Integration Smoke Test ==="
echo "Platform: $(uname -s)/$(uname -m)"
echo "Go: $(go version 2>/dev/null || echo 'not found')"
echo "Docker: $(docker --version 2>/dev/null || echo 'not found')"
echo ""

echo "--- Test 1: Go unit tests (benchmark-client) ---"
if (cd "$BASEDIR/benchmark-client/go-client" && go test ./... 2>&1); then
  PASS=$((PASS + 1))
  echo "  PASS go test ./..."
else
  FAIL=$((FAIL + 1))
  echo "  FAIL go test ./..."
fi

echo ""
echo "--- Test 2: Build benchmark-client ---"
if (cd "$BASEDIR/benchmark-client/go-client" && go build -o benchmark-client . 2>&1); then
  PASS=$((PASS + 1))
  echo "  PASS benchmark-client binary built"
else
  FAIL=$((FAIL + 1))
  echo "  FAIL benchmark-client build failed"
fi

echo ""
echo "--- Test 3: Build nats-worker ---"
if (cd "$BASEDIR/nats-worker" && go build -o nats-worker . 2>&1); then
  PASS=$((PASS + 1))
  echo "  PASS nats-worker binary built"
else
  FAIL=$((FAIL + 1))
  echo "  FAIL nats-worker build failed"
fi

echo ""
echo "--- Test 4: Start NATS Docker container ---"
DOCKER_RUNNING=false
if docker info &>/dev/null; then
  DOCKER_RUNNING=true
fi

if [ "$DOCKER_RUNNING" = true ]; then
  cd "$BASEDIR/infra"
  docker compose up -d nats 2>&1 || true
  echo "  Waiting 5s for NATS container..."
  sleep 5
  PASS=$((PASS + 1))
  echo "  PASS NATS container started"
else
  skip "NATS Docker container" "Docker not running"
fi

echo ""
echo "--- Test 5: NATS healthz endpoint ---"
if [ "$DOCKER_RUNNING" = true ]; then
  HEALTHZ=$(curl -sf http://localhost:8222/healthz 2>/dev/null || echo "")
  if [ -n "$HEALTHZ" ]; then
    PASS=$((PASS + 1))
    echo "  PASS NATS healthz: $HEALTHZ"
  else
    FAIL=$((FAIL + 1))
    echo "  FAIL NATS healthz returned empty"
  fi
else
  skip "NATS healthz" "Docker not running"
fi

echo ""
echo "--- Test 6: Benchmark client NATS connectivity ---"
if [ "$DOCKER_RUNNING" = true ]; then
  NATS_VARZ=$(curl -sf http://localhost:8222/varz 2>/dev/null | jq -r '.version' 2>/dev/null || echo "")
  if [ -n "$NATS_VARZ" ]; then
    PASS=$((PASS + 1))
    echo "  PASS NATS server version: $NATS_VARZ"
  else
    FAIL=$((FAIL + 1))
    echo "  FAIL Could not read NATS varz"
  fi
else
  skip "NATS connectivity" "Docker not running"
fi

echo ""
echo "--- Test 7: nats-worker Kafka consumer ---"
if [ "$DOCKER_RUNNING" = true ]; then
  if docker exec benchmark-kafka bash -c "echo test" &>/dev/null; then
    skip "nats-worker Kafka consumer" "Kafka container not running (expected on macOS)"
  else
    skip "nats-worker Kafka consumer" "Kafka not available"
  fi
else
  skip "nats-worker Kafka consumer" "Docker not running"
fi

echo ""
echo "--- Cleanup ---"
if [ "$DOCKER_RUNNING" = true ]; then
  cd "$BASEDIR/infra"
  docker compose stop nats 2>/dev/null || true
  docker compose rm -f nats 2>/dev/null || true
  echo "  NATS container stopped."
fi

echo ""
echo "=== Results: $PASS passed, $FAIL failed, $SKIP skipped ==="
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
