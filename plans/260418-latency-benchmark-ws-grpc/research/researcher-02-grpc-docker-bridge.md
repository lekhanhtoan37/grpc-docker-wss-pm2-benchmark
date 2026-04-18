# Research: gRPC Server-Streaming + Docker Bridge (3 Containers)

**Date:** 2026-04-18
**Scope:** NodeJS gRPC server-streaming with `@grpc/grpc-js`, 3 identical containers, bridge network, Kafka on host

---

## Key Findings

- **`@grpc/grpc-js`** is the official pure-JS gRPC implementation. No native deps, works in any Docker image.
- Server-streaming: client sends 1 request, server calls `call.write()` repeatedly, ends with `call.end()`.
- Must bind server to `0.0.0.0` inside container (not `127.0.0.1`) or host cannot reach it.
- Docker user-defined bridge networks provide automatic DNS resolution via container/service name.
- Default bridge network does NOT support DNS by container name — always define a custom bridge network.
- gRPC uses HTTP/2 — single TCP connection multiplexed. No per-message connection overhead.
- Containers on same bridge network communicate directly (no port mapping needed between them).
- Port mapping only needed to expose gRPC to host machine for testing.
- `host.docker.internal` resolves to host from container on Docker Desktop (macOS/Windows). Use for Kafka on host.
- gRPC server-streaming is ideal for Kafka consumer → client push pattern. Each client gets its own stream; server consumes Kafka topics and writes messages to active streams.

---

## 1. gRPC Server-Streaming in NodeJS

### Proto Definition

```protobuf
// event.proto
syntax = "proto3";
package event;

service EventService {
  rpc StreamEvents(EventRequest) returns (stream EventResponse);
}

message EventRequest {
  string topic = 1;
}

message EventResponse {
  string id = 1;
  string payload = 2;
  int64 timestamp = 3;
}
```

### Server Implementation (Kafka → gRPC stream)

```js
const grpc = require('@grpc/grpc-js');
const protoLoader = require('@grpc/proto-loader');
const { Kafka } = require('kafkajs');

const packageDef = protoLoader.loadSync('event.proto', {
  keepCase: true,
  longs: String,
  enums: String,
  defaults: true,
  oneofs: true,
});
const eventProto = grpc.loadPackageDefinition(packageDef).event;

const activeStreams = new Set();

function streamEvents(call) {
  const topic = call.request.topic;
  activeStreams.add(call);
  console.log(`Client connected, streaming topic: ${topic}`);

  call.on('cancelled', () => {
    activeStreams.delete(call);
    console.log('Client disconnected');
  });
}

// Kafka consumer pushes to all active gRPC streams
async function startKafkaConsumer() {
  const kafka = new Kafka({
    brokers: [`${process.env.KAFKA_HOST || 'host.docker.internal'}:9092`],
  });
  const consumer = kafka.consumer({ groupId: 'grpc-stream-' + process.env.CONTAINER_ID });
  await consumer.connect();
  await consumer.subscribe({ topic: 'test-events', fromBeginning: false });

  await consumer.run({
    eachMessage: async ({ message }) => {
      const payload = JSON.parse(message.value.toString());
      for (const call of activeStreams) {
        try {
          call.write({
            id: message.key?.toString() || Date.now().toString(),
            payload: JSON.stringify(payload),
            timestamp: Date.now(),
          });
        } catch (e) {
          activeStreams.delete(call);
        }
      }
    },
  });
}

const server = new grpc.Server();
server.addService(eventProto.EventService.service, { streamEvents });
server.bindAsync('0.0.0.0:50051', grpc.ServerCredentials.createInsecure(), (err, port) => {
  if (err) { console.error(err); return; }
  server.start();
  console.log(`gRPC server on 0.0.0.0:${port}`);
  startKafkaConsumer().catch(console.error);
});
```

### Client (for testing from host)

```js
const grpc = require('@grpc/grpc-js');
const protoLoader = require('@grpc/proto-loader');

const packageDef = protoLoader.loadSync('event.proto', { keepCase: true, longs: String, enums: String });
const eventProto = grpc.loadPackageDefinition(packageDef).event;

const client = new eventProto.EventService('localhost:50051', grpc.ChannelCredentials.createInsecure());
const stream = client.streamEvents({ topic: 'test-events' });

stream.on('data', (resp) => console.log('Event:', resp.payload, 'latency:', Date.now() - Number(resp.timestamp), 'ms'));
stream.on('end', () => console.log('Stream ended'));
stream.on('error', (err) => console.error('Stream error:', err));
```

---

## 2. Dockerfile

```dockerfile
FROM node:20-alpine
WORKDIR /app
COPY package*.json ./
RUN npm ci --only=production
COPY . .
EXPOSE 50051
ENV CONTAINER_ID=default
CMD ["node", "server.js"]
```

---

## 3. Docker Compose (3 gRPC servers + bridge network)

```yaml
version: "3.8"

services:
  grpc-server-1:
    build: .
    container_name: grpc-server-1
    environment:
      CONTAINER_ID: "1"
      KAFKA_HOST: "host.docker.internal"
    ports:
      - "50051:50051"
    networks:
      - grpc-net
    extra_hosts:
      - "host.docker.internal:host-gateway"

  grpc-server-2:
    build: .
    container_name: grpc-server-2
    environment:
      CONTAINER_ID: "2"
      KAFKA_HOST: "host.docker.internal"
    ports:
      - "50052:50051"
    networks:
      - grpc-net
    extra_hosts:
      - "host.docker.internal:host-gateway"

  grpc-server-3:
    build: .
    container_name: grpc-server-3
    environment:
      CONTAINER_ID: "3"
      KAFKA_HOST: "host.docker.internal"
    ports:
      - "50053:50051"
    networks:
      - grpc-net
    extra_hosts:
      - "host.docker.internal:host-gateway"

networks:
  grpc-net:
    driver: bridge
```

### Key config notes:
- Each container maps unique host port (50051/50052/50053) → internal 50051
- `extra_hosts: host.docker.internal:host-gateway` lets containers reach Kafka on host
- All 3 containers on same `grpc-net` bridge network — can reach each other by container name
- `KAFKA_HOST=host.docker.internal` used by Kafka consumer inside container

---

## 4. Architecture Diagram

```
┌─────────────────────────────────────────────────┐
│  HOST                                           │
│                                                 │
│  ┌──────────┐    Kafka on host:9092             │
│  │Test Tool │                                    │
│  │(gRPC     │──► localhost:50051 ─┐             │
│  │ client)  │──► localhost:50052 ─┼──► NAT ──►  │
│  │          │──► localhost:50053 ─┘             │
│  └──────────┘                                   │
│       │                                         │
└───────│─────────────────────────────────────────┘
        │ (port mapping)
        ▼
┌───────────────────── Docker Bridge: grpc-net ────────────────────┐
│                                                                  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐           │
│  │ grpc-server-1│  │ grpc-server-2│  │ grpc-server-3│           │
│  │ :50051       │  │ :50051       │  │ :50051       │           │
│  │ KafkaConsumer│  │ KafkaConsumer│  │ KafkaConsumer│           │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘           │
│         │                 │                 │                    │
│         └─────────────────┴─────────────────┘                   │
│                           │                                      │
│                    host.docker.internal:9092                     │
└───────────────────────────│──────────────────────────────────────┘
                            ▼
                    ┌───────────────┐
                    │  Kafka Broker │
                    │  host:9092    │
                    └───────────────┘
```

---

## 5. Gotchas

### Docker Bridge Networking
- **Default bridge = no DNS.** Always use user-defined bridge networks. Default bridge only supports IP-based communication between containers.
- **`0.0.0.0` binding required.** gRPC server must bind `0.0.0.0:50051` inside container, NOT `127.0.0.1` or `localhost`.
- **NAT overhead.** Bridge network adds NAT layer for host→container communication. Adds ~0.1-0.5ms latency per hop vs host network. For latency benchmarks, this overhead is measurable.
- **`host.docker.internal` on Linux.** Only works by default on Docker Desktop (macOS/Windows). On Linux, use `extra_hosts: ["host.docker.internal:host-gateway"]` in compose.
- **Port conflicts.** All 3 containers use same internal port (50051) — fine on bridge network. Must map to different host ports.

### gRPC Streaming
- **`call.on('cancelled')` cleanup.** Always listen for cancel event to clean up intervals, Kafka subscriptions, etc. Leaked streams = memory leak.
- **No built-in backpressure from `call.write()`.** `call.write()` returns boolean — if `false`, drain must be handled. For high-throughput Kafka→gRPC, use `call.write()` return value or risk buffer overflow.
- **Stream is per-client.** Each gRPC client gets its own `ServerWritableStream`. Must track active streams in a Set/Map for fan-out from Kafka.
- **HTTP/2 multiplexing.** Single TCP connection per client. Multiple streams multiplexed. Good for performance, but means one TCP connection failure drops all streams.

### Kafka + Docker
- **3 consumers, same group → partition rebalancing.** If all 3 containers share Kafka group ID, messages split across them. Use unique group IDs per container to have each consume all messages.
- **Kafka `advertised.listeners`.** Kafka broker must advertise a hostname reachable from containers. `host.docker.internal` usually works.

---

## 6. Recommendations

1. **Use unique Kafka consumer group IDs per container** (`grpc-stream-${CONTAINER_ID}`) so each server gets all events for fan-out to its connected clients.
2. **Use `grpcurl` for quick testing** from host: `grpcurl -plaintext localhost:50051 event.EventService/StreamEvents`
3. **Add health checks** via `grpc_health_probe` in Dockerfile for proper compose health checking.
4. **For latency benchmarks**, note that bridge NAT adds ~0.1-0.5ms. Document this as baseline overhead. Compare against host-network mode if needed.
5. **Handle `call.write()` backpressure** — check return value and drain before continuing to avoid memory issues under load.
6. **Keep-alive settings** — gRPC defaults may kill idle streams. Configure keepalive:
   ```js
   server.bindAsync('0.0.0.0:50051',
     grpc.ServerCredentials.createInsecure(),
     () => server.start()
   );
   // Client-side keepalive:
   const client = new EventService('localhost:50051',
     grpc.ChannelCredentials.createInsecure(), {
       'grpc.keepalive_time_ms': 30000,
       'grpc.keepalive_timeout_ms': 10000,
     }
   );
   ```
7. **Container DNS** — on `grpc-net`, containers reach each other as `grpc-server-1`, `grpc-server-2`, `grpc-server-3`. Host cannot use these names — must use `localhost:50051/50052/50053`.

---

## References

- gRPC NodeJS `@grpc/grpc-js` docs: https://grpc.io/grpc/node/
- Docker bridge networking: https://docs.docker.com/network/drivers/bridge/
- gRPC streaming patterns: https://webcoderspeed.com/blog/scaling/grpc-streaming-2026
- Docker Compose networking + DNS: https://www.grizzlypeaksoftware.com/library/docker-networking-deep-dive-sgdzzflp
- ServerWritableStream API: https://grpc.io/grpc/node/grpc-ServerWritableStream.html

---

## Unresolved Questions

- **Exact latency overhead** of Docker bridge NAT for gRPC streaming — needs empirical measurement. Literature suggests 0.1-0.5ms.
- **gRPC vs WebSocket latency** under same Docker bridge conditions — requires benchmark comparison.
- **PM2 inside container** — if needed, PM2 should manage Node processes inside each container. Adds complexity but enables clustering per container.
