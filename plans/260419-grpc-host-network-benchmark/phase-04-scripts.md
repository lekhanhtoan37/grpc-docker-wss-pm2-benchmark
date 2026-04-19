# Phase 04: Update run-benchmark.sh and health-check.sh

**Effort:** 45m  
**Prerequisite:** Phase 02, Phase 03

## Objective

Update orchestration scripts to start host-networked gRPC containers, check their health, and include them in system info collection.

## Files to Modify

### 1. `run-benchmark.sh`

#### Change 1: Add host-networked gRPC startup after Step 3 (after line 44)

Insert new step between Step 3 (bridge gRPC) and Step 4 (WS servers):

```bash
echo ""
echo "--- Step 3b: Starting gRPC host-networked servers ---"
cd "$BASEDIR/grpc-server" && docker compose -f docker-compose.host.yml up -d --build
cd "$BASEDIR"
echo "Waiting 5s for host-networked gRPC containers..."
sleep 5
```

#### Change 2: Update Docker stats collection (line 122)

**Before:**
```bash
docker stats --no-stream grpc-server-1 grpc-server-2 grpc-server-3 2>/dev/null || true
```

**After:**
```bash
docker stats --no-stream grpc-server-1 grpc-server-2 grpc-server-3 grpc-host-1 grpc-host-2 grpc-host-3 2>/dev/null || true
```

#### Change 3: Add cleanup for host containers

Add cleanup function (or extend existing cleanup):

```bash
cleanup_host() {
  cd "$BASEDIR/grpc-server" && docker compose -f docker-compose.host.yml down 2>/dev/null || true
}
```

Call in trap or at end of script.

### 2. `health-check.sh`

#### Change 1: Add host gRPC port checks (after line 39)

```bash
echo ""
echo "--- gRPC Host-Networked Servers ---"
for port in 60051 60052 60053; do
  check "gRPC-host :$port" "nc -z localhost $port"
done
```

#### Change 2: Add Docker Desktop version check (optional, after Docker checks)

```bash
echo ""
echo "--- Docker Host Networking ---"
if [[ "$(uname)" == "Darwin" ]]; then
  DOCKER_VERSION=$(docker version --format '{{.Client.Version}}' 2>/dev/null || echo "unknown")
  check "Docker Desktop >= 4.34" "[[ '$DOCKER_VERSION' > '4.33' ]] || echo 'warn: host networking may not work'"
  echo "  ⚠ macOS: network_mode:host uses VM network, not true host networking"
fi
```

## Verification

1. Run `bash health-check.sh` — should check all ports including 60051-60053
2. Run `bash run-benchmark.sh` with short params: `WARMUP=5 DURATION=10 RUNS=1 bash run-benchmark.sh`
3. Verify host gRPC containers start and are included in docker stats output
4. Verify benchmark output includes all 3 groups

## Rollback

Revert script changes. Host containers can be stopped manually: `cd grpc-server && docker compose -f docker-compose.host.yml down`
