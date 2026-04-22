---
phase: 2
title: "Docker Configuration"
description: "Create Dockerfile, docker-compose.yml (bridge), docker-compose.host.yml (host) for uws-server"
depends_on: [phase-01]
---

# Phase 2: Docker Configuration

## Goal

Create Docker configs for uWS server in bridge and host network modes, following the exact pattern from `grpc-server/`.

## Files to Create

### 1. `uws-server/Dockerfile`

```dockerfile
FROM node:20-bookworm-slim

WORKDIR /app
COPY package*.json ./
RUN npm install --omit=dev
COPY server.js .
ENV PORT=8091
ENV CONTAINER_ID=default
CMD ["node", "--max-old-space-size=16384", "server.js"]
```

**Notes:**
- `node:20-bookworm-slim` — Debian-based (glibc), required for uWS native binary
- `git` is included in `node:20-bookworm-slim`, so `npm install` from GitHub works
- No Alpine — uWS binaries require glibc
- Matches `grpc-server/Dockerfile` pattern exactly

### 2. `uws-server/docker-compose.yml` (Bridge Network)

```yaml
services:
  uws-server-1:
    build: .
    container_name: uws-server-1
    environment:
      CONTAINER_ID: "1"
      KAFKA_BROKER: "192.168.0.9:9091"
      KAFKA_TOPIC: "benchmark-messages"
      PORT: "8091"
    ports:
      - "50061:8091"

  uws-server-2:
    build: .
    container_name: uws-server-2
    environment:
      CONTAINER_ID: "2"
      KAFKA_BROKER: "192.168.0.9:9091"
      KAFKA_TOPIC: "benchmark-messages"
      PORT: "8091"
    ports:
      - "50062:8091"

  uws-server-3:
    build: .
    container_name: uws-server-3
    environment:
      CONTAINER_ID: "3"
      KAFKA_BROKER: "192.168.0.9:9091"
      KAFKA_TOPIC: "benchmark-messages"
      PORT: "8091"
    ports:
      - "50063:8091"
```

**Pattern:** Same as `grpc-server/docker-compose.yml` — 3 containers, each with unique `CONTAINER_ID` → unique Kafka consumer group `uws-bridge-${CONTAINER_ID}`. Host ports 50061-50063 map to container port 8091.

### 3. `uws-server/docker-compose.host.yml` (Host Network)

```yaml
services:
  uws-host-1:
    build: .
    container_name: uws-host-1
    network_mode: host
    environment:
      CONTAINER_ID: "host-1"
      PORT: "60061"
      KAFKA_BROKER: "192.168.0.9:9091"
      KAFKA_TOPIC: "benchmark-messages"

  uws-host-2:
    build: .
    container_name: uws-host-2
    network_mode: host
    environment:
      CONTAINER_ID: "host-2"
      PORT: "60062"
      KAFKA_BROKER: "192.168.0.9:9091"
      KAFKA_TOPIC: "benchmark-messages"

  uws-host-3:
    build: .
    container_name: uws-host-3
    network_mode: host
    environment:
      CONTAINER_ID: "host-3"
      PORT: "60063"
      KAFKA_BROKER: "192.168.0.9:9091"
      KAFKA_TOPIC: "benchmark-messages"
```

**Pattern:** Same as `grpc-server/docker-compose.host.yml` — `network_mode: host`, unique `PORT` per container. Kafka group: `uws-host-host-1`, `uws-host-host-2`, `uws-host-host-3`.

## Port Summary (After Phase 2)

| Service | Mode | Ports |
|---------|------|-------|
| ws-server | PM2 cluster | 8090 |
| uws-server | PM2 cluster | **8091** |
| gRPC server | Docker bridge | 50051, 50052, 50053 |
| uws-server | Docker bridge | **50061, 50062, 50063** |
| gRPC server | Docker host | 60051, 60052, 60053 |
| uws-server | Docker host | **60061, 60062, 60063** |

## Acceptance Criteria

- [ ] `docker compose build` succeeds in `uws-server/`
- [ ] Bridge containers start: `docker compose up -d` creates 3 containers
- [ ] Host containers start: `docker compose -f docker-compose.host.yml up -d` creates 3 containers
- [ ] Each container logs "Listening on :<port>" and "Kafka consumer connected"
- [ ] Health check: `curl http://localhost:50061/health` returns `ok`
- [ ] Health check: `curl http://localhost:60061/health` returns `ok`
- [ ] Kafka consumer groups are unique per container (check via `kafka-consumer-groups.sh`)
- [ ] WS clients can connect to all 6 endpoints
