---
phase: 4
title: "Benchmark Script Update"
description: "Update run-benchmark-1gb.sh to build, start, and health-check uWS servers alongside existing ones"
depends_on: [phase-01, phase-02, phase-03]
---

# Phase 4: Benchmark Script Update

## Goal

Update `run-benchmark-1gb.sh` to manage uWS servers in all 3 modes (PM2, Docker bridge, Docker host) alongside existing servers.

## File to Modify

### `run-benchmark-1gb.sh`

#### Change 1: Start uWS PM2 workers (after Step 4 WS PM2 section, ~line 354)

Insert after the existing WS PM2 start block:

```bash
# ──────────────────────────────────────────────
# Step 4b: Start uWS servers (PM2)
# ──────────────────────────────────────────────
echo ""
echo "--- Step 4b: Start uWS servers (PM2) ---"
cd "$BASEDIR/uws-server"
run_as_user npm install --silent
run_pm2 describe uws-benchmark &>/dev/null && run_pm2 delete uws-benchmark 2>/dev/null || true
run_pm2 start ecosystem.config.js
cd "$BASEDIR"
echo "Waiting 5s for uWS workers..."
sleep 5
```

#### Change 2: Build + start uWS Docker containers (after gRPC Docker section, ~line 342)

Insert after gRPC host containers are up:

```bash
# ──────────────────────────────────────────────
# Step 4c: Build + start uWS Docker
# ──────────────────────────────────────────────
echo ""
echo "--- Step 4c: Build + start uWS servers ---"
cd "$BASEDIR/uws-server"
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
```

#### Change 3: Update container readiness check (~line 380-388)

Add uWS containers to the readiness check loop:

```bash
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
```

#### Change 4: Update server diagnostics (~line 480-486)

Add uWS containers to the per-run diagnostics:

```bash
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
```

#### Change 5: Update cleanup function (~line 460-468)

```bash
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
```

#### Change 6: Update container restart between runs (~line 496-506)

```bash
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
```

#### Change 7: Update Docker stats collection (~line 531)

```bash
docker stats --no-stream \
  grpc-server-1 grpc-server-2 grpc-server-3 \
  grpc-host-1 grpc-host-2 grpc-host-3 \
  uws-server-1 uws-server-2 uws-server-3 \
  uws-host-1 uws-host-2 uws-host-3 \
  2>/dev/null || true
```

#### Change 8: Update PM2 metrics collection (~line 528)

```bash
echo "=== PM2 Metrics (WS) ==="
run_pm2 show ws-benchmark 2>/dev/null || true
echo ""
echo "=== PM2 Metrics (uWS) ==="
run_pm2 show uws-benchmark 2>/dev/null || true
```

## Optional: Update health-check-1gb.sh

If `health-check-1gb.sh` exists and checks server endpoints, add uWS health checks:

```bash
# uWS health checks
for port in 8091 50061 50062 50063 60061 60062 60063; do
  if curl -sf "http://127.0.0.1:$port/health" >/dev/null 2>&1; then
    echo "  uWS :$port: OK"
  else
    echo "  uWS :$port: FAIL"
  fi
done
```

## Summary of Script Changes

| Section | Action | Lines Affected |
|---------|--------|---------------|
| Step 4b | Add uWS PM2 start | New block (~10 lines) |
| Step 4c | Add uWS Docker build + start | New block (~15 lines) |
| Container readiness | Add 6 uWS container names | Modify loop |
| Server diagnostics | Add 6 uWS containers + PM2 logs | Modify loop |
| Cleanup | Add uWS Docker down + PM2 stop | Modify function |
| Run restart | Add uWS Docker down + up | Modify block |
| Docker stats | Add 6 uWS container names | Modify command |
| PM2 metrics | Add uws-benchmark show | Modify command |

## Acceptance Criteria

- [ ] `run-benchmark-1gb.sh` starts all uWS servers (PM2 + Docker bridge + Docker host)
- [ ] Health checks pass for all uWS endpoints
- [ ] Container readiness checks include uWS containers
- [ ] Go client connects to all 6 groups
- [ ] Benchmark runs complete with uWS groups in results
- [ ] Cleanup stops all uWS servers
- [ ] Between-run restart includes uWS containers
- [ ] System info collection includes uWS Docker stats and PM2 metrics
