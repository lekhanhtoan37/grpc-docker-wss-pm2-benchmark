#!/bin/bash
set -euo pipefail

echo "=== 1GB/s Benchmark Health Check ==="
echo ""

BASEDIR="$(cd "$(dirname "$0")" && pwd)"
PM2_USER="${SUDO_USER:-$(whoami)}"
PM2_HOME_USER="$(getent passwd "$PM2_USER" | cut -d: -f6)"
NVM_DIR="${PM2_HOME_USER}/.nvm"
NODE_PATH=""
if [ -d "$NVM_DIR/versions/node" ]; then
  NODE_PATH="$(ls -td "$NVM_DIR"/versions/node/*/bin 2>/dev/null | head -1)"
fi
RESOLVED_PATH="${NODE_PATH:+$NODE_PATH:}${PATH}"

run_as_user() {
  sudo -u "$PM2_USER" env PATH="${RESOLVED_PATH}" PM2_HOME="${PM2_HOME_USER}/.pm2" "$@"
}

PASS=0
FAIL=0

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

echo "--- Systemd Kafka Benchmark ---"
check "Kafka benchmark service active" "systemctl is-active kafka-benchmark"
check "Kafka port 192.168.0.9:9091" "nc -z 192.168.0.9 9091"
check "Kafka controller 127.0.0.1:9093" "nc -z 127.0.0.1 9093"
check "Topic benchmark-messages" "/opt/kafka-benchmark/bin/kafka-topics.sh --describe --topic benchmark-messages --bootstrap-server 192.168.0.9:9091"

echo ""
echo "--- Docker bridge → Kafka connectivity ---"
HOST_IP=192.168.0.9
check "iptables allow Docker→Kafka" "iptables -L INPUT -n | grep 9091"
check "Bridge container → Kafka (${HOST_IP}:9091)" "docker exec grpc-server-1 node -e \"const net=require('net');const s=net.createConnection(9091,'${HOST_IP}',()=>process.exit(0));s.on('error',()=>process.exit(1));setTimeout(()=>process.exit(1),3000)\""

echo ""
echo "--- gRPC Servers (bridge) ---"
for port in 50051 50052 50053; do
  check "gRPC bridge :$port" "nc -z 127.0.0.1 $port"
done

echo ""
echo "--- gRPC Host-Networked Servers ---"
for port in 60051 60052 60053; do
  check "gRPC host :$port" "nc -z 127.0.0.1 $port"
done

echo ""
echo "--- PM2 WS ---"
check "PM2 ws-benchmark" "run_as_user npx pm2 describe ws-benchmark"

echo ""
echo "--- WS Connectivity ---"
if PATH="$RESOLVED_PATH" timeout 3 node -e "const net=require('net');const s=net.createConnection(8090,'127.0.0.1',()=>process.exit(0));s.on('error',()=>process.exit(1));setTimeout(()=>process.exit(1),3000)" &>/dev/null; then
  echo "  PASS WS :8090"
  PASS=$((PASS + 1))
else
  echo "  FAIL WS :8090"
  FAIL=$((FAIL + 1))
fi

echo ""
echo "--- Docker ---"
check "Docker running" "docker info"
check "grpc-server-1 container" "docker ps | grep grpc-server-1"
check "grpc-host-1 container" "docker ps | grep grpc-host-1"

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
  echo "Fix failed checks before running benchmark."
  exit 1
fi
