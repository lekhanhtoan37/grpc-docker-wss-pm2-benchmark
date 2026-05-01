---
title: "Phase 01: NATS Server Infrastructure"
phase: 1-of-5
status: pending
priority: P0
effort: 1h
blocked-by: none
blocks: [phase-02, phase-03, phase-04]
---

# Phase 01: NATS Server Infrastructure

## Context

- Existing infra: `infra/docker-compose.yml` (Zookeeper + Kafka)
- NATS needs its own container, exposed on port 4222 (client), 8222 (monitoring)
- Must work alongside Kafka without conflicts

## Overview

Deploy NATS server via Docker. Configure for benchmark workloads (1KB messages, high throughput). Add to existing infra compose file.

## Requirements

### Functional
- NATS server accessible at `localhost:4222`
- Monitoring endpoint at `localhost:8222` (/varz, /connz)
- Supports 1KB+ payload (default 1MB max is fine)
- Works with Docker bridge and host networking

### Non-Functional
- Zero persistence (no jetstream, no file storage)
- Minimal config — pure core NATS
- Low overhead — NATS should not be the bottleneck

## Architecture

```
infra/docker-compose.yml
├── zookeeper (existing)
├── kafka (existing)
└── nats (NEW)
    ├── port 4222:4222 (client)
    └── port 8222:8222 (monitoring)
```

## Implementation Steps

1. **Add NATS service to `infra/docker-compose.yml`**

   ```yaml
   nats:
     image: nats:2-alpine
     container_name: benchmark-nats
     ports:
       - "4222:4222"
       - "8222:8222"
     command:
       - "--max_payload=1048576"
       - "--write_deadline=10s"
       - "--net=0.0.0.0"
     healthcheck:
       test: ["CMD", "wget", "--spider", "-q", "http://localhost:8222/healthz"]
       interval: 5s
       timeout: 3s
       retries: 5
   ```

2. **Create NATS config file `infra/nats.conf`** (optional, for tuning)

   ```
   max_payload: 1MB
   write_deadline: "10s"
   max_connections: 65536
   max_subscriptions: 0
   max_control_line: 4096
   ```

3. **Add health check to `health-check.sh`**

   ```bash
   echo -n "NATS: "
   curl -sf http://localhost:8222/varz | jq -r '.status' 2>/dev/null || echo "NOT READY"
   ```

4. **Add health check to `health-check-1gb.sh`** (same pattern)

5. **Verify NATS connectivity**

   ```bash
   docker compose -f infra/docker-compose.yml up -d nats
   curl -sf http://localhost:8222/healthz
   ```

## Files

| File | Action |
|------|--------|
| `infra/docker-compose.yml` | MODIFY — add nats service |
| `infra/nats.conf` | CREATE — NATS tuning config |
| `health-check.sh` | MODIFY — add NATS check |
| `health-check-1gb.sh` | MODIFY — add NATS check |

## Todo Checklist

- [ ] Add NATS service to infra/docker-compose.yml
- [ ] Create infra/nats.conf
- [ ] Add NATS health check to health-check.sh
- [ ] Add NATS health check to health-check-1gb.sh
- [ ] Test: `docker compose up nats` → verify 4222 + 8222
- [ ] Test: `curl localhost:8222/healthz` returns OK

## Success Criteria

- `docker compose -f infra/docker-compose.yml up -d nats` starts NATS
- `curl localhost:8222/varz` returns server info
- `curl localhost:8222/healthz` returns OK
- Kafka services unaffected

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Port 4222 conflict | Low | Medium | Check `lsof -i :4222` first |
| NATS becomes bottleneck | Low | High | Monitor via /varz, increase write_deadline |
| Docker network issues | Low | Low | Test bridge + host networking |
