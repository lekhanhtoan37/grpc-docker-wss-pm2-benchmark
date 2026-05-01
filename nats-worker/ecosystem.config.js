module.exports = {
  apps: [
    {
      name: "nats-benchmark",
      script: "./nats-worker",
      instances: 3,
      exec_mode: "fork",
      max_memory_restart: "16G",
      env: {
        KAFKA_BROKER: "192.168.0.9:9091",
        KAFKA_TOPIC: "benchmark-messages",
        NATS_URL: "nats://localhost:4222",
        NATS_SUBJECT: "benchmark.messages",
        PORT: 8095,
      },
      kill_timeout: 10000,
      listen_timeout: 10000,
      max_restarts: 10,
      restart_delay: 4000,
    },
  ],
};
