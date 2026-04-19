# Research Report: Docker `network_mode: host` for gRPC Benchmark

**Date:** 2026-04-19  
**Scope:** Adding gRPC host-networked containers to existing bridge-networked benchmark project

---

## 1. Current Project Architecture

The project runs 3 gRPC server instances in Docker with **bridge networking**:

- **gRPC servers** (`grpc-server/docker-compose.yml`): 3 containers on custom bridge networks (`grpc-net`, `kafka-net`)
- **Kafka** (`infra/docker-compose.yml`): Zookeeper + Kafka on default bridge network, with two listeners:
  - `PLAINTEXT://localhost:9092` (host access)
  - `INTERNAL://benchmark-kafka:29092` (container-to-container via Docker DNS)
- Each gRPC server maps a unique host port: `50051→50051`, `50052→50051`, `50053→50051`
- Kafka connectivity: `benchmark-kafka:29092` (Docker DNS resolves across the external `infra_default` network)

---

## 2. How `network_mode: host` Works

### 2.1 Core Mechanism

With `network_mode: host`, the container **shares the host's network namespace directly**. There is no separate network namespace, no virtual ethernet pair (veth), no docker0 bridge, and no NAT/ipables rules.

```
Bridge mode:   [Host OS] -- veth -- [Docker bridge] -- veth -- [Container namespace]
Host mode:     [Host OS] ← shared → [Container process]
```

The container process binds ports directly on the host's network interfaces. From inside the container, `localhost` and `127.0.0.1` refer to the **actual host's** loopback.

### 2.2 Docker Compose Syntax

```yaml
services:
  grpc-host-1:
    build: .
    container_name: grpc-host-1
    network_mode: host
    environment:
      KAFKA_BROKER: "localhost:9092"
```

**Critical rule:** Do NOT use `networks:` key alongside `network_mode: host`. Do NOT use `ports:` alongside `network_mode: host`. These are mutually exclusive and will cause errors.

---

## 3. Port Mapping Implications

### 3.1 No Port Mapping Needed (or Allowed)

With host networking, the `ports:` directive is **incompatible**. Docker will either:
- Throw an error: `"host" network_mode is incompatible with port_bindings` (Compose V1)
- Silently discard port mappings with a warning: `WARNING: Published ports are discarded when using host network mode` (Compose V2 / `docker run`)

The container's process binds directly to host ports. If a gRPC server listens on port `50051`, it is immediately accessible at `host-ip:50051` with no mapping.

### 3.2 Port Conflict Problem for Multiple Instances

**This is the critical challenge for running 3 host-networked gRPC instances.**

Since all 3 instances share the host's network namespace, they **cannot bind the same port**. If each gRPC server listens on `0.0.0.0:50051`, only the first container will start — the other two will fail with `EADDRINUSE`.

**Solutions:**

| Approach | How | Pros | Cons |
|----------|-----|------|------|
| **Different listen ports per container** | Pass `PORT=50061/50062/50063` via env var; server binds to that port | Simple, deterministic | Clients must know each port |
| **Loopback binding** | Bind to `127.0.0.1:$PORT` with unique ports | Works on macOS Docker Desktop | Same as above |
| **Scale with port ranges** | Not applicable to host mode | — | Port ranges only work in bridge/Swarm mode |

**Recommended approach:** Configure each gRPC server instance with a unique port via environment variable:

```yaml
services:
  grpc-host-1:
    build: .
    container_name: grpc-host-1
    network_mode: host
    environment:
      GRPC_PORT: "50061"
      KAFKA_BROKER: "localhost:9092"

  grpc-host-2:
    build: .
    container_name: grpc-host-2
    network_mode: host
    environment:
      GRPC_PORT: "50062"
      KAFKA_BROKER: "localhost:9092"

  grpc-host-3:
    build: .
    container_name: grpc-host-3
    network_mode: host
    environment:
      GRPC_PORT: "50063"
      KAFKA_BROKER: "localhost:9092"
```

Use ports **outside** the range used by the existing bridge-networked servers (50051-50053) to avoid conflicts with those containers too.

---

## 4. Kafka Connectivity Changes

### 4.1 How Bridge-Mode Servers Connect to Kafka

Current setup:
- gRPC containers are on the `kafka-net` (external `infra_default`) network
- They use Docker DNS: `benchmark-kafka:29092` → resolves to Kafka container's internal IP
- Kafka's `INTERNAL` listener advertises `benchmark-kafka:29092`

### 4.2 How Host-Mode Servers Connect to Kafka

With `network_mode: host`, Docker DNS is **unavailable**. The container cannot resolve `benchmark-kafka` as a hostname. Instead:

**Option A: Use `localhost:9092`** (recommended if Kafka is on the same host)
- Kafka already advertises `PLAINTEXT://localhost:9092`
- Host-networked containers see the host's network stack, so `localhost:9092` reaches the Kafka port mapped from the infra compose file
- This works because `infra/docker-compose.yml` publishes `9092:9092` to the host

**Option B: Use `host.docker.internal`** (macOS fallback)
- Not needed with host networking — the container already IS on the host network

**Change required:**
```yaml
environment:
  KAFKA_BROKER: "localhost:9092"  # was "benchmark-kafka:29092"
```

### 4.3 Important: The Kafka Container Still Uses Bridge Networking

The Kafka container itself does NOT need to switch to host mode. It stays on its default bridge network with published ports. The host-networked gRPC servers simply reach Kafka via the host-published port `9092`.

---

## 5. macOS Docker Desktop Limitations (CRITICAL)

### 5.1 Host Networking on macOS Is Fundamentally Different

**Docker Desktop on macOS runs containers inside a Linux VM.** This means `network_mode: host` gives the container access to the **VM's** network namespace, NOT the macOS host's network namespace.

Practical implications:

| Aspect | Linux | macOS Docker Desktop |
|--------|-------|---------------------|
| Host network | Actual host network | Linux VM network |
| `localhost` in container | Host's localhost | VM's localhost |
| Port visibility | Immediate on host | **Must be enabled in Docker Desktop settings** |
| Performance gain | Full (no NAT overhead) | **Partial** (still crosses VM boundary) |
| DNS resolution | Host's DNS | VM's DNS |

### 5.2 Required: Enable Host Networking in Docker Desktop Settings

As of Docker Desktop 4.34+, host networking is GA on macOS but must be **explicitly enabled**:

1. Open Docker Desktop → Settings → Resources → Network
2. Enable **"Host Networking"**
3. Restart Docker Desktop

Without this setting, `network_mode: host` is silently ignored or behaves incorrectly.

### 5.3 Known Bugs on macOS

From Docker GitHub issues:

- **Port binding issues:** Sockets must bind to `127.0.0.1:PORT` specifically (not `0.0.0.0:PORT`) for ports to be forwarded to the macOS host (issue #7347, fixed in 4.32+ but still has edge cases)
- **IPv6 issues:** Host networking on macOS only reliably works with IPv4. Bind to `127.0.0.1` not `::` (issue #7261)
- **Restart failures:** After Docker Desktop restart, host-networked containers may become unreachable until recreated (issue #7683, still open)
- **LAN access regressions:** Version 4.57.0 broke LAN access from containers (issue #7836)
- **VM boundary overhead:** Even with host networking, packets traverse the macOS↔VM boundary, reducing the performance benefit compared to native Linux

### 5.4 Benchmark Validity Concern

**If running on macOS Docker Desktop, the benchmark comparing host vs bridge networking may not reflect production Linux performance.** The VM layer introduces overhead that partially negates the host networking advantage. The results would still be valid for comparing relative performance on macOS, but absolute numbers won't match Linux production.

**Recommendation:** Document this limitation clearly. If the benchmark is meant to simulate production Linux behavior, consider running it on an actual Linux host or VM.

---

## 6. Performance Implications: Host vs Bridge

### 6.1 Measured Overhead

Based on multiple benchmarks and real-world tests:

| Metric | Bridge Mode | Host Mode | Improvement |
|--------|-------------|-----------|-------------|
| Throughput (iperf3) | ~20-30 Gbps | ~40 Gbps | 25-100% higher |
| Latency overhead (NAT) | 15-50 µs per packet | ~0 µs | 15-50 µs saved |
| Median latency (HTTP) | baseline | 2-8% lower | Moderate |
| p95 latency | baseline | 3-10% lower | Noticeable |
| Requests/sec (nginx, ab) | ~1600 RPS | ~5300 RPS | 3x (extreme case with NAT proxy) |

### 6.2 Where Overhead Comes From

Bridge mode overhead sources:
1. **veth pair** — virtual ethernet device pair between container and bridge
2. **docker0 bridge** — L2 forwarding within the host
3. **iptables NAT** — DNAT/SNAT rules for port mapping (`-p` flag)
4. **docker-proxy** — userspace proxy process for localhost port forwarding (largest overhead source)

The docker-proxy is particularly costly. When a client connects to `localhost:mapped_port` on the host, the connection goes through a userspace proxy before reaching the container. This is why bridge mode with port mapping can be **2-3x slower** than host mode for localhost connections.

### 6.3 gRPC-Specific Impact

gRPC uses HTTP/2 with persistent multiplexed connections. The performance difference is most pronounced when:
- Many short-lived RPC calls (each incurs NAT overhead in bridge mode)
- Large payloads (NAT overhead is amortized, less difference)
- Many concurrent streams (connection setup cost matters)

For a benchmark comparing bridge vs host, expect **measurable but not dramatic** differences for gRPC, since gRPC reuses connections. The main benefit will be reduced p95/p99 latency tail.

### 6.4 Summary Table

| Concern | Bridge Mode | Host Mode |
|---------|-------------|-----------|
| Network isolation | Good | None |
| Docker DNS | Available | Unavailable |
| Port mapping | Required | Not allowed |
| Performance | Good (slight overhead) | Best (zero overhead) |
| macOS compatibility | Full | Partial (VM boundary) |
| Multiple same-image instances | Easy (different mapped ports) | Requires different listen ports |

---

## 7. Implementation Recommendations

### 7.1 Docker Compose File Structure

Create a **separate** docker-compose file (e.g., `docker-compose.host.yml`) for the host-networked instances:

```yaml
# grpc-server/docker-compose.host.yml
services:
  grpc-host-1:
    build: .
    container_name: grpc-host-1
    network_mode: host
    environment:
      CONTAINER_ID: "host-1"
      GRPC_PORT: "50061"
      KAFKA_BROKER: "localhost:9092"
      KAFKA_TOPIC: "benchmark-messages"

  grpc-host-2:
    build: .
    container_name: grpc-host-2
    network_mode: host
    environment:
      CONTAINER_ID: "host-2"
      GRPC_PORT: "50062"
      KAFKA_BROKER: "localhost:9092"
      KAFKA_TOPIC: "benchmark-messages"

  grpc-host-3:
    build: .
    container_name: grpc-host-3
    network_mode: host
    environment:
      CONTAINER_ID: "host-3"
      GRPC_PORT: "50063"
      KAFKA_BROKER: "localhost:9092"
      KAFKA_TOPIC: "benchmark-messages"
```

### 7.2 Server Code Changes Required

The gRPC server must read the port from an environment variable instead of hardcoding `50051`:

```javascript
const port = process.env.GRPC_PORT || '50051';
server.bindAsync(`0.0.0.0:${port}`, grpc.ServerCredentials.createInsecure(), () => {
  server.start();
});
```

**On macOS:** Bind to `127.0.0.1` instead of `0.0.0.0` for reliable host networking:
```javascript
const host = process.platform === 'darwin' ? '127.0.0.1' : '0.0.0.0';
server.bindAsync(`${host}:${port}`, ...);
```

### 7.3 Running Both Bridge and Host Instances Simultaneously

The existing bridge-networked servers (ports 50051-50053) and the new host-networked servers (ports 50061-50063) can run concurrently:

```bash
# Start infrastructure
cd infra && docker compose up -d

# Start bridge-networked gRPC servers (existing)
cd grpc-server && docker compose up -d

# Start host-networked gRPC servers (new)
cd grpc-server && docker compose -f docker-compose.host.yml up -d
```

### 7.4 Client Changes

The benchmark client needs to know about the host-networked servers. Add them to the server list:
- Bridge servers: `localhost:50051`, `localhost:50052`, `localhost:50053`
- Host servers: `localhost:50061`, `localhost:50062`, `localhost:50063`

---

## 8. Key Takeaways

1. **`network_mode: host` is incompatible with `ports:` and `networks:`** — remove both from host-mode services
2. **Each host-mode instance needs a unique port** — pass via env var, use ports outside the bridge range
3. **Kafka connectivity switches from Docker DNS to localhost** — use `localhost:9092` instead of `benchmark-kafka:29092`
4. **macOS Docker Desktop has significant limitations** — must enable host networking in settings; performance gains are reduced by VM boundary; several known bugs
5. **Performance improvement is real but modest for gRPC** — expect 2-10% lower latency; throughput gains are most visible with many short-lived connections
6. **Keep bridge and host instances in separate compose files** — they have incompatible networking configurations
7. **The benchmark is most valid on Linux** — macOS results won't reflect production behavior due to the VM layer

---

## Sources

- Docker Docs: Host networking (https://docs.docker.com/network/host/)
- Docker Compose Issues: #8326, #10464, #3442, #7188 (port mapping incompatibility)
- Docker for Mac Issues: #7261, #7347, #7448, #7683, #7836 (macOS host networking bugs)
- Performance benchmarks from oneuptime.com, thelinuxcode.com, compilenrun.com, eastondev.com
- Apache HugeGraph Issue #2951 (host networking cross-platform problems)
