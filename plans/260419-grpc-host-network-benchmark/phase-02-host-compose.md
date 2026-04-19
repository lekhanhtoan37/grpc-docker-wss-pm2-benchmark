# Phase 02: Create docker-compose.host.yml

**Effort:** 30m  
**Prerequisite:** Phase 01

## Objective

Create new compose file for 3 gRPC instances with `network_mode: host`. No `ports:` (incompatible with host mode). No `networks:` (incompatible). Kafka via `localhost:9092`.

## File to Create

### `grpc-server/docker-compose.host.yml`

```yaml
services:
  grpc-host-1:
    build: .
    container_name: grpc-host-1
    network_mode: host
    environment:
      CONTAINER_ID: "host-1"
      GRPC_PORT: "60051"
      GRPC_HOST: "0.0.0.0"
      KAFKA_BROKER: "localhost:9092"
      KAFKA_TOPIC: "benchmark-messages"

  grpc-host-2:
    build: .
    container_name: grpc-host-2
    network_mode: host
    environment:
      CONTAINER_ID: "host-2"
      GRPC_PORT: "60052"
      GRPC_HOST: "0.0.0.0"
      KAFKA_BROKER: "localhost:9092"
      KAFKA_TOPIC: "benchmark-messages"

  grpc-host-3:
    build: .
    container_name: grpc-host-3
    network_mode: host
    environment:
      CONTAINER_ID: "host-3"
      GRPC_PORT: "60053"
      GRPC_HOST: "0.0.0.0"
      KAFKA_BROKER: "localhost:9092"
      KAFKA_TOPIC: "benchmark-messages"
```

**Key design decisions:**

- **No `ports:`** — incompatible with `network_mode: host`
- **No `networks:`** — incompatible with `network_mode: host`
- **No `depends_on:`** — Kafka is in a different compose file; managed by `run-benchmark.sh`
- **`GRPC_HOST: "0.0.0.0"`** — works on Linux; on macOS Docker Desktop may need `127.0.0.1` (see Phase 05)
- **Ports 60051-60053** — outside bridge range (50051-50053), no conflict
- **Kafka via `localhost:9092`** — host-mode containers see host network, not Docker DNS

## Verification

```bash
# Ensure infra is running
cd infra && docker compose up -d

# Start host-networked gRPC servers
cd grpc-server && docker compose -f docker-compose.host.yml up -d --build

# Verify 3 containers running
docker ps | grep grpc-host

# Verify ports listening
nc -z localhost 60051 && echo "OK" || echo "FAIL"
nc -z localhost 60052 && echo "OK" || echo "FAIL"
nc -z localhost 60053 && echo "OK" || echo "FAIL"

# Verify Kafka connectivity (check container logs)
docker logs grpc-host-1 2>&1 | grep "Kafka connected"

# Cleanup
docker compose -f docker-compose.host.yml down
```

## Rollback

`rm grpc-server/docker-compose.host.yml`
