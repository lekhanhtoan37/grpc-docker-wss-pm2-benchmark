# Kafka KRaft Systemd + 1GB/s Benchmark — Research Report

## 1. Kafka KRaft Systemd Service Unit File

### Install Steps (Linux)

```bash
# Prereq: Java 17+
sudo dnf install java-17-openjdk-devel   # RHEL
# or: sudo apt install openjdk-17-jdk    # Ubuntu

# Download Kafka 3.x+ (includes KRaft, no Zookeeper)
curl -LO https://downloads.apache.org/kafka/3.9.0/kafka_2.13-3.9.0.tgz
sudo tar -xzf kafka_2.13-3.9.0.tgz -C /opt/
sudo ln -s /opt/kafka_2.13-3.9.0 /opt/kafka

# Create dedicated user
sudo useradd -r -s /sbin/nologin kafka
sudo chown -R kafka:kafka /opt/kafka
```

### Generate Cluster UUID & Format Storage

```bash
KAFKA_CLUSTER_ID=$(/opt/kafka/bin/kafka-storage.sh random-uuid)
echo "Cluster UUID: $KAFKA_CLUSTER_ID"

sudo -u kafka /opt/kafka/bin/kafka-storage.sh format \
  -t $KAFKA_CLUSTER_ID \
  -c /opt/kafka/config/kraft/server.properties
```

### KRaft server.properties (minimal for benchmark)

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

# Socket tuning for high throughput
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
num.network.threads=8
num.io.threads=8
num.recovery.threads.per.data.dir=2
log.dirs=/var/lib/kafka/data
```

### Systemd Unit File

```ini
# /etc/systemd/system/kafka.service
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

### Enable & Start

```bash
sudo mkdir -p /var/lib/kafka/data
sudo chown -R kafka:kafka /var/lib/kafka/data
sudo systemctl daemon-reload
sudo systemctl enable --now kafka
sudo systemctl status kafka
```

---

## 2. Minimal Config for 1GB/s Throughput

### Broker-side (server.properties)

| Setting | Value | Why |
|---|---|---|
| `socket.send.buffer.bytes` | `1048576` (1MB) | Larger socket buffers |
| `socket.receive.buffer.bytes` | `1048576` | Larger socket buffers |
| `socket.request.max.bytes` | `104857600` (100MB) | Allow large produce requests |
| `num.network.threads` | `8` | More network I/O threads |
| `num.io.threads` | `8` | More disk I/O threads |
| `log.segment.bytes` | `1073741824` (1GB) | Fewer segment files, less overhead |
| `log.flush.interval.messages` | `50000` | Less frequent flush = more throughput |
| `log.flush.interval.ms` | `10000` | Less frequent flush |
| `compression.type` | `producer` | Let producer choose compression |

### Producer-side Throughput Tuning

| Setting | Value | Why |
|---|---|---|
| `batch.size` | `131072` (128KB) or `262144` (256KB) | Larger batches = fewer requests |
| `linger.ms` | `20`–`50` | **Biggest throughput lever** — allows batch filling |
| `compression.type` | `lz4` or `zstd` | lz4 = fast CPU, zstd = better ratio (~20-30% better) |
| `acks` | `1` | Leader-only ack (benchmark, not prod) |
| `buffer.memory` | `268435456` (256MB) or `536870912` (512MB) | Avoid blocking on buffer full |
| `max.in.flight.requests.per.connection` | `5`–`10` | Pipeline more requests |
| `enable.idempotence` | `true` | Prevents duplicates on retry, ~3-5% overhead |
| `max.request.size` | `10485760` (10MB) | Allow large produce requests |
| `delivery.timeout.ms` | `120000` | Keep default |

### Key Insight: `linger.ms` > `batch.size`

- `linger.ms` is the **biggest throughput lever** — raising from 0 to 20ms can **4x throughput** on small records
- `batch.size` is a ceiling, not a target — batch fills to `batch.size` OR `linger.ms` expires, whichever first
- Measure batch fill ratio: `record-size-avg / batch-size-avg`. Below 0.5 means raise `linger.ms`

---

## 3. Topic Config for High Throughput

### Create Benchmark Topic

```bash
/opt/kafka/bin/kafka-topics.sh --create \
  --topic benchmark-1gb \
  --bootstrap-server localhost:9092 \
  --partitions 12 \
  --replication-factor 1 \
  --config retention.ms=86400000 \
  --config segment.bytes=1073741824 \
  --config retention.bytes=-1 \
  --config max.message.bytes=10485760 \
  --config min.insync.replicas=1
```

### Partition Count Sizing

```
Partitions = max(T/P, T/C)
T = target throughput (MB/s) = 1000 MB/s
P = producer throughput per partition (~10 MB/s typical)
=> ~100 partitions for full parallelism
```

**For single-node benchmark:** 12–24 partitions sufficient (single broker, disk I/O is bottleneck, not partition parallelism)

### Key Topic Settings

| Setting | Value | Why |
|---|---|---|
| `partitions` | 12–24 | Parallelism for single broker |
| `replication-factor` | `1` | Benchmark only, no replication overhead |
| `segment.bytes` | `1073741824` (1GB) | Fewer segment files, less file handle overhead |
| `retention.ms` | `86400000` (24h) | Enough for benchmark run |
| `retention.bytes` | `-1` | No size limit (disk permitting) |
| `max.message.bytes` | `10485760` (10MB) | Allow large batches |
| `min.insync.replicas` | `1` | Single broker, no quorum needed |
| `compression.type` | `producer` | Let producer decide |

---

## 4. Producer Math for 1GB/s (1000 MB/s) with ~1KB Messages

### Throughput Calculation

```
Target: 1000 MB/s
Message size: ~1 KB (1024 bytes)
Messages/second: 1000 * 1024 * 1024 / 1024 = ~1,024,000 msg/s
```

**~1M messages/second** needed for 1GB/s with 1KB messages.

### Batch Tuning for 1M msg/s

```
batch.size = 131072 (128KB)
messages per batch = 131072 / 1024 = 128 messages
batches/second = 1,024,000 / 128 = 8,000 batches/s
```

With `linger.ms=20`:
- In 20ms at 1M msg/s → ~20,000 messages arrive → multiple batches fill per linger period
- With 12 partitions: ~85,333 msg/s per partition → ~666 batches/s per partition

### Recommended Producer Config (for 1GB/s benchmark)

```properties
bootstrap.servers=localhost:9092
acks=1
batch.size=262144
linger.ms=20
compression.type=lz4
buffer.memory=536870912
max.in.flight.requests.per.connection=10
max.request.size=10485760
enable.idempotence=true
delivery.timeout.ms=120000
key.serializer=org.apache.kafka.common.serialization.StringSerializer
value.serializer=org.apache.kafka.common.serialization.StringSerializer
```

### Run Built-in Benchmark

```bash
/opt/kafka/bin/kafka-producer-perf-test.sh \
  --topic benchmark-1gb \
  --num-records 10000000 \
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

### Expected Results (single broker, modern NVMe)

| Config | Throughput | Notes |
|---|---|---|
| Default (batch=16KB, linger=0) | ~50K msg/s (~50 MB/s) | Baseline |
| Optimized (batch=128KB, linger=20, lz4) | ~300K-500K msg/s | Good tuning |
| Max throughput (batch=256KB, linger=50, zstd) | ~500K+ msg/s | Best case |

**Reaching true 1GB/s (1M msg/s) on single node** requires:
- NVMe SSD (not SATA)
- Sufficient RAM for page cache (8GB+)
- JVM heap 4G+
- May need multiple producer instances in parallel

---

## 5. macOS-Specific Considerations

### Option A: Homebrew (simplest)

```bash
brew install kafka
# Recent brew kafka 3.x+ uses KRaft by default
# Config: /opt/homebrew/etc/kafka/ (Apple Silicon) or /usr/local/etc/kafka/ (Intel)
# Data: /opt/homebrew/var/lib/kafka-logs/ or /usr/local/var/lib/kafka-logs/

# Start with KRaft (no Zookeeper needed for Kafka 3.x+)
brew services start kafka
```

**Note:** Older brew formulas required separate `brew services start zookeeper`. Kafka 3.7+ brew formula defaults to KRaft mode.

### Option B: Tarball (more control)

```bash
curl -LO https://downloads.apache.org/kafka/3.9.0/kafka_2.13-3.9.0.tgz
tar -xzf kafka_2.13-3.9.0.tgz
cd kafka_2.13-3.9.0

# Generate cluster ID
KAFKA_CLUSTER_ID=$(bin/kafka-storage.sh random-uuid)

# Format storage
bin/kafka-storage.sh format -t $KAFKA_CLUSTER_ID -c config/kraft/server.properties

# Start
bin/kafka-server-start.sh config/kraft/server.properties
```

### macOS Tuning Notes

- **File descriptor limit:** `ulimit -n 100000` — macOS default is 256, too low for Kafka
- **sysctl tuning:**
  ```bash
  sudo sysctl -w kern.maxfiles=65536
  sudo sysctl -w kern.maxfilesperproc=65536
  ```
- **APFS filesystem:** Good sequential write performance, but not as predictable as ext4/xfs on Linux
- **Docker Desktop:** If running Kafka in Docker on macOS, throughput is **significantly lower** due to virtiofs overhead — expect 100-300 MB/s max
- **For real benchmarks:** Use Linux (native or VM) — macOS filesystem overhead adds noise
- **Homebrew launchd:** `brew services start kafka` uses launchd plist, not systemd. Config at `~/Library/LaunchAgents/homebrew.mxcl.kafka.plist`

### macOS KRaft Config Location

```
Apple Silicon: /opt/homebrew/etc/kafka/kraft/server.properties
Intel:         /usr/local/etc/kafka/kraft/server.properties
```

Edit `listeners` to ensure `localhost:9092`:
```properties
listeners=PLAINTEXT://localhost:9092,CONTROLLER://localhost:9093
advertised.listeners=PLAINTEXT://localhost:9092
```

---

## Quick Reference: End-to-End Setup

```bash
# 1. Install
curl -LO https://downloads.apache.org/kafka/3.9.0/kafka_2.13-3.9.0.tgz
tar -xzf kafka_2.13-3.9.0.tgz && mv kafka_2.13-3.9.0 /opt/kafka

# 2. Format KRaft storage
KAFKA_CLUSTER_ID=$(/opt/kafka/bin/kafka-storage.sh random-uuid)
/opt/kafka/bin/kafka-storage.sh format -t $KAFKA_CLUSTER_ID -c /opt/kafka/config/kraft/server.properties

# 3. Edit server.properties (see above), then start
/opt/kafka/bin/kafka-server-start.sh -daemon /opt/kafka/config/kraft/server.properties

# 4. Create benchmark topic
/opt/kafka/bin/kafka-topics.sh --create --topic bench \
  --bootstrap-server localhost:9092 \
  --partitions 12 --replication-factor 1 \
  --config retention.ms=86400000 --config segment.bytes=1073741824

# 5. Run perf test
/opt/kafka/bin/kafka-producer-perf-test.sh \
  --topic bench --num-records 10000000 --record-size 1024 --throughput -1 \
  --producer-props bootstrap.servers=localhost:9092 \
    batch.size=262144 linger.ms=20 compression.type=lz4 acks=1 \
    buffer.memory=536870912
```
