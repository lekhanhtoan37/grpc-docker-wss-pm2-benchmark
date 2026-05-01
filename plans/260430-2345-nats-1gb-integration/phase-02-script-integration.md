---
phase: 2
name: "Script Integration — nats-worker + All Updates"
status: pending
priority: P0
effort: 2h
blocked-by: [phase-01]
blocks: [phase-03]
---

## Overview

Add nats-worker deployment (PM2 + Docker host) and update all downstream steps in `run-benchmark-1gb.sh`: consumer group cleanup, scenario restarts, diagnostics, system info collection, and cleanup trap. Also update `health-check-1gb.sh`.

## Requirements

1. Deploy nats-worker via PM2 (3 instances, ports 8095-8097)
2. Deploy nats-worker via Docker host (3 instances, ports 60081-60083)
3. Add `nats-benchmark-worker` to consumer group cleanup
4. Add nats-worker restart between benchmark scenarios
5. Add nats-worker to cleanup trap
6. Update health-check-1gb.sh with NATS systemd + Docker host worker checks
7. Add nats-worker diagnostics (PM2 logs + Docker logs)

## Implementation Steps

### Step 1: Insert Step 4g — nats-worker PM2 (after line 438)

Follow the exact pattern of Step 4f (Go WS PM2):

1. `cd nats-worker && go mod tidy && go build`
2. `run_pm2 delete nats-benchmark` (if exists)
3. `run_pm2 start ecosystem.config.js`
4. Verify ports 8095/8096/8097

### Step 2: Insert Step 4h — nats-worker Docker host (after Step 4g)

Follow the exact pattern of Step 4e (Go WS Docker):

1. `docker compose -f docker-compose.host.yml down` (cleanup)
2. `docker compose -f docker-compose.host.yml build --no-cache`
3. `docker compose -f docker-compose.host.yml up -d`
4. Verify ports 60081/60082/60083

### Step 3: Update Step 6b — Container consumer status (after line 482)

Add nats-worker host containers to the consumer readiness check loop:

```
nats-worker-host-1 nats-worker-host-2 nats-worker-host-3
```

### Step 4: Update Step 6d — Topic reset (lines 516-567)

Four additions:
1. **Line 521**: Add `run_pm2 stop nats-benchmark 2>/dev/null || true`
2. **Line 528**: Add `cd "$BASEDIR/nats-worker" && docker compose -f docker-compose.host.yml down 2>/dev/null || true`
3. **Consumer group grep** (line 533): Extend regex to `|nats-benchmark-worker`
4. **Line 555**: Add `cd "$BASEDIR/nats-worker" && run_pm2 start ecosystem.config.js`
5. **Line 563**: Add `cd "$BASEDIR/nats-worker" && docker compose -f docker-compose.host.yml up -d`

### Step 5: Update Step 7 — Scenario restart (lines 699-723)

Add nats-worker Docker host to the container restart cycle:

```bash
cd "$BASEDIR/nats-worker"
docker compose -f docker-compose.host.yml down 2>/dev/null || true
sleep 2
docker compose -f docker-compose.host.yml up -d
cd "$BASEDIR"
```

Add nats-worker PM2 logs to diagnostics (after line 646):

```bash
echo "  === nats-worker (pm2 logs, last 10 lines) ==="
run_pm2 logs nats-benchmark --nostream --lines 10 2>&1 | sed 's/^/    /' || true
```

Add nats-worker host containers to Docker logs loop:

```
nats-worker-host-1 nats-worker-host-2 nats-worker-host-3
```

### Step 6: Update cleanup() trap (lines 600-612)

Add before `cd "$BASEDIR"`:

```bash
cd "$BASEDIR/nats-worker" && docker compose -f docker-compose.host.yml down 2>/dev/null || true
run_pm2 stop nats-benchmark 2>/dev/null || true
```

Also fix pre-existing gap: add missing go-ws-server cleanup (Docker down + PM2 stop).

### Step 7: Update Step 8 — System info (lines 730-763)

Add to the system info block:
- NATS server: `curl -sf http://localhost:8222/varz | jq '{version, connections, subscriptions}'`
- nats-worker PM2: `run_pm2 show nats-benchmark`
- nats-worker Docker host stats: add to `docker stats --no-stream` list

### Step 8: Update health-check-1gb.sh

Add NATS systemd checks (after NATS block, line 86):

```bash
check "NATS benchmark service active" "systemctl is-active nats-benchmark"
check "NATS port 127.0.0.1:4222" "nc -z 127.0.0.1 4222"
check "NATS monitor 127.0.0.1:8222" "nc -z 127.0.0.1 8222"
```

Add Docker host worker checks (after NATS Workers block, line 93):

```bash
echo ""
echo "--- NATS Host Workers ---"
for port in 60081 60082 60083; do
  check "NATS host worker :$port" "nc -z 127.0.0.1 $port"
done
```

## Files Affected

| File | Change |
|------|--------|
| `run-benchmark-1gb.sh` | Steps 4g, 4h, 6b, 6d, 7, 8, cleanup() |
| `health-check-1gb.sh` | NATS systemd checks + Docker host worker checks |

## Todo

- [ ] Add Step 4g (nats-worker PM2) after Step 4f
- [ ] Add Step 4h (nats-worker Docker host) after Step 4g
- [ ] Update Step 6b consumer status to include nats-worker containers
- [ ] Update Step 6d stop/consumer-group/restart for nats-worker
- [ ] Update Step 7 scenario restart + diagnostics for nats-worker
- [ ] Update cleanup() trap for nats-worker + fix go-ws-server gap
- [ ] Update Step 8 system info for NATS + nats-worker metrics
- [ ] Update health-check-1gb.sh with NATS systemd + host worker checks

## Success Criteria

- `run-benchmark-1gb.sh` completes without errors (dry-run or short duration)
- `health-check-1gb.sh` shows NATS + all nats-worker ports as PASS
- nats-worker PM2 instances show in `pm2 list`
- nats-worker Docker host containers show in `docker ps`
- Consumer groups `nats-benchmark-worker-*` appear after workers join
- Scenario restart correctly cycles nats-worker containers
- cleanup() stops all nats-worker processes on exit

## Risk Assessment

| Risk | Impact | Mitigation |
|------|--------|-----------|
| nats-worker binary build fails | PM2 step fails | Build error is caught by `set -e`; go mod tidy before build |
| Docker host containers can't reach Kafka | Workers log errors, no NATS messages | Same iptables rule (Step 3) allows Docker→Kafka connectivity |
| Port conflict 8095-8097 or 60081-60083 | Worker fails to start | Documented port ranges; `ss -ntpl` check in verification |
| Missing `backend` Docker network | Docker bridge compose fails (if accidentally used) | We skip bridge mode entirely; only use host compose file |
