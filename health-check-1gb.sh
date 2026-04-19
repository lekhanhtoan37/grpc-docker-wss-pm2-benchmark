#!/bin/bash
set -euo pipefail

echo "=== 1GB/s Benchmark Health Check ==="
echo ""

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
check "Kafka port 127.0.0.1:9091" "nc -z 127.0.0.1 9091"
check "Kafka controller 127.0.0.1:9093" "nc -z 127.0.0.1 9093"
check "Topic benchmark-messages" "/opt/kafka-benchmark/bin/kafka-topics.sh --describe --topic benchmark-messages --bootstrap-server 127.0.0.1:9091"

echo ""
echo "--- Docker bridge → Kafka connectivity ---"
check "route_localnet enabled" "sysctl net.ipv4.conf.all.route_localnet | grep '= 1'"
check "DNAT rule exists" "iptables -t nat -L PREROUTING -n | grep 9091"
check "Bridge container → Kafka" "docker exec grpc-server-1 node -e \"const net=require('net');const s=net.createConnection(9091,'192.168.0.5',()=>process.exit(0));s.on('error',()=>process.exit(1));setTimeout(()=>process.exit(1),3000)\""

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
check "PM2 ws-benchmark" "pm2 describe ws-benchmark"

echo ""
echo "--- WS Connectivity ---"
check "WS :8090" "cd '$(dirname "$0")/ws-server' && node -e \"const ws=new(require('ws'))('ws://127.0.0.1:8090');ws.on('open',()=>{process.exit(0)});setTimeout(()=>process.exit(1),3000)\""

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
