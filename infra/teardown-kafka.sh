#!/bin/bash
set -e

echo "=== Stopping Benchmark Kafka systemd service ==="
sudo systemctl stop kafka-benchmark || true
sudo systemctl disable kafka-benchmark || true
sudo rm -f /etc/systemd/system/kafka-benchmark.service
sudo systemctl daemon-reload

echo "Kafka benchmark stopped. Data preserved at /home/kafka-benchmark/data"
echo "To fully remove: sudo rm -rf /opt/kafka-benchmark /home/kafka-benchmark"
echo "To remove user: sudo userdel kafka-bench"
