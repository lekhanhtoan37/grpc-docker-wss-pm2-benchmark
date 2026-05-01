---
phase: 1
name: "NATS Server Systemd Service"
status: pending
priority: P0
effort: 1.5h
blocked-by: []
blocks: [phase-02]
---

## Overview

Install NATS server as a systemd service on 192.168.0.9, mirroring the Kafka systemd pattern. Add Step 1c to `run-benchmark-1gb.sh` between Step 1b (Kafka reconfigure) and Step 2 (create topic).

## Requirements

1. Download NATS server binary (single static binary, v2.11.4)
2. Create `nats-bench` system user
3. Write config file to `/opt/nats-benchmark/nats.conf`
4. Install systemd service `nats-benchmark`
5. Idempotent — skip if already running
6. Monitoring endpoint on port 8222 (required by health-check-1gb.sh)

## Implementation Steps

### Step 1: Add NATS constants to script header

After the Kafka constants block (line ~49), add:

```bash
NATS_VERSION="2.11.4"
NATS_DIR="/opt/nats-benchmark"
NATS_USER="nats-bench"
NATS_SERVICE="nats-benchmark"
```

### Step 2: Insert Step 1c after line 294

Insert the NATS server setup block between Step 1b (Kafka verify port) and Step 2 (create topic). Follow the idempotent pattern from Step 1 (Kafka):

1. Check `systemctl is-active nats-benchmark` + `nc -z 127.0.0.1 4222`
2. If running → skip
3. If not → download binary, create user, write config, install service, start, verify

### Step 3: NATS config file

```
listen: "0.0.0.0:4222"
monitor: "0.0.0.0:8222"
max_payload: 1MB
write_deadline: "10s"
max_connections: 65536
max_subscriptions: 0
max_control_line: 4096
```

### Step 4: systemd service unit

```ini
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

## Files Affected

| File | Change |
|------|--------|
| `run-benchmark-1gb.sh` | Add NATS constants (header) + Step 1c block (after line 294) |

## Todo

- [ ] Add NATS_VERSION, NATS_DIR, NATS_USER, NATS_SERVICE constants after line 49
- [ ] Insert Step 1c NATS server setup block after line 294
- [ ] Test idempotent skip (run twice — second should say "already running")

## Success Criteria

- `systemctl is-active nats-benchmark` returns active
- `nc -z 127.0.0.1 4222` succeeds (client port)
- `nc -z 127.0.0.1 8222` succeeds (monitor port)
- `curl -sf http://localhost:8222/varz | jq .version` returns NATS version
- Re-running Step 1c prints "already running" and skips

## Risk Assessment

| Risk | Impact | Mitigation |
|------|--------|-----------|
| NATS binary download fails | Script exits | curl `-f` flag + explicit error check |
| Port 4222/8222 in use | Startup fails | Check with `ss -ntpl` before install; document conflict resolution |
| systemd service fails to start | Benchmark blocked | Log journalctl output on failure; `Restart=on-failure` for auto-recovery |
