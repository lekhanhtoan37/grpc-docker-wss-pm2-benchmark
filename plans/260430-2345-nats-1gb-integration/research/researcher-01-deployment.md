# Research Report: NATS Integration into run-benchmark-1gb.sh

**Date:** 2026-04-30
**Scope:** Deployment strategy for NATS server + nats-worker in the 1GB benchmark script
**Target server:** Linux (192.168.0.9), runs as root with `run_as_user` for PM2

---

## 1. Step Insertion Points

The 1gb script follows a clear step pattern. Below is the full step map and where NATS components should be inserted:

| Step | Lines | Purpose | NATS Insertion? |
|------|-------|---------|-----------------|
| Step 0 | 54-67 | Install system dependencies | No |
| Step 1 | 72-220 | Setup Kafka (systemd, idempotent) | **After Step 1b (line ~294): Insert NATS server systemd setup** |
| Step 1b | 225-294 | Always reconfigure + restart Kafka | No |
| Step 2 | 299-321 | Create/verify topic | No |
| Step 3 | 329-337 | iptables DNAT for Docker → Kafka | No change needed (NATS is localhost) |
| Step 4 | 343-438 | Build + start all servers (Docker + PM2) | **After Step 4f (line ~438): Insert nats-worker deployment** |
| Step 5 | 443-445 | Health check | **Must update health-check-1gb.sh** (already has NATS checks at lines 84-93) |
| Step 6 | 451-465 | Install deps + build Go client | No (nats-worker is pre-built) |
| Step 6b | 470-482 | Container Kafka consumer status | **Add nats-worker container health check** |
| Step 6c | 487-511 | Quick Kafka verify | No |
| Step 6d | 516-567 | Stop consumers, reset topic, restart | **Must add nats-benchmark-worker-\* consumer group cleanup** |
| Step 7 | 616-725 | Run benchmark scenarios | **Must add NATS nats-worker restart logic** |
| Step 8 | 730-771 | Collect system info | **Add NATS server + nats-worker metrics** |

### Recommended New Steps

**New Step 1c: NATS Server Setup (after line ~294)**
- Insert between Step 1b (Kafka reconfigure) and Step 2 (create topic)
- Mirrors the Kafka systemd pattern exactly: idempotent check → download → create user → write config → install service → start → verify

**New Step 4g: nats-worker Deployment (after line ~438)**
- Insert after Step 4f (Go WS PM2)
- Depends on whether PM2 or Docker mode is chosen (see Section 3)

**New Step 6b-ext: nats-worker Consumer Status (after line ~482)**
- Check nats-worker containers/PM2 processes for Kafka consumer readiness

---

## 2. NATS Server Deployment via systemd (Production Linux)

### Why systemd (not Docker)

The 1gb script already uses systemd for Kafka on 192.168.0.9. NATS should follow the same pattern:
- Consistent with Kafka deployment model (both are message brokers)
- No Docker networking overhead for localhost connections
- nats-worker PM2/Docker instances connect to `nats://localhost:4222` — systemd NATS on localhost avoids Docker bridge latency
- The health-check-1gb.sh already checks `curl -sf http://localhost:8222/varz` (NATS monitoring port) — expects NATS on localhost

### Recommended systemd Service Definition

Based on the official NATS hardened service template from `nats-server/util/nats-server-hardened.service` and the project's `infra/nats.conf`:

```ini
# /etc/systemd/system/nats-benchmark.service
[Unit]
Description=NATS Benchmark Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=nats-bench
Group=nats-bench
ExecStart=/opt/nats-benchmark/nats-server -c /opt/nats-benchmark/nats.conf
ExecStop=/bin/kill -s SIGUSR2 $MAINPID
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
LimitNOFILE=100000

[Install]
WantedBy=multi-user.target
```

### NATS Config File (`/opt/nats-benchmark/nats.conf`)

Adapted from `infra/nats.conf` with monitoring enabled for the health check:

```
listen: "0.0.0.0:4222"
monitor: "0.0.0.0:8222"
max_payload: 1MB
write_deadline: "10s"
max_connections: 65536
max_subscriptions: 0
max_control_line: 4096
```

### Step 1c Implementation Pattern (mirrors Kafka setup)

```bash
NATS_VERSION="2.11.4"  # Latest stable at time of writing
NATS_DIR="/opt/nats-benchmark"
NATS_USER="nats-bench"
NATS_SERVICE="nats-benchmark"
NATS_PORT=4222
NATS_MONITOR_PORT=8222

# Idempotent check (same pattern as Kafka at lines 75-76)
if systemctl is-active --quiet "$NATS_SERVICE" 2>/dev/null \
    && nc -z 127.0.0.1 "$NATS_PORT" 2>/dev/null; then
  echo "NATS benchmark already running. Skipping setup."
else
  # Download nats-server binary
  if [ ! -f "${NATS_DIR}/nats-server" ]; then
    mkdir -p "${NATS_DIR}"
    curl -fSL "https://github.com/nats-io/nats-server/releases/download/v${NATS_VERSION}/nats-server-v${NATS_VERSION}-linux-amd64.tar.gz" \
      -o /tmp/nats-server.tar.gz
    tar -xzf /tmp/nats-server.tar.gz -C /tmp/
    cp "/tmp/nats-server-v${NATS_VERSION}-linux-amd64/nats-server" "${NATS_DIR}/nats-server"
    chmod +x "${NATS_DIR}/nats-server"
    rm -rf /tmp/nats-server.tar.gz "/tmp/nats-server-v${NATS_VERSION}-linux-amd64"
  fi

  # Create user
  id "${NATS_USER}" &>/dev/null || useradd -r -s /sbin/nologin "${NATS_USER}"

  # Write config
  tee "${NATS_DIR}/nats.conf" > /dev/null <<CONF
listen: "0.0.0.0:4222"
monitor: "0.0.0.0:8222"
max_payload: 1MB
write_deadline: "10s"
max_connections: 65536
max_subscriptions: 0
max_control_line: 4096
CONF

  chown -R "${NATS_USER}:${NATS_USER}" "${NATS_DIR}"

  # Install systemd service (idempotent)
  if [ ! -f "/etc/systemd/system/${NATS_SERVICE}.service" ]; then
    tee "/etc/systemd/system/${NATS_SERVICE}.service" > /dev/null <<SVC
[Unit]
Description=NATS Benchmark Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${NATS_USER}
Group=${NATS_USER}
ExecStart=${NATS_DIR}/nats-server -c ${NATS_DIR}/nats.conf
ExecStop=/bin/kill -s SIGUSR2 \$MAINPID
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
LimitNOFILE=100000

[Install]
WantedBy=multi-user.target
SVC
    systemctl daemon-reload
    systemctl enable "$NATS_SERVICE"
  fi

  systemctl restart "$NATS_SERVICE"
  sleep 3

  # Verify
  systemctl is-active --quiet "$NATS_SERVICE" || { echo "ERROR: NATS failed to start"; exit 1; }
fi
```

### Key Design Decisions

1. **Binary download, not package manager**: NATS distributes as a single static binary. No apt repo needed. This matches the project's approach (Kafka tarball download).
2. **Separate user (`nats-bench`)**: Mirrors `kafka-bench` user. Isolates benchmark NATS from any production NATS on the same host.
3. **SIGUSR2 for graceful stop**: NATS uses Lame Duck Mode on SIGUSR2, allowing clients to disconnect gracefully.
4. **Monitoring on port 8222**: Required by health-check-1gb.sh line 86 (`curl -sf http://localhost:8222/varz`).

---

## 3. PM2 vs Docker for nats-worker on 1GB

### Analysis

The nats-worker has three deployment modes already defined:

| Mode | Config File | Instances | Port Range | Kafka Consumer Group |
|------|------------|-----------|------------|---------------------|
| Docker bridge | `docker-compose.yml` | 3 (nats-bridge-1/2/3) | All :8095 (separate network) | `nats-benchmark-worker-{CONTAINER_ID}` |
| Docker host | `docker-compose.host.yml` | 3 (nats-worker-host-1/2/3) | :60081/:60082/:60083 | `nats-benchmark-worker-{CONTAINER_ID}` |
| PM2 | `ecosystem.config.js` | 3 | :8095/:8096/:8097 | `nats-benchmark-worker-{INSTANCE}` |

### Benchmark Client NATS Groups (from main.go lines 36-38)

```go
{Name: "NATS bridge", Type: "nats", Endpoints: []string{"nats://localhost:4222"},
    Subject: "benchmark.messages", QueueGroup: "nats-bridge"},
{Name: "NATS host", Type: "nats", Endpoints: []string{"nats://localhost:4222", ...},
    Subject: "benchmark.messages", QueueGroup: "nats-host"},
{Name: "NATS PM2", Type: "nats", Endpoints: []string{"nats://localhost:4222", ...},
    Subject: "benchmark.messages", QueueGroup: "nats-pm2"},
```

Each benchmark group needs its own NATS queue group AND its own set of nats-worker instances subscribing to different queue groups. The workers publish to the same NATS subject (`benchmark.messages`), but the benchmark client subscribes with different queue groups to isolate measurements.

### Recommendation: Deploy ALL THREE modes (bridge + host + PM2)

This matches the existing pattern where the 1gb script deploys every variant:

| Existing Service | Bridge (Docker network) | Host (Docker host-net) | PM2 |
|------------------|------------------------|----------------------|-----|
| gRPC | docker-compose.yml | docker-compose.host.yml | N/A |
| uWS | docker-compose.yml | docker-compose.host.yml | ecosystem.config.js |
| Go WS | docker-compose.yml | docker-compose.host.yml | ecosystem.config.js |
| WS | N/A | N/A | ecosystem.config.js |
| **NATS worker** | **docker-compose.yml** | **docker-compose.host.yml** | **ecosystem.config.js** |

### Implementation for each mode

**Mode 1: Docker bridge (nats-bridge-1/2/3)**
- Insert after existing bridge Docker deployments (after Step 4, line ~360)
- Uses `docker-compose.yml` — but requires a Docker network named `backend` that connects to NATS
- **Problem**: NATS is now on host (systemd), not in Docker. Bridge containers can't reach `localhost:4222`
- **Solution**: Use `docker-compose.host.yml` instead for bridge mode too, OR use `extra_hosts: ["benchmark-nats:host-gateway"]` in docker-compose.yml to allow bridge containers to reach host NATS
- **Simpler alternative**: Skip bridge mode for NATS. The bridge mode only makes sense when the server is also in Docker. Since NATS server is systemd on host, "bridge" NATS worker → host NATS adds Docker network overhead but no isolation benefit. **Recommendation: Deploy bridge nats-workers using host network mode but with different ports to simulate bridge behavior.**

Actually, re-examining: the bridge containers in the existing setup connect to Kafka at `192.168.0.9:9091` using the iptables rule from Step 3. The nats-worker bridge containers similarly connect to Kafka. They publish to NATS at `nats://benchmark-nats:4222` (Docker network DNS). With NATS now on host, bridge workers need to connect to host NATS.

**Revised recommendation**: Deploy only **host Docker** and **PM2** modes. Skip Docker bridge for nats-worker because:
1. NATS server is on host (systemd), not in a Docker network
2. Adding `extra_hosts` or host gateway to reach NATS defeats the purpose of bridge network isolation
3. The benchmark still has bridge mode coverage for gRPC, uWS, and Go WS
4. This simplifies the integration significantly

If bridge NATS is needed later, add `extra_hosts: ["nats-host:host-gateway"]` to docker-compose.yml and change `NATS_URL` to `nats://nats-host:4222`.

### Final Deployment Plan

**Step 4g: nats-worker PM2 (3 instances, ports 8095/8096/8097)**

```bash
echo ""
echo "--- Step 4g: Start nats-worker (PM2) ---"
cd "$BASEDIR/nats-worker"
PATH="$RESOLVED_PATH" go mod tidy
PATH="$RESOLVED_PATH" GOTOOLCHAIN=local go build -o nats-worker .
echo "nats-worker binary: $(ls -lh nats-worker | awk '{print $5, $6, $7, $8}')"
run_pm2 describe nats-benchmark &>/dev/null && run_pm2 delete nats-benchmark 2>/dev/null || true
run_pm2 start ecosystem.config.js
cd "$BASEDIR"
echo "Waiting 5s for nats-worker instances..."
sleep 5
echo "Verifying nats-worker ports..."
for port in 8095 8096 8097; do
  if ss -ntpl | grep -q ":${port} "; then
    echo "  :${port} OK"
  else
    echo "  :${port} NOT LISTENING"
  fi
done
```

**Step 4h: nats-worker Docker host (3 instances, ports 60081/60082/60083)**

```bash
echo ""
echo "--- Step 4h: Build + start nats-worker (Docker host) ---"
cd "$BASEDIR/nats-worker"
docker compose down 2>/dev/null || true
docker compose -f docker-compose.host.yml down 2>/dev/null || true
docker compose -f docker-compose.host.yml build --no-cache
docker compose -f docker-compose.host.yml up -d
cd "$BASEDIR"
echo "Waiting 5s for nats-worker host containers..."
sleep 5
echo "Verifying nats-worker host ports..."
for port in 60081 60082 60083; do
  if ss -ntpl | grep -q ":${port} "; then
    echo "  :${port} OK"
  else
    echo "  :${port} NOT LISTENING"
  fi
done
```

### PM2 ecosystem.config.js Updates Needed

The existing `ecosystem.config.js` is correct for PM2 mode. The `NATS_URL: "nats://localhost:4222"` works because NATS is on localhost (systemd). No changes needed.

---

## 4. Consumer Group Cleanup

### Current State (Step 6d, lines 533-536)

```bash
CONSUMER_GROUPS=$("${KAFKA_DIR}/bin/kafka-consumer-groups.sh" \
  --bootstrap-server "192.168.0.9:${KAFKA_PORT}" --list 2>/dev/null \
  | grep -E "ws-benchmark|uws-benchmark|grpc-benchmark|go-ws-benchmark" || true)
```

### Changes Required

**1. Extend the grep pattern to include nats-benchmark-worker:**

```bash
CONSUMER_GROUPS=$("${KAFKA_DIR}/bin/kafka-consumer-groups.sh" \
  --bootstrap-server "192.168.0.9:${KAFKA_PORT}" --list 2>/dev/null \
  | grep -E "ws-benchmark|uws-benchmark|grpc-benchmark|go-ws-benchmark|nats-benchmark-worker" || true)
```

**2. Stop nats-worker instances before topic reset:**

After line 521 (stop go-ws-benchmark), add:

```bash
run_pm2 stop nats-benchmark 2>/dev/null || true
```

**3. Stop nats-worker Docker host containers:**

After the go-ws-server Docker down (line 528), add:

```bash
cd "$BASEDIR/nats-worker" && docker compose -f docker-compose.host.yml down 2>/dev/null || true
cd "$BASEDIR"
```

**4. Restart nats-worker after topic reset:**

After the go-ws-server PM2 restart (line 555), add:

```bash
cd "$BASEDIR/nats-worker"
run_pm2 start ecosystem.config.js
cd "$BASEDIR"
```

After the go-ws-server Docker restart (line 563), add:

```bash
cd "$BASEDIR/nats-worker" && docker compose -f docker-compose.host.yml up -d
cd "$BASEDIR"
```

### Consumer Groups Created by nats-worker

| Mode | INSTANCE value | Consumer Group |
|------|---------------|----------------|
| PM2 (instance 0) | `0` | `nats-benchmark-worker-0` |
| PM2 (instance 1) | `1` | `nats-benchmark-worker-1` |
| PM2 (instance 2) | `2` | `nats-benchmark-worker-2` |
| Docker host-1 | `host-1` | `nats-benchmark-worker-host-1` |
| Docker host-2 | `host-2` | `nats-benchmark-worker-host-2` |
| Docker host-3 | `host-3` | `nats-benchmark-worker-host-3` |

The grep pattern `nats-benchmark-worker` catches all of these.

### Cleanup Function (trap EXIT)

The cleanup function at lines 600-612 also needs nats-worker teardown:

```bash
cleanup() {
  echo ""
  echo "--- Cleanup ---"
  stop_producer
  pkill -f "coordinator.*-listen" 2>/dev/null || true
  pkill -f "worker.*-coordinator" 2>/dev/null || true
  cd "$BASEDIR/grpc-server" && docker compose down 2>/dev/null || true
  cd "$BASEDIR/grpc-server" && docker compose -f docker-compose.host.yml down 2>/dev/null || true
  cd "$BASEDIR/uws-server" && docker compose down 2>/dev/null || true
  cd "$BASEDIR/uws-server" && docker compose -f docker-compose.host.yml down 2>/dev/null || true
  cd "$BASEDIR/go-ws-server" && docker compose down 2>/dev/null || true
  cd "$BASEDIR/go-ws-server" && docker compose -f docker-compose.host.yml down 2>/dev/null || true
  cd "$BASEDIR/nats-worker" && docker compose -f docker-compose.host.yml down 2>/dev/null || true  # NEW
  cd "$BASEDIR"
  run_pm2 stop ws-benchmark 2>/dev/null || true
  run_pm2 stop uws-benchmark 2>/dev/null || true
  run_pm2 stop go-ws-benchmark 2>/dev/null || true
  run_pm2 stop nats-benchmark 2>/dev/null || true  # NEW
}
```

### Scenario Restart Logic (lines 699-723)

Between scenarios, all Docker containers are restarted. Add nats-worker host containers:

```bash
cd "$BASEDIR/nats-worker"
docker compose -f docker-compose.host.yml down 2>/dev/null || true
sleep 2
docker compose -f docker-compose.host.yml up -d
cd "$BASEDIR"
```

---

## 5. Testing Strategy

### Phase 1: NATS Server Smoke Test (after Step 1c)

```bash
# Verify NATS systemd service
systemctl is-active --quiet nats-benchmark && echo "PASS: NATS service active" || echo "FAIL: NATS service inactive"

# Verify NATS client port
nc -z 127.0.0.1 4222 && echo "PASS: NATS port 4222 open" || echo "FAIL: NATS port 4222 closed"

# Verify NATS monitoring port
nc -z 127.0.0.1 8222 && echo "PASS: NATS monitor port 8222 open" || echo "FAIL: NATS monitor port 8222 closed"

# Verify NATS /varz endpoint
curl -sf http://localhost:8222/varz | jq '{version, cluster_id, connections}' && echo "PASS: NATS monitoring API" || echo "FAIL: NATS monitoring API"

# Publish and subscribe a test message using nats CLI (if available) or custom tool
echo "test" | nc -w1 127.0.0.1 4222
```

### Phase 2: nats-worker Health Check

```bash
# PM2 instances (ports 8095, 8096, 8097)
for port in 8095 8096 8097; do
  STATUS=$(curl -sf "http://localhost:$port/health" 2>/dev/null)
  echo "nats-worker PM2 :$port: $STATUS"
done

# Docker host instances (ports 60081, 60082, 60083)
for port in 60081 60082 60083; do
  STATUS=$(curl -sf "http://localhost:$port/health" 2>/dev/null)
  echo "nats-worker host :$port: $STATUS"
done
```

### Phase 3: End-to-End Message Flow Test

Build and run a minimal test that:
1. Produces a message to Kafka topic `benchmark-messages`
2. nats-worker consumes it from Kafka and publishes to NATS subject `benchmark.messages`
3. A test subscriber receives it from NATS

```bash
# Quick e2e test using the kafka tools + a simple NATS subscriber
# 1. Start a temporary NATS subscriber in background
PATH="$RESOLVED_PATH" timeout 15 node -e "
const { connect } = require('nats');
(async () => {
  const nc = await connect({ servers: 'nats://localhost:4222' });
  const sub = nc.subscribe('benchmark.messages');
  const msg = await Promise.race([
    (async () => { for await (const m of sub) return m; })(),
    new Promise((_, rej) => setTimeout(() => rej('timeout'), 12000))
  ]);
  console.log('RECEIVED:', msg.data.toString().substring(0, 100));
  await nc.close();
  process.exit(0);
})().catch(e => { console.error('FAIL:', e.message); process.exit(1); });
" &
NATS_SUB_PID=$!

sleep 2

# 2. Produce a test message to Kafka
echo "e2e-test-$(date +%s%N)" | "${KAFKA_DIR}/bin/kafka-console-producer.sh" \
  --topic benchmark-messages \
  --bootstrap-server "192.168.0.9:${KAFKA_PORT}" 2>/dev/null

# 3. Wait for subscriber
wait "$NATS_SUB_PID" 2>/dev/null
E2E_EXIT=$?

if [ "$E2E_EXIT" -eq 0 ]; then
  echo "PASS: End-to-end Kafka → nats-worker → NATS flow verified"
else
  echo "WARN: E2E test timed out (nats-worker may still be consuming lag)"
fi
```

### Phase 4: health-check-1gb.sh Verification

The health check script already has NATS checks (lines 84-93):
- NATS server: `curl -sf http://localhost:8222/varz | jq -r '.status'`
- NATS workers: `curl -sf http://localhost:$port/health` for ports 8095/8096/8097

These checks will pass automatically once Step 1c and Step 4g are implemented. No changes needed to health-check-1gb.sh unless Docker host workers are added (then add ports 60081/60082/60083 to the worker check loop).

**Recommended addition to health-check-1gb.sh** for Docker host workers:

```bash
echo ""
echo "--- NATS Host Workers ---"
for port in 60081 60082 60083; do
  echo -n "  NATS host worker :$port: "
  curl -sf "http://localhost:$port/health" 2>/dev/null && echo "OK" && PASS=$((PASS + 1)) || { echo "FAIL"; FAIL=$((FAIL + 1)); }
done
```

Also add NATS systemd check:

```bash
echo ""
echo "--- NATS Server ---"
check "NATS benchmark service active" "systemctl is-active nats-benchmark"
check "NATS port 127.0.0.1:4222" "nc -z 127.0.0.1 4222"
check "NATS monitor 127.0.0.1:8222" "nc -z 127.0.0.1 8222"
```

### Phase 5: Standalone Smoke Test Script

For quick validation without running the full benchmark:

```bash
# Smoke test: NATS infrastructure only
# 1. Check systemd service
systemctl is-active nats-benchmark || echo "FAIL: NATS not running"

# 2. Check NATS monitoring
curl -sf http://localhost:8222/varz | jq '{version, connections, subscriptions}'

# 3. Check nats-worker PM2 health
for port in 8095 8096 8097; do
  curl -sf "http://localhost:$port/health" && echo " :$port OK" || echo " :$port FAIL"
done

# 4. Check nats-worker Kafka consumer groups
/opt/kafka-benchmark/bin/kafka-consumer-groups.sh \
  --bootstrap-server 192.168.0.9:9091 --list 2>/dev/null \
  | grep nats-benchmark-worker

# 5. Check PM2 status
sudo -u $(echo $SUDO_USER) npx pm2 list | grep nats
```

---

## Summary of Changes Required

### Files to Create
| File | Purpose |
|------|---------|
| `infra/nats-benchmark.service` | systemd unit file for NATS server |

### Files to Modify
| File | Change |
|------|--------|
| `run-benchmark-1gb.sh` | Add Step 1c (NATS server), Step 4g/4h (nats-worker PM2 + Docker host), update Step 6d (consumer group cleanup), update Step 7 (scenario restart), update cleanup() trap |
| `health-check-1gb.sh` | Add NATS systemd checks, add nats-worker host port checks |

### Infrastructure Constants to Add to Script Header
```bash
NATS_VERSION="2.11.4"
NATS_DIR="/opt/nats-benchmark"
NATS_USER="nats-bench"
NATS_SERVICE="nats-benchmark"
NATS_PORT=4222
NATS_MONITOR_PORT=8222
```

### Insertion Summary

```
Step 0   → System deps          (unchanged)
Step 1   → Kafka setup          (unchanged)
Step 1b  → Kafka reconfigure    (unchanged)
Step 1c  → NATS server setup    ★ NEW
Step 2   → Create topic         (unchanged)
Step 3   → iptables             (unchanged)
Step 4   → gRPC/uWS/Go servers  (unchanged)
Step 4g  → nats-worker PM2      ★ NEW
Step 4h  → nats-worker Docker   ★ NEW
Step 5   → Health check         (health-check-1gb.sh updated)
Step 6   → Build client         (unchanged)
Step 6b  → Container status     (add nats-worker checks)
Step 6d  → Topic reset          (add nats-benchmark-worker cleanup)
Step 7   → Run benchmark        (add nats-worker restart between scenarios)
Step 8   → System info          (add NATS + nats-worker metrics)
```

### Risk Assessment

| Risk | Mitigation |
|------|-----------|
| NATS server fails to start | systemd auto-restart + explicit check in Step 1c |
| nats-worker can't connect to Kafka | Same iptables rule (Step 3) covers Docker → 192.168.0.9:9091 |
| nats-worker can't connect to NATS | PM2 instances use localhost:4222 (always works). Docker host uses localhost:4222 (host network mode, always works) |
| Consumer group conflicts | Step 6d explicitly deletes all nats-benchmark-worker-* groups |
| Port conflicts | PM2: 8095-8097. Docker host: 60081-60083. Both ranges are unused by existing services |
| NATS subject pollution between bridge/host/PM2 | nats-worker instances all publish to same subject. Benchmark client uses separate queue groups (`nats-pm2`, `nats-host`, `nats-bridge`) to isolate subscriptions |
