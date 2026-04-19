# Phase 01: Systemd Kafka KRaft Setup

> **Prerequisite:** Linux host with systemd (Ubuntu 22.04+ or RHEL 8+). macOS users: use `brew services start kafka` for dev only.

**Goal:** Install Kafka 3.9 KRaft as systemd service on localhost:9092 with throughput-optimized config. Create benchmark topic with 12 partitions.

**Effort:** 2-3h

---

## Task 1.1: Install Kafka KRaft

**Files:**
- Create: `infra/setup-kafka.sh`

- [ ] **Step 1: Create setup script**

```bash
#!/bin/bash
set -e

KAFKA_VERSION="3.9.0"
SCALA_VERSION="2.13"
KAFKA_DIR="/opt/kafka"
KAFKA_DATA="/var/lib/kafka/data"
KAFKA_USER="kafka"

echo "=== Installing Kafka ${KAFKA_VERSION} KRaft ==="

# Check Java 17+
if ! java -version 2>&1 | grep -q "17\|21"; then
  echo "Installing OpenJDK 17..."
  if command -v apt &>/dev/null; then
    sudo apt update && sudo apt install -y openjdk-17-jdk-headless
  elif command -v dnf &>/dev/null; then
    sudo dnf install -y java-17-openjdk-devel
  else
    echo "Unsupported package manager. Install Java 17+ manually."
    exit 1
  fi
fi

# Download Kafka
if [ ! -d "${KAFKA_DIR}" ]; then
  echo "Downloading Kafka..."
  curl -LO "https://downloads.apache.org/kafka/${KAFKA_VERSION}/kafka_${SCALA_VERSION}-${KAFKA_VERSION}.tgz"
  sudo tar -xzf "kafka_${SCALA_VERSION}-${KAFKA_VERSION}.tgz" -C /opt/
  sudo ln -s "/opt/kafka_${SCALA_VERSION}-${KAFKA_VERSION}" "${KAFKA_DIR}"
  rm "kafka_${SCALA_VERSION}-${KAFKA_VERSION}.tgz"
fi

# Create user
if ! id "${KAFKA_USER}" &>/dev/null; then
  sudo useradd -r -s /sbin/nologin "${KAFKA_USER}"
fi

# Create data dir
sudo mkdir -p "${KAFKA_DATA}"
sudo chown -R "${KAFKA_USER}:${KAFKA_USER}" "${KAFKA_DIR}"
sudo chown -R "${KAFKA_USER}:${KAFKA_USER}" "${KAFKA_DATA}"

echo "Kafka installed at ${KAFKA_DIR}"
```

- [ ] **Step 2: Run setup script**

```bash
chmod +x infra/setup-kafka.sh
sudo bash infra/setup-kafka.sh
```

Expected: `/opt/kafka` exists, `kafka` user created

- [ ] **Step 3: Commit**

```bash
git add infra/setup-kafka.sh
git commit -m "feat: add Kafka KRaft install script"
```

---

## Task 1.2: Configure KRaft server.properties

**Files:**
- Create: `infra/server.properties`

- [ ] **Step 1: Write server.properties**

```properties
# /opt/kafka/config/kraft/server.properties
node.id=1
process.roles=broker,controller
listeners=PLAINTEXT://localhost:9092,CONTROLLER://localhost:9093
advertised.listeners=PLAINTEXT://localhost:9092
controller.listener.names=CONTROLLER
listener.security.protocol.map=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT
controller.quorum.voters=1@localhost:9093
log.dirs=/var/lib/kafka/data

# Socket tuning for 1GB/s
socket.send.buffer.bytes=1048576
socket.receive.buffer.bytes=1048576
socket.request.max.bytes=104857600

# Log tuning
log.segment.bytes=1073741824
num.partitions=12
log.retention.hours=168
log.retention.bytes=-1
log.flush.interval.messages=50000
log.flush.interval.ms=10000

# Thread tuning
num.network.threads=8
num.io.threads=8
num.recovery.threads.per.data.dir=2

# Allow topic deletion
delete.topic.enable=true
auto.create.topics.enable=false
```

- [ ] **Step 2: Copy config to Kafka dir**

```bash
sudo cp infra/server.properties /opt/kafka/config/kraft/server.properties
```

- [ ] **Step 3: Format KRaft storage**

```bash
KAFKA_CLUSTER_ID=$(sudo -u kafka /opt/kafka/bin/kafka-storage.sh random-uuid)
echo "Cluster UUID: $KAFKA_CLUSTER_ID"

sudo -u kafka /opt/kafka/bin/kafka-storage.sh format \
  -t "$KAFKA_CLUSTER_ID" \
  -c /opt/kafka/config/kraft/server.properties
```

Expected: "Formatting ..." output with no errors

- [ ] **Step 4: Commit**

```bash
git add infra/server.properties
git commit -m "feat: add KRaft server.properties tuned for 1GB/s"
```

---

## Task 1.3: Create systemd unit file

**Files:**
- Create: `infra/kafka.service`

- [ ] **Step 1: Write systemd unit**

```ini
[Unit]
Description=Apache Kafka Broker (KRaft Mode)
Documentation=https://kafka.apache.org/documentation/
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=kafka
Group=kafka
Environment="KAFKA_HEAP_OPTS=-Xmx4G -Xms4G"
Environment="KAFKA_JVM_PERFORMANCE_OPTS=-server -XX:+UseG1GC -XX:MaxGCPauseMillis=20"
ExecStart=/opt/kafka/bin/kafka-server-start.sh /opt/kafka/config/kraft/server.properties
ExecStop=/opt/kafka/bin/kafka-server-stop.sh
Restart=on-failure
RestartSec=10
TimeoutStopSec=120
LimitNOFILE=100000

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 2: Install and start service**

```bash
sudo cp infra/kafka.service /etc/systemd/system/kafka.service
sudo systemctl daemon-reload
sudo systemctl enable --now kafka
sleep 10
sudo systemctl status kafka
```

Expected: `active (running)`

- [ ] **Step 3: Verify Kafka is listening**

```bash
nc -z localhost 9092 && echo "Kafka port 9092 open" || echo "FAILED"
nc -z localhost 9093 && echo "Controller port 9093 open" || echo "FAILED"
```

Expected: Both ports open

- [ ] **Step 4: Commit**

```bash
git add infra/kafka.service
git commit -m "feat: add Kafka KRaft systemd unit file"
```

---

## Task 1.4: Create benchmark topic

**Files:**
- Create: `infra/create-topic.sh`

- [ ] **Step 1: Write topic creation script**

```bash
#!/bin/bash
set -e

BROKER="${1:-localhost:9092}"
TOPIC="benchmark-messages"
PARTITIONS=12

echo "=== Creating topic ${TOPIC} with ${PARTITIONS} partitions ==="

/opt/kafka/bin/kafka-topics.sh --create \
  --topic "$TOPIC" \
  --bootstrap-server "$BROKER" \
  --partitions "$PARTITIONS" \
  --replication-factor 1 \
  --config retention.ms=86400000 \
  --config segment.bytes=1073741824 \
  --config retention.bytes=-1 \
  --config max.message.bytes=10485760 \
  --config min.insync.replicas=1 \
  --if-not-exists

/opt/kafka/bin/kafka-topics.sh --describe \
  --topic "$TOPIC" \
  --bootstrap-server "$BROKER"
```

- [ ] **Step 2: Run topic creation**

```bash
chmod +x infra/create-topic.sh
sudo bash infra/create-topic.sh
```

Expected: Topic with 12 partitions, replication-factor 1

- [ ] **Step 3: Verify with Java producer perf test (baseline)**

```bash
/opt/kafka/bin/kafka-producer-perf-test.sh \
  --topic benchmark-messages \
  --num-records 1000000 \
  --record-size 1024 \
  --throughput -1 \
  --producer-props \
    bootstrap.servers=localhost:9092 \
    batch.size=262144 \
    linger.ms=20 \
    compression.type=lz4 \
    acks=1 \
    buffer.memory=536870912
```

Expected: >200 MB/s on NVMe, >50 MB/s on SATA SSD. If <50 MB/s, Kafka is misconfigured.

- [ ] **Step 4: Commit**

```bash
git add infra/create-topic.sh
git commit -m "feat: add benchmark topic creation script"
```

---

## Task 1.5: Create teardown script (rollback)

**Files:**
- Create: `infra/teardown-kafka.sh`

- [ ] **Step 1: Write teardown script**

```bash
#!/bin/bash
set -e

echo "=== Stopping Kafka systemd service ==="
sudo systemctl stop kafka || true
sudo systemctl disable kafka || true
sudo rm -f /etc/systemd/system/kafka.service
sudo systemctl daemon-reload

echo "Kafka stopped. Data preserved at /var/lib/kafka/data"
echo "To fully remove: sudo rm -rf /opt/kafka /var/lib/kafka/data"
```

- [ ] **Step 2: Commit**

```bash
chmod +x infra/teardown-kafka.sh
git add infra/teardown-kafka.sh
git commit -m "feat: add Kafka teardown script for rollback"
```
