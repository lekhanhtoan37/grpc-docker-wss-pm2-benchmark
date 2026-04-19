# Phase 01: Make gRPC Server Port Configurable

**Effort:** 30m  
**Prerequisite:** None

## Objective

Allow gRPC server to listen on configurable port via `GRPC_PORT` env var. Required for host-networked containers where each instance needs a unique port.

## Files to Modify

### 1. `grpc-server/server.js` (line 10)

**Before:**
```javascript
const PORT = 50051;
```

**After:**
```javascript
const PORT = parseInt(process.env.GRPC_PORT || "50051", 10);
```

Also update the bind address for macOS compatibility. Line 47:

**Before:**
```javascript
server.bindAsync(
    `0.0.0.0:${PORT}`,
```

**After:**
```javascript
const HOST = process.env.GRPC_HOST || "0.0.0.0";
// ... later:
server.bindAsync(
    `${HOST}:${PORT}`,
```

### 2. `grpc-server/Dockerfile` (line 7)

**Before:**
```
EXPOSE 50051
```

**After:**
```
ENV GRPC_PORT=50051
EXPOSE ${GRPC_PORT}
```

Note: `EXPOSE` is documentation only in Docker; `ARG`/`ENV` substitution works in Dockerfile but not for EXPOSE in all Docker versions. Alternative: just remove the EXPOSE line since it's not required (port mapping in compose handles it).

## Verification

1. Start a single gRPC container with `GRPC_PORT=60051`:
   ```bash
   cd grpc-server && docker compose run --rm -e GRPC_PORT=60051 -e CONTAINER_ID=test grpc-server-1
   ```
2. Verify log shows `Listening on 0.0.0.0:60051`
3. Connect with gRPC client to `localhost:60051` — should succeed

## Rollback

Revert `PORT` to hardcoded `50051`. No new files created.
