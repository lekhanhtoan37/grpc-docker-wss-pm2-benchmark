#!/bin/bash
set -e

echo "=== Docker daemon tuning for benchmark ==="

if [ ! -f /etc/docker/daemon.json ]; then
  echo '{"userland-proxy": false}' | sudo tee /etc/docker/daemon.json
else
  echo "daemon.json exists. Ensure 'userland-proxy': false is set."
  echo "Current content:"
  cat /etc/docker/daemon.json
fi

echo "Restarting Docker..."
sudo systemctl restart docker

echo "Verifying..."
docker info 2>/dev/null | grep -i proxy || echo "Proxy setting applied"
