#!/bin/bash
set -euo pipefail

echo "=== 1GB/s Throughput Benchmark ==="

BASEDIR="$(cd "$(dirname "$0")" && pwd)"
WARMUP="${WARMUP:-30}"
DURATION="${DURATION:-120}"
RUNS="${RUNS:-3}"
TARGET_MBPS="${TARGET_MBPS:-1000}"
RESULTS_DIR="$BASEDIR/results"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

KAFKA_VERSION="3.9.2"
SCALA_VERSION="2.13"
KAFKA_DIR="/opt/kafka-benchmark"
KAFKA_DATA="/var/lib/kafka-benchmark/data"
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

if systemctl is-active --quiet "$KAFKA_SERVICE" 2>/dev/null && nc -z 127.0.0.1 "$KAFKA_PORT" 2>/dev/null; then
  echo "Kafka benchmark already running on 127.0.0.1:$KAFKA_PORT. Skipping setup."
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

  # Write server.properties (always overwrite to ensure 127.0.0.1:9091)
  echo "Writing server.properties (bind 127.0.0.1:${KAFKA_PORT})..."
  sudo tee "${KAFKA_DIR}/config/kraft/server.properties" > /dev/null <<'PROPS'
node.id=1
process.roles=broker,controller
listeners=PLAINTEXT://127.0.0.1:9091,CONTROLLER://127.0.0.1:9093
advertised.listeners=PLAINTEXT://127.0.0.1:9091
controller.listener.names=CONTROLLER
listener.security.protocol.map=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT
controller.quorum.voters=1@127.0.0.1:9093
log.dirs=/var/lib/kafka-benchmark/data

socket.send.buffer.bytes=1048576
socket.receive.buffer.bytes=1048576
socket.request.max.bytes=104857600

log.segment.bytes=1073741824
num.partitions=12
log.retention.hours=168
log.retention.bytes=-1
log.flush.interval.messages=50000
log.flush.interval.ms=10000

num.network.threads=8
num.io.threads=8
num.recovery.threads.per.data.dir=2

delete.topic.enable=true
auto.create.topics.enable=false
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

# Verify port
if nc -z 127.0.0.1 "$KAFKA_PORT" 2>/dev/null; then
  echo "Port 127.0.0.1:${KAFKA_PORT}: open"
else
  echo "ERROR: Port 127.0.0.1:${KAFKA_PORT} not open."
  exit 1
fi

# ──────────────────────────────────────────────
# Step 2: Create topic (idempotent)
# ──────────────────────────────────────────────
echo ""
echo "--- Step 2: Verify benchmark topic ---"
if "${KAFKA_DIR}/bin/kafka-topics.sh" --describe \
    --topic benchmark-messages \
    --bootstrap-server "127.0.0.1:${KAFKA_PORT}" 2>/dev/null; then
  echo "Topic benchmark-messages exists."
else
  echo "Creating topic benchmark-messages (12 partitions)..."
  "${KAFKA_DIR}/bin/kafka-topics.sh" --create \
    --topic benchmark-messages \
    --bootstrap-server "127.0.0.1:${KAFKA_PORT}" \
    --partitions 12 \
    --replication-factor 1 \
    --config retention.ms=86400000 \
    --config segment.bytes=1073741824 \
    --config retention.bytes=-1 \
    --config max.message.bytes=10485760 \
    --config min.insync.replicas=1 \
    --if-not-exists
  "${KAFKA_DIR}/bin/kafka-topics.sh" --describe \
    --topic benchmark-messages \
    --bootstrap-server "127.0.0.1:${KAFKA_PORT}"
fi

# ──────────────────────────────────────────────
# Step 3: Build + start gRPC Docker
# ──────────────────────────────────────────────
echo ""
echo "--- Step 3: Build + start gRPC servers ---"
cd "$BASEDIR/grpc-server"
docker compose build
docker compose -f docker-compose.host.yml build
docker compose up -d
cd "$BASEDIR"
echo "Waiting 10s for gRPC bridge containers..."
sleep 10

cd "$BASEDIR/grpc-server"
docker compose -f docker-compose.host.yml up -d
cd "$BASEDIR"
echo "Waiting 5s for gRPC host containers..."
sleep 5

# ──────────────────────────────────────────────
# Step 4: Start WS servers (PM2)
# ──────────────────────────────────────────────
echo ""
echo "--- Step 4: Start WS servers (PM2) ---"
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
echo "--- Step 6: Install deps ---"
npm install --silent --prefix "$BASEDIR/benchmark-client"
npm install --silent --prefix "$BASEDIR/producer"

# ──────────────────────────────────────────────
# Step 7: Run benchmark
# ──────────────────────────────────────────────
PRODUCER_PID=""

start_producer() {
  echo ""
  echo "--- Starting producer (target: ${TARGET_MBPS} MB/s) ---"
  KAFKA_BROKER=127.0.0.1:${KAFKA_PORT} TARGET_MBPS="$TARGET_MBPS" \
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
  echo ""
  echo "--- Cleanup ---"
  stop_producer
  cd "$BASEDIR/grpc-server" && docker compose down 2>/dev/null || true
  cd "$BASEDIR/grpc-server" && docker compose -f docker-compose.host.yml down 2>/dev/null || true
  pm2 stop ws-benchmark 2>/dev/null || true
}
trap cleanup EXIT

echo ""
echo "--- Step 7: Running $RUNS benchmark runs ---"

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

# ──────────────────────────────────────────────
# Step 8: Collect system info
# ──────────────────────────────────────────────
echo ""
echo "--- Step 8: Collect system info ---"
{
  echo "=== System Info ==="
  echo "Date: $(date)"
  echo "Kernel: $(uname -a)"
  echo "Node: $(node --version)"
  echo "Docker: $(docker --version)"
  echo "PM2: $(pm2 --version)"
  echo "Kafka benchmark: systemd ($(systemctl is-active kafka-benchmark))"
  echo "Kafka benchmark port: 127.0.0.1:${KAFKA_PORT}"
  echo ""
  echo "=== Topic Info ==="
  "${KAFKA_DIR}/bin/kafka-topics.sh" --describe --topic benchmark-messages --bootstrap-server "127.0.0.1:${KAFKA_PORT}" 2>/dev/null || true
  echo ""
  echo "=== PM2 Metrics ==="
  pm2 show ws-benchmark 2>/dev/null || true
  echo ""
  echo "=== Docker Stats ==="
  docker stats --no-stream grpc-server-1 grpc-server-2 grpc-server-3 grpc-host-1 grpc-host-2 grpc-host-3 2>/dev/null || true
  echo ""
  echo "=== Disk Info ==="
  df -h "${KAFKA_DATA}" 2>/dev/null || df -h .
} > "$RESULTS_DIR/system-info-${TIMESTAMP}.log"

echo ""
echo "=== Benchmark complete ==="
echo "Results: $RESULTS_DIR/"
echo "Runs: $RUNS x ${DURATION}s measurement"
echo "Producer target: ${TARGET_MBPS} MB/s"
