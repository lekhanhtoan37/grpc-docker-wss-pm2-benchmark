# Phase 1: Kafka Setup + Producer App

**Prerequisites**: Kafka running on host at `:9092`

---

## Tasks

### 1.1 Create Kafka Topic

```bash
# If Kafka has CLI tools
kafka-topics.sh --create \
  --topic benchmark-messages \
  --partitions 1 \
  --replication-factor 1 \
  --if-not-exists \
  --bootstrap-server localhost:9092
```

- **1 partition** (Approach B: all consumers unique groups, each gets all messages)
- `retention.ms=3600000` (1 hour, enough for benchmark)
- `cleanup.policy=delete`

### 1.2 Create Producer App

**Directory**: `producer/`

**package.json**:
```json
{
  "name": "benchmark-producer",
  "version": "1.0.0",
  "private": true,
  "scripts": {
    "start": "node producer.js"
  },
  "dependencies": {
    "kafkajs": "^2.2.4"
  }
}
```

**producer.js**:
- Connect to `localhost:9092`
- Send 100 msg/sec (1 msg every 10ms via `setInterval`)
- Each message payload ~1KB:
  ```json
  {
    "timestamp": 1713456789123,
    "seq": 42000,
    "data": "x".repeat(1024 - ~40 bytes overhead)
  }
  ```
- Use `acks: 1` (leader ack only)
- Log every 1000th message with current send rate
- Handle SIGINT for graceful shutdown
- Track and log producer-side send latency (time from `producer.send()` call to ack)

### 1.3 Producer Config Details

| Config | Value | Reason |
|--------|-------|--------|
| Rate | 100 msg/s | Task requirement |
| Payload | ~1KB JSON | Task requirement |
| Interval | 10ms | 1000ms / 100 = 10ms between messages |
| Acks | 1 | Leader only, fast |
| Topic | `benchmark-messages` | Shared topic |
| Partition | 1 | Approach B: all consumers unique groups |
| Key | `msg-{seq}` | Sequential, deterministic |

### 1.4 Verification

```bash
# Start producer
cd producer && npm install && npm start

# Verify messages in Kafka (separate terminal)
kafka-console-consumer.sh --topic benchmark-messages --from-beginning --bootstrap-server localhost:9092
```

Expected: ~100 messages/sec appearing, each ~1KB JSON with `timestamp`, `seq`, `data` fields.

---

## Gotchas

- **setInterval drift**: `setInterval(fn, 10)` drifts over time. For exact 100 msg/s, track elapsed time and send accordingly. For benchmark purposes, drift is acceptable (<1% over 5 min).
- **Backpressure**: If `producer.send()` takes longer than 10ms, messages queue. Monitor producer lag. At 100 msg/s with 1KB payload, this is unlikely.
- **Kafka not running**: Producer should fail fast with clear error if Kafka unreachable. Don't silently retry forever.
- **Topic auto-creation**: If Kafka has `auto.create.topics.enable=true`, topic created on first message. Explicit creation preferred for partition control.

---

## Acceptance Criteria

- [ ] Topic `benchmark-messages` exists with 1 partition
- [ ] Producer sends 100 msg/sec, each ~1KB
- [ ] Messages contain `timestamp` (epoch ms), `seq` (incrementing), `data` (padding)
- [ ] Producer logs send rate every 1000 messages
- [ ] Graceful shutdown on SIGINT
