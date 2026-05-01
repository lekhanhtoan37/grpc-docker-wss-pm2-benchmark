# Scout Report: NATS Integration Points in run-benchmark-1gb.sh

## Key Findings

### 1. Pattern to Follow: Go WS Server
Go WS server is the closest analog to nats-worker:
- Both are Go binaries consuming from Kafka
- Both have Docker bridge/host + PM2 deployment modes
- Both publish to downstream transport (WS clients vs NATS subject)

Go WS steps in 1gb script:
- Step 4e: Docker bridge+host build/deploy (lines 402-418)
- Step 4f: PM2 build/deploy (lines 420-438)
- Step 6d: PM2 stop (521), Docker down (527-528), PM2 restart (555), Docker restart (562-563)
- Consumer group pattern: `go-ws-benchmark` (line 533)
- Cleanup: PM2 stop (610 not present — missing from cleanup!), Docker down (not in cleanup either)
- Scenario restart: Docker down (707-709), Docker up (717-719)
- Diagnostics: PM2 logs (646), Docker logs (634, 637)

### 2. Missing from cleanup() function
The cleanup() at lines 600-612 only handles grpc-server, uws-server, ws-benchmark, uws-benchmark.
It DOES NOT clean up go-ws-server Docker/PM2. This is a pre-existing gap.

### 3. NATS Worker Consumer Groups
From nats-worker/main.go line 26: `GROUP_ID = envStr("GROUP_ID", "nats-benchmark-worker-"+INSTANCE)`
- PM2 instances: `nats-benchmark-worker-0`, `-1`, `-2` (NODE_APP_INSTANCE from PM2)
- Docker host: `nats-benchmark-worker-host-1`, `-host-2`, `-host-3` (CONTAINER_ID)

### 4. Ports
- NATS server: 4222 (client), 8222 (monitoring)
- nats-worker PM2: 8095, 8096, 8097 (health endpoints)
- nats-worker Docker host: 60081, 60082, 60083 (health endpoints)

### 5. health-check-1gb.sh Already Has NATS
Lines 84-93 check NATS server + PM2 workers. Needs Docker host worker ports added.

### 6. Docker Bridge Network Issue
infra/docker-compose.yml does NOT define a named network. nats-worker/docker-compose.yml expects `external: true` network `backend`. This will fail unless:
- Option A: Add network definition to infra/docker-compose.yml
- Option B: Skip Docker bridge mode for nats-worker (researcher recommendation)
