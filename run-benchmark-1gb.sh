#!/bin/bash
set -euo pipefail

echo "=== 1GB/s Throughput Benchmark ==="

# Resolve node/npm/pm2 PATH when running via sudo
PM2_USER="${SUDO_USER:-$(whoami)}"
PM2_HOME_USER="$(getent passwd "$PM2_USER" | cut -d: -f6)"
if [ "$(id -u)" -eq 0 ] && [ -n "${SUDO_USER:-}" ]; then
  export NVM_DIR="${PM2_HOME_USER}/.nvm"
  [ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh"
  NVM_BIN="$(ls -d "${PM2_HOME_USER}/.nvm/versions/node/"*/bin 2>/dev/null | tail -1)"
  for p in "${PM2_HOME_USER}/.local/bin" "$NVM_BIN" "/usr/local/bin" "/usr/local/go/bin"; do
    [ -d "$p" ] && export PATH="$p:$PATH"
  done
fi

[ -d "/usr/local/go/bin" ] && export PATH="/usr/local/go/bin:$PATH"

command -v node >/dev/null || { echo "ERROR: node not found. Install Node.js first."; exit 1; }
command -v npm >/dev/null || { echo "ERROR: npm not found. Install Node.js first."; exit 1; }

run_as_user() {
  sudo -u "$PM2_USER" env PATH="$PATH" PM2_HOME="${PM2_HOME_USER}/.pm2" "$@"
}

run_pm2() {
  run_as_user pm2 "$@"
}

BASEDIR="$(cd "$(dirname "$0")" && pwd)"
WARMUP="${WARMUP:-30}"
DURATION="${DURATION:-120}"
RUNS="${RUNS:-3}"
TARGET_MBPS="${TARGET_MBPS:-1000}"
NUM_PRODUCERS="${NUM_PRODUCERS:-10}"
CONNS="${CONNS:-30}"
RESULTS_DIR="$BASEDIR/results"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

KAFKA_VERSION="3.9.2"
SCALA_VERSION="2.13"
KAFKA_DIR="/opt/kafka-benchmark"
KAFKA_DATA="/home/kafka-benchmark/data"
KAFKA_USER="kafka-bench"
KAFKA_SERVICE="kafka-benchmark"
KAFKA_PORT=9091
KAFKA_CONTROLLER_PORT=9093

mkdir -p "$RESULTS_DIR"

# ──────────────────────────────────────────────
# Step 1: Setup Kafka (idempotent)
# ──────────────────────────────────────────────
echo ""
echo "--- Step 1: Kafka benchmark setup ---"

if systemctl is-active --quiet "$KAFKA_SERVICE" 2>/dev/null && nc -z 192.168.0.5 "$KAFKA_PORT" 2>/dev/null; then
  echo "Kafka benchmark already running on 192.168.0.5:$KAFKA_PORT. Skipping setup."
else
  echo "Kafka benchmark not running. Setting up..."

  # Java 17+
  if ! java -version 2>&1 | grep -q "17\|21"; then
    echo "Installing OpenJDK 17..."
    if command -v apt &>/dev/null; then
      sudo apt update && sudo apt install -y openjdk-17-jdk-headless
    elif command -v dnf &>/dev/null; then
      sudo dnf install -y java-17-openjdk-devel
    else
      echo "ERROR: Unsupported package manager. Install Java 17+ manually."
      exit 1
    fi
  fi

  # Download + install Kafka
  if [ ! -d "${KAFKA_DIR}" ]; then
    echo "Downloading Kafka ${KAFKA_VERSION}..."
    local_tar="kafka_${SCALA_VERSION}-${KAFKA_VERSION}.tgz"
    local_url="https://downloads.apache.org/kafka/${KAFKA_VERSION}/${local_tar}"
    curl -fSL -o "${local_tar}" "${local_url}"
    file_size=$(stat -c%s "${local_tar}" 2>/dev/null || stat -f%z "${local_tar}" 2>/dev/null || echo 0)
    if [ "$file_size" -lt 1000000 ]; then
      echo "ERROR: Downloaded file too small (${file_size} bytes). URL may be wrong."
      echo "URL: ${local_url}"
      echo "Try available versions: https://downloads.apache.org/kafka/"
      rm -f "${local_tar}"
      exit 1
    fi
    sudo tar -xzf "${local_tar}" -C /opt/
    sudo ln -s "/opt/kafka_${SCALA_VERSION}-${KAFKA_VERSION}" "${KAFKA_DIR}"
    rm -f "${local_tar}"
    echo "Kafka extracted to ${KAFKA_DIR}"
  else
    echo "Kafka dir ${KAFKA_DIR} exists. Skipping download."
  fi

  # Create user
  if ! id "${KAFKA_USER}" &>/dev/null; then
    sudo useradd -r -s /sbin/nologin "${KAFKA_USER}"
    echo "User ${KAFKA_USER} created."
  fi

  # Data dir
  sudo mkdir -p "${KAFKA_DATA}"
  KAFKA_REAL_DIR="$(readlink -f "${KAFKA_DIR}")"
  sudo chown -R "${KAFKA_USER}:${KAFKA_USER}" "${KAFKA_REAL_DIR}"
  sudo chown -R "${KAFKA_USER}:${KAFKA_USER}" "${KAFKA_DATA}"

  echo "Writing server.properties (host: 192.168.0.5)..."
  sudo tee "${KAFKA_DIR}/config/kraft/server.properties" > /dev/null <<PROPS
node.id=1
process.roles=broker,controller
listeners=PLAINTEXT://192.168.0.5:9091,CONTROLLER://127.0.0.1:9093
advertised.listeners=PLAINTEXT://192.168.0.5:9091
controller.listener.names=CONTROLLER
listener.security.protocol.map=PLAINTEXT:PLAINTEXT,CONTROLLER:PLAINTEXT
controller.quorum.voters=1@127.0.0.1:9093
log.dirs=/home/kafka-benchmark/data

socket.send.buffer.bytes=1048576
socket.receive.buffer.bytes=1048576
socket.request.max.bytes=104857600

log.segment.bytes=104857600
num.partitions=12
log.retention.ms=120000
log.retention.bytes=1073741824
log.cleanup.policy=delete
log.cleanup.interval.ms=10000

num.network.threads=8
num.io.threads=8
num.recovery.threads.per.data.dir=2

delete.topic.enable=true
auto.create.topics.enable=false

group.coordinator.new.enable=false
offsets.topic.replication.factor=1
PROPS

  # Format KRaft storage (skip if already formatted)
  if [ ! -f "${KAFKA_DATA}/meta.properties" ]; then
    echo "Formatting KRaft storage..."
    KAFKA_CLUSTER_ID=$(sudo -u "${KAFKA_USER}" "${KAFKA_DIR}/bin/kafka-storage.sh" random-uuid)
    echo "Cluster ID: ${KAFKA_CLUSTER_ID}"
    sudo -u "${KAFKA_USER}" "${KAFKA_DIR}/bin/kafka-storage.sh" format \
      -t "$KAFKA_CLUSTER_ID" \
      -c "${KAFKA_DIR}/config/kraft/server.properties"
  else
    echo "KRaft storage already formatted. Skipping."
  fi

  # Install systemd service (skip if already exists)
  if [ ! -f "/etc/systemd/system/${KAFKA_SERVICE}.service" ]; then
    echo "Installing systemd service..."
    sudo tee "/etc/systemd/system/${KAFKA_SERVICE}.service" > /dev/null <<SVC
[Unit]
Description=Benchmark Kafka Broker (KRaft Mode)
Documentation=https://kafka.apache.org/documentation/
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${KAFKA_USER}
Group=${KAFKA_USER}
Environment="KAFKA_HEAP_OPTS=-Xmx4G -Xms4G"
Environment="KAFKA_JVM_PERFORMANCE_OPTS=-server -XX:+UseG1GC -XX:MaxGCPauseMillis=20"
ExecStart=${KAFKA_DIR}/bin/kafka-server-start.sh ${KAFKA_DIR}/config/kraft/server.properties
ExecStop=${KAFKA_DIR}/bin/kafka-server-stop.sh
Restart=on-failure
RestartSec=10
TimeoutStopSec=120
LimitNOFILE=100000

[Install]
WantedBy=multi-user.target
SVC
    sudo systemctl daemon-reload
    sudo systemctl enable "$KAFKA_SERVICE"
  else
    echo "Systemd service already installed."
    sudo systemctl daemon-reload
  fi

  # Start Kafka
  echo "Starting ${KAFKA_SERVICE}..."
  sudo systemctl restart "$KAFKA_SERVICE"

  echo "Waiting 10s for Kafka startup..."
  sleep 10

  # Verify
  if systemctl is-active --quiet "$KAFKA_SERVICE" 2>/dev/null; then
    echo "Kafka benchmark: active"
  else
    echo "ERROR: Kafka benchmark failed to start."
    echo "Check: sudo journalctl -u ${KAFKA_SERVICE} -n 50"
    exit 1
  fi
fi

# ──────────────────────────────────────────────
# Step 1b: Always update config + restart
# ──────────────────────────────────────────────
echo ""
echo "--- Updating server.properties (host: 192.168.0.5) ---"
sudo tee "${KAFKA_DIR}/config/kraft/server.properties" > /dev/null <<PROPS
node.id=1
process.roles=broker,controller
listeners=PLAINTEXT://192.168.0.5:9091,CONTROLLER://127.0.0.1:9093
advertised.listeners=PLAINTEXT://192.168.0.5:9091
controller.listener.names=CONTROLLER
listener.security.protocol.map=PLAINTEXT:PLAINTEXT,CONTROLLER:PLAINTEXT
controller.quorum.voters=1@127.0.0.1:9093
log.dirs=/home/kafka-benchmark/data

socket.send.buffer.bytes=1048576
socket.receive.buffer.bytes=1048576
socket.request.max.bytes=104857600

log.segment.bytes=104857600
num.partitions=12
log.retention.ms=120000
log.retention.bytes=1073741824
log.cleanup.policy=delete
log.cleanup.interval.ms=10000

num.network.threads=8
num.io.threads=8
num.recovery.threads.per.data.dir=2

delete.topic.enable=true
auto.create.topics.enable=false

group.coordinator.new.enable=false
offsets.topic.replication.factor=1
PROPS

echo "Stopping Kafka and cleaning KRaft data..."
sudo systemctl stop "$KAFKA_SERVICE" 2>/dev/null || true
sleep 2
sudo rm -rf "${KAFKA_DATA}"/*
sudo chown "${KAFKA_USER}:${KAFKA_USER}" "${KAFKA_DATA}"

echo "Formatting KRaft storage..."
KAFKA_CLUSTER_ID=$(sudo -u "${KAFKA_USER}" "${KAFKA_DIR}/bin/kafka-storage.sh" random-uuid)
echo "Cluster ID: ${KAFKA_CLUSTER_ID}"
sudo -u "${KAFKA_USER}" "${KAFKA_DIR}/bin/kafka-storage.sh" format \
  -t "$KAFKA_CLUSTER_ID" \
  -c "${KAFKA_DIR}/config/kraft/server.properties"

echo "Starting ${KAFKA_SERVICE}..."
sudo systemctl start "$KAFKA_SERVICE"
echo "Waiting 15s for Kafka startup..."
sleep 15

if systemctl is-active --quiet "$KAFKA_SERVICE" 2>/dev/null; then
  echo "Kafka benchmark: active"
else
  echo "ERROR: Kafka benchmark failed to start."
  echo "Check: sudo journalctl -u ${KAFKA_SERVICE} -n 50"
  exit 1
fi

echo "Listeners:"
sudo netstat -ntpl 2>/dev/null | grep 9091 || ss -ntpl | grep 9091

# Verify port
if nc -z 192.168.0.5 "$KAFKA_PORT" 2>/dev/null; then
  echo "Port 192.168.0.5:${KAFKA_PORT}: open"
else
  echo "ERROR: Port 192.168.0.5:${KAFKA_PORT} not open."
  exit 1
fi

# ──────────────────────────────────────────────
# Step 2: Create topic (idempotent)
# ──────────────────────────────────────────────
echo ""
echo "--- Step 2: Verify benchmark topic ---"
if "${KAFKA_DIR}/bin/kafka-topics.sh" --describe \
    --topic benchmark-messages \
    --bootstrap-server "192.168.0.5:${KAFKA_PORT}" 2>/dev/null; then
  echo "Topic benchmark-messages exists."
else
  echo "Creating topic benchmark-messages (12 partitions)..."
  "${KAFKA_DIR}/bin/kafka-topics.sh" --create \
    --topic benchmark-messages \
    --bootstrap-server "192.168.0.5:${KAFKA_PORT}" \
    --partitions 12 \
    --replication-factor 1 \
    --config retention.ms=120000 \
    --config segment.bytes=104857600 \
    --config retention.bytes=1073741824 \
    --config max.message.bytes=10485760 \
    --config min.insync.replicas=1 \
    --if-not-exists
  "${KAFKA_DIR}/bin/kafka-topics.sh" --describe \
    --topic benchmark-messages \
    --bootstrap-server "192.168.0.5:${KAFKA_PORT}"
fi

# ──────────────────────────────────────────────
# Step 3: iptables DNAT for Docker bridge → Kafka
# ──────────────────────────────────────────────
# Step 3: Cleanup + iptables for Docker → Kafka
# ──────────────────────────────────────────────
echo ""
echo "--- Step 3: Setup iptables ---"

# Remove old rules
sudo iptables -D INPUT -p tcp --dport 9091 -j ACCEPT 2>/dev/null || true
sudo iptables -D INPUT -s 172.16.0.0/12 -d 192.168.0.5 -p tcp --dport 9091 -j ACCEPT 2>/dev/null || true

# Allow Docker containers → Kafka on host IP
sudo iptables -I INPUT 1 -s 172.16.0.0/12 -d 192.168.0.5 -p tcp --dport 9091 -j ACCEPT
echo "iptables: allow 172.16.0.0/12 → 192.168.0.5:9091"

# ──────────────────────────────────────────────
# Step 4: Build + start gRPC Docker
# ──────────────────────────────────────────────
echo ""
echo "--- Step 4: Build + start gRPC servers ---"
cd "$BASEDIR/grpc-server"
docker compose down 2>/dev/null || true
docker compose -f docker-compose.host.yml down 2>/dev/null || true
docker compose build --no-cache
docker compose -f docker-compose.host.yml build --no-cache
docker compose up -d
cd "$BASEDIR"
echo "Waiting 10s for gRPC bridge containers..."
sleep 10

cd "$BASEDIR/grpc-server"
docker compose -f docker-compose.host.yml up -d
cd "$BASEDIR"
echo "Waiting 5s for gRPC host containers..."
sleep 5

echo ""
echo "--- Step 4b: Build + start uWS servers (Docker) ---"
cd "$BASEDIR/uws-server"
run_as_user npm install --silent
docker compose down 2>/dev/null || true
docker compose -f docker-compose.host.yml down 2>/dev/null || true
docker compose build --no-cache
docker compose -f docker-compose.host.yml build --no-cache
docker compose up -d
cd "$BASEDIR"
echo "Waiting 10s for uWS bridge containers..."
sleep 10

cd "$BASEDIR/uws-server"
docker compose -f docker-compose.host.yml up -d
cd "$BASEDIR"
echo "Waiting 5s for uWS host containers..."
sleep 5

# ──────────────────────────────────────────────
# Step 4: Start WS servers (PM2)
# ──────────────────────────────────────────────
echo ""
echo "--- Step 4: Start WS servers (PM2) ---"
cd "$BASEDIR/ws-server"
run_as_user npm install --silent
run_pm2 describe ws-benchmark &>/dev/null && run_pm2 delete ws-benchmark 2>/dev/null || true
run_pm2 start ecosystem.config.js
cd "$BASEDIR"
echo "Waiting 5s for WS workers..."
sleep 5

echo ""
echo "--- Step 4d: Start uWS servers (PM2) ---"
NVM_DIR="${PM2_HOME_USER}/.nvm"
[ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh"
NODE_20_BIN="$(nvm which 20 2>/dev/null || which node 2>/dev/null)"
echo "uWS Node interpreter: $NODE_20_BIN ($(node --version))"
cd "$BASEDIR/uws-server"
PATH="$(dirname "$NODE_20_BIN"):$PATH" run_as_user npm install --silent
cd "$BASEDIR/uws-server"
NODE_20_PATH="$NODE_20_BIN" run_pm2 describe uws-benchmark &>/dev/null && run_pm2 delete uws-benchmark 2>/dev/null || true
NODE_20_PATH="$NODE_20_BIN" run_pm2 start ecosystem.config.js
cd "$BASEDIR"
echo "Waiting 5s for uWS workers..."
sleep 5

# ──────────────────────────────────────────────
# Step 5: Health check
# ──────────────────────────────────────────────
echo ""
echo "--- Step 5: Health check ---"
bash "$BASEDIR/health-check-1gb.sh"

# ──────────────────────────────────────────────
# Step 6: Install deps
# ──────────────────────────────────────────────
echo ""
echo "--- Step 6: Install deps + build Go client ---"
npm install --silent --prefix "$BASEDIR/benchmark-client"
npm install --silent --prefix "$BASEDIR/producer"
npm rebuild --silent --prefix "$BASEDIR/producer"
command -v go >/dev/null || { echo "ERROR: go not found. Install Go first."; exit 1; }
echo "Building Go benchmark client..."
(cd "$BASEDIR/benchmark-client/go-client" && go build -o benchmark-client .)
echo "Go binary: $(ls -lh "$BASEDIR/benchmark-client/go-client/benchmark-client" | awk '{print $5, $6, $7, $8}')"

# ──────────────────────────────────────────────
# Step 6b: Check container readiness
# ──────────────────────────────────────────────
echo ""
echo "--- Container Kafka consumer status ---"
  for c in grpc-server-1 grpc-server-2 grpc-server-3 grpc-host-1 grpc-host-2 grpc-host-3 \
           uws-server-1 uws-server-2 uws-server-3 uws-host-1 uws-host-2 uws-host-3; do
  CONSUMER_READY=$(docker logs "$c" 2>&1 | grep -cE "Kafka consumer (connected|ready)" || true)
  echo "  $c: consumer_ready=$CONSUMER_READY"
  if [ "$CONSUMER_READY" -eq 0 ]; then
    echo "    Last 5 lines:"
    docker logs "$c" 2>&1 | tail -5 | sed 's/^/      /'
  fi
done

# ──────────────────────────────────────────────
# Step 6c: Quick Kafka verify (produce + consume 1 msg)
# ──────────────────────────────────────────────
echo ""
echo "--- Quick Kafka verify: produce + consume 1 message ---"
VERIFY_TOPIC="__bench_verify_$$"
"${KAFKA_DIR}/bin/kafka-topics.sh" --create \
  --topic "$VERIFY_TOPIC" \
  --bootstrap-server "192.168.0.5:${KAFKA_PORT}" \
  --partitions 1 --replication-factor 1 \
  --config retention.ms=60000 \
  --if-not-exists 2>/dev/null || true
echo "test-message-$(date +%s)" | "${KAFKA_DIR}/bin/kafka-console-producer.sh" \
  --topic "$VERIFY_TOPIC" \
  --bootstrap-server "192.168.0.5:${KAFKA_PORT}" \
  --property "parse.key=false" 2>/dev/null
VERIFY_MSG=$("${KAFKA_DIR}/bin/kafka-console-consumer.sh" \
  --topic "$VERIFY_TOPIC" \
  --bootstrap-server "192.168.0.5:${KAFKA_PORT}" \
  --from-beginning --max-messages 1 --timeout-ms 10000 2>/dev/null | head -1)
if [ -n "$VERIFY_MSG" ]; then
  echo "  PASS Kafka produce+consume verified: '$VERIFY_MSG'"
else
  echo "  WARN Kafka consume returned empty (broker may still be settling)"
fi
"${KAFKA_DIR}/bin/kafka-topics.sh" --delete \
  --topic "$VERIFY_TOPIC" \
  --bootstrap-server "192.168.0.5:${KAFKA_PORT}" 2>/dev/null || true

# ──────────────────────────────────────────────
# Step 6d: Verify benchmark-messages topic has data
# ──────────────────────────────────────────────
echo ""
echo "--- Verify benchmark-messages topic ---"
"${KAFKA_DIR}/bin/kafka-run-class.sh" kafka.tools.GetOffsetShell \
  --broker-list "192.168.0.5:${KAFKA_PORT}" \
  --topic benchmark-messages 2>/dev/null | head -20 || echo "  (no offsets yet - topic is empty, normal before producers start)"
echo ""

# ──────────────────────────────────────────────
# Step 7: Run benchmark
# ──────────────────────────────────────────────
PRODUCER_PIDS=""

start_producer() {
  echo ""
  echo "--- Starting $NUM_PRODUCERS producers (target: ${TARGET_MBPS} MB/s each) ---"
  PRODUCER_PIDS=""
  for i in $(seq 1 "$NUM_PRODUCERS"); do
    KAFKA_BROKER=192.168.0.5:${KAFKA_PORT} TARGET_MBPS="$TARGET_MBPS" \
      node --max-old-space-size=16384 "$BASEDIR/producer/producer-rdkafka.js" &
    PRODUCER_PIDS="$PRODUCER_PIDS $!"
  done
  echo "Producer PIDs:$PRODUCER_PIDS"
  echo "Waiting 5s for producers to ramp up..."
  sleep 5
}

stop_producer() {
  for pid in $PRODUCER_PIDS; do
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
    fi
  done
  for pid in $PRODUCER_PIDS; do
    wait "$pid" 2>/dev/null || true
  done
  PRODUCER_PIDS=""
}

cleanup() {
  echo ""
  echo "--- Cleanup ---"
  stop_producer
  cd "$BASEDIR/grpc-server" && docker compose down 2>/dev/null || true
  cd "$BASEDIR/grpc-server" && docker compose -f docker-compose.host.yml down 2>/dev/null || true
  cd "$BASEDIR/uws-server" && docker compose down 2>/dev/null || true
  cd "$BASEDIR/uws-server" && docker compose -f docker-compose.host.yml down 2>/dev/null || true
  run_pm2 stop ws-benchmark 2>/dev/null || true
  run_pm2 stop uws-benchmark 2>/dev/null || true
}
trap cleanup EXIT

echo ""
echo "--- Step 7: Running $RUNS benchmark runs ---"

for run in $(seq 1 "$RUNS"); do
  echo ""
  echo "=== Run $run/$RUNS ($(date)) ==="

  start_producer

  echo "--- Server diagnostics (after producers started) ---"
for c in grpc-server-1 grpc-server-2 grpc-server-3 grpc-host-1 grpc-host-2 grpc-host-3 \
         uws-server-1 uws-server-2 uws-server-3 uws-host-1 uws-host-2 uws-host-3; do
    echo "  === $c (last 10 lines) ==="
    docker logs "$c" --tail 10 2>&1 | sed 's/^/    /'
  done
  echo "  === WS server (pm2 logs, last 10 lines) ==="
  run_pm2 logs ws-benchmark --nostream --lines 10 2>&1 | sed 's/^/    /' || true
  echo "  === uWS server (pm2 logs, last 10 lines) ==="
  run_pm2 logs uws-benchmark --nostream --lines 10 2>&1 | sed 's/^/    /' || true
  echo "--- End server diagnostics ---"

  "$BASEDIR/benchmark-client/go-client/benchmark-client" \
    --warmup "$WARMUP" --duration "$DURATION" --conns "$CONNS" \
    2>&1 | tee "$RESULTS_DIR/1gb-run-${run}-${TIMESTAMP}.log"
  echo ""

  stop_producer

  if [ "$run" -lt "$RUNS" ]; then
    echo "Restarting Docker containers for next run..."
    cd "$BASEDIR/grpc-server"
    docker compose down 2>/dev/null || true
    docker compose -f docker-compose.host.yml down 2>/dev/null || true
    cd "$BASEDIR/uws-server"
    docker compose down 2>/dev/null || true
    docker compose -f docker-compose.host.yml down 2>/dev/null || true
    sleep 2
    cd "$BASEDIR/grpc-server"
    docker compose up -d
    docker compose -f docker-compose.host.yml up -d
    cd "$BASEDIR/uws-server"
    docker compose up -d
    docker compose -f docker-compose.host.yml up -d
    cd "$BASEDIR"
    echo "Waiting 15s for containers + Kafka consumers..."
    sleep 15
  fi
done

# ──────────────────────────────────────────────
# Step 9: Collect system info
# ──────────────────────────────────────────────
echo ""
echo "--- Step 8: Collect system info ---"
{
  echo "=== System Info ==="
  echo "Date: $(date)"
  echo "Kernel: $(uname -a)"
  echo "Node: $(node --version)"
  echo "Docker: $(docker --version)"
  echo "PM2: $(run_pm2 --version 2>/dev/null || echo 'not found')"
  echo "Kafka benchmark: systemd ($(systemctl is-active kafka-benchmark))"
  echo "Kafka benchmark port: 192.168.0.5:${KAFKA_PORT}"
  echo ""
  echo "=== Topic Info ==="
  "${KAFKA_DIR}/bin/kafka-topics.sh" --describe --topic benchmark-messages --bootstrap-server "192.168.0.5:${KAFKA_PORT}" 2>/dev/null || true
  echo ""
  echo "=== PM2 Metrics (WS) ==="
  run_pm2 show ws-benchmark 2>/dev/null || true
  echo ""
  echo "=== PM2 Metrics (uWS) ==="
  run_pm2 show uws-benchmark 2>/dev/null || true
  echo ""
  echo "=== Docker Stats ==="
  docker stats --no-stream \
    grpc-server-1 grpc-server-2 grpc-server-3 \
    grpc-host-1 grpc-host-2 grpc-host-3 \
    uws-server-1 uws-server-2 uws-server-3 \
    uws-host-1 uws-host-2 uws-host-3 \
    2>/dev/null || true
  echo ""
  echo "=== Disk Info ==="
  df -h "${KAFKA_DATA}" 2>/dev/null || df -h .
} > "$RESULTS_DIR/system-info-${TIMESTAMP}.log"

echo ""
echo "=== Benchmark complete ==="
echo "Results: $RESULTS_DIR/"
echo "Runs: $RUNS x ${DURATION}s measurement"
echo "Conns/group: $CONNS"
echo "Producer target: ${TARGET_MBPS} MB/s"
