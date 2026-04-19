#!/bin/bash
set -e

BROKER="${1:-127.0.0.1:9091}"
TOPIC="benchmark-messages"
PARTITIONS=12

echo "=== Creating topic ${TOPIC} with ${PARTITIONS} partitions ==="

/opt/kafka-benchmark/bin/kafka-topics.sh --create \
  --topic "$TOPIC" \
  --bootstrap-server "$BROKER" \
  --partitions "$PARTITIONS" \
  --replication-factor 1 \
  --config retention.ms=86400000 \
  --config segment.bytes=1073741824 \
  --config retention.bytes=-1 \
  --config max.message.bytes=10485760 \
  --config min.insync.replicas=1 \
  --if-not-exists

/opt/kafka-benchmark/bin/kafka-topics.sh --describe \
  --topic "$TOPIC" \
  --bootstrap-server "$BROKER"
