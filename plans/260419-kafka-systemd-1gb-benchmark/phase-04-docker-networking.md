# Phase 04: Update Docker Networking for Host Kafka

**Goal:** Update gRPC Docker Compose files to connect to host-systemd Kafka on localhost:9092 instead of Docker Kafka on kafka-net.

**Effort:** 1-2h

---

## Task 4.1: Update gRPC bridge compose to reach host Kafka

**Files:**
- Modify: `grpc-server/docker-compose.yml`

**Key insight:** When Kafka runs on the host (systemd), containers on bridge network reach it via `host.docker.internal` (Docker Desktop) or `172.17.0.1` (Linux Docker Engine). The `extra_hosts` directive maps `host.docker.internal` to the host gateway on Linux.

- [ ] **Step 1: Update docker-compose.yml**

```yaml
services:
  grpc-server-1:
    build: .
    container_name: grpc-server-1
    environment:
      CONTAINER_ID: "1"
      KAFKA_BROKER: "host.docker.internal:9092"
      KAFKA_TOPIC: "benchmark-messages"
    ports:
      - "50051:50051"
    networks:
      - grpc-net
    extra_hosts:
      - "host.docker.internal:host-gateway"

  grpc-server-2:
    build: .
    container_name: grpc-server-2
    environment:
      CONTAINER_ID: "2"
      KAFKA_BROKER: "host.docker.internal:9092"
      KAFKA_TOPIC: "benchmark-messages"
    ports:
      - "50052:50051"
    networks:
      - grpc-net
    extra_hosts:
      - "host.docker.internal:host-gateway"

  grpc-server-3:
    build: .
    container_name: grpc-server-3
    environment:
      CONTAINER_ID: "3"
      KAFKA_BROKER: "host.docker.internal:9092"
      KAFKA_TOPIC: "benchmark-messages"
    ports:
      - "50053:50051"
    networks:
      - grpc-net
    extra_hosts:
      - "host.docker.internal:host-gateway"

networks:
  grpc-net:
    driver: bridge
```

**Changes:**
- Removed `kafka-net` external network (no longer needed — Kafka is on host)
- Changed `KAFKA_BROKER` from `benchmark-kafka:29092` to `host.docker.internal:9092`
- Added `extra_hosts: host.docker.internal:host-gateway` for Linux compatibility
- This is the **Docker bridge** path — traffic goes: container → docker0 bridge → iptables NAT → host loopback → Kafka

- [ ] **Step 2: Verify connectivity from container**

```bash
cd grpc-server && docker compose up -d --build
docker exec grpc-server-1 node -e "
  const net = require('net');
  const s = net.createConnection(9092, 'host.docker.internal', () => {
    console.log('Connected to host Kafka!');
    s.end();
  });
  s.on('error', (e) => { console.log('Failed:', e.message); process.exit(1); });
"
```

Expected: "Connected to host Kafka!"

- [ ] **Step 3: Commit**

```bash
git add grpc-server/docker-compose.yml
git commit -m "feat: update gRPC bridge compose to reach host Kafka via host.docker.internal"
```

---

## Task 4.2: Verify gRPC host compose (no changes needed)

**Files:**
- No changes: `grpc-server/docker-compose.host.yml`

**Reason:** Host-networked containers already use `KAFKA_BROKER: "localhost:9092"` and `network_mode: host`. They share the host's network namespace, so they connect to systemd Kafka on localhost:9092 directly. No changes required.

- [ ] **Step 1: Verify host-networked containers can reach Kafka**

```bash
cd grpc-server && docker compose -f docker-compose.host.yml up -d --build
docker exec grpc-host-1 node -e "
  const net = require('net');
  const s = net.createConnection(9092, 'localhost', () => {
    console.log('Host Kafka reachable!');
    s.end();
  });
  s.on('error', (e) => { console.log('Failed:', e.message); process.exit(1); });
"
```

Expected: "Host Kafka reachable!"

---

## Task 4.3: Docker daemon tuning (Linux only)

**Files:**
- Create: `infra/docker-daemon-tuning.sh`

**Purpose:** Reduce Docker bridge overhead by disabling userspace proxy.

- [ ] **Step 1: Create tuning script**

```bash
#!/bin/bash
set -e

echo "=== Docker daemon tuning for benchmark ==="

if [ ! -f /etc/docker/daemon.json ]; then
  echo '{"userland-proxy": false}' | sudo tee /etc/docker/daemon.json
else
  echo "daemon.json exists. Ensure 'userland-proxy': false is set."
  echo "Current content:"
  cat /etc/docker/daemon.json
fi

echo "Restarting Docker..."
sudo systemctl restart docker

echo "Verifying..."
docker info 2>/dev/null | grep -i proxy || echo "Proxy setting applied"
```

- [ ] **Step 2: Apply tuning**

```bash
chmod +x infra/docker-daemon-tuning.sh
sudo bash infra/docker-daemon-tuning.sh
```

Expected: Docker restarts with `userland-proxy: false`

- [ ] **Step 3: Commit**

```bash
git add infra/docker-daemon-tuning.sh
git commit -m "feat: add Docker daemon tuning script to disable userland-proxy"
```
