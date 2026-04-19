#!/bin/bash
set -e

BROKER="${1:-192.168.0.5:9091}"
TOPIC="benchmark-messages"
PARTITIONS=12

echo "=== Creating topic ${TOPIC} with ${PARTITIONS} partitions ==="

/opt/kafka-benchmark/bin/kafka-topics.sh --create \
  --topic "$TOPIC" \
  --bootstrap-server "$BROKER" \
  --partitions "$PARTITIONS" \
  --replication-factor 1 \
  --config retention.ms=120000 \
  --config segment.bytes=104857600 \
  --config retention.bytes=1073741824 \
  --config max.message.bytes=10485760 \
  --config min.insync.replicas=1 \
  --if-not-exists

/opt/kafka-benchmark/bin/kafka-topics.sh --describe \
  --topic "$TOPIC" \
  --bootstrap-server "$BROKER"
