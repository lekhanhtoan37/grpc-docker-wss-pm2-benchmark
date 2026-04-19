#!/bin/bash
set -e

echo "=== Stopping Kafka systemd service ==="
sudo systemctl stop kafka || true
sudo systemctl disable kafka || true
sudo rm -f /etc/systemd/system/kafka.service
sudo systemctl daemon-reload

echo "Kafka stopped. Data preserved at /var/lib/kafka/data"
echo "To fully remove: sudo rm -rf /opt/kafka /var/lib/kafka/data"
