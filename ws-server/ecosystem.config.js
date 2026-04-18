module.exports = {
  apps: [
    {
      name: "ws-benchmark",
      script: "./server.js",
      instances: 3,
      exec_mode: "cluster",
      max_memory_restart: "512M",
      env: {
        PORT: 8080,
        KAFKA_BROKER: "localhost:9092",
        KAFKA_TOPIC: "benchmark-messages",
      },
      kill_timeout: 10000,
      listen_timeout: 10000,
      wait_ready: true,
      max_restarts: 10,
      restart_delay: 4000,
    },
  ],
};
