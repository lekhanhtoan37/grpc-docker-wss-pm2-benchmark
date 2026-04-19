#!/bin/bash
set -e

KAFKA_VERSION="3.9.2"
SCALA_VERSION="2.13"
KAFKA_DIR="/opt/kafka-benchmark"
KAFKA_DATA="/home/kafka-benchmark/data"
KAFKA_USER="kafka-bench"
KAFKA_SERVICE="kafka-benchmark"

echo "=== Installing Benchmark Kafka ${KAFKA_VERSION} KRaft ==="

if ! java -version 2>&1 | grep -q "17\|21"; then
  echo "Installing OpenJDK 17..."
  if command -v apt &>/dev/null; then
    sudo apt update && sudo apt install -y openjdk-17-jdk-headless
  elif command -v dnf &>/dev/null; then
    sudo dnf install -y java-17-openjdk-devel
  else
    echo "Unsupported package manager. Install Java 17+ manually."
    exit 1
  fi
fi

if [ -d "${KAFKA_DIR}" ]; then
  echo "Kafka benchmark already installed at ${KAFKA_DIR}. Skipping download."
else
  echo "Downloading Kafka..."
  curl -fSL -o "kafka_${SCALA_VERSION}-${KAFKA_VERSION}.tgz" "https://downloads.apache.org/kafka/${KAFKA_VERSION}/kafka_${SCALA_VERSION}-${KAFKA_VERSION}.tgz"
  sudo tar -xzf "kafka_${SCALA_VERSION}-${KAFKA_VERSION}.tgz" -C /opt/
  sudo ln -s "/opt/kafka_${SCALA_VERSION}-${KAFKA_VERSION}" "${KAFKA_DIR}"
  rm "kafka_${SCALA_VERSION}-${KAFKA_VERSION}.tgz"
fi

if ! id "${KAFKA_USER}" &>/dev/null; then
  echo "Creating user ${KAFKA_USER}..."
  sudo useradd -r -s /sbin/nologin "${KAFKA_USER}"
else
  echo "User ${KAFKA_USER} already exists. Reusing."
fi

sudo mkdir -p "${KAFKA_DATA}"
sudo chown -R "${KAFKA_USER}:${KAFKA_USER}" "${KAFKA_DIR}"
sudo chown -R "${KAFKA_USER}:${KAFKA_USER}" "${KAFKA_DATA}"

echo ""
echo "Kafka installed at ${KAFKA_DIR}"
echo ""
echo "Next steps:"
echo "  sudo cp infra/server.properties ${KAFKA_DIR}/config/kraft/server.properties"
echo "  KAFKA_CLUSTER_ID=\$(sudo -u ${KAFKA_USER} ${KAFKA_DIR}/bin/kafka-storage.sh random-uuid)"
echo "  sudo -u ${KAFKA_USER} ${KAFKA_DIR}/bin/kafka-storage.sh format -t \$KAFKA_CLUSTER_ID -c ${KAFKA_DIR}/config/kraft/server.properties"
echo "  sudo cp infra/kafka-benchmark.service /etc/systemd/system/"
echo "  sudo systemctl daemon-reload"
echo "  sudo systemctl enable --now ${KAFKA_SERVICE}"
