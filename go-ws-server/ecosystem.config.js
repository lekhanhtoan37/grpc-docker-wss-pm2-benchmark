module.exports = {
  apps: [
    {
      name: "go-ws-benchmark",
      script: "./go-ws-server",
      instances: 3,
      exec_mode: "fork",
      max_memory_restart: "16G",
      env: {
        KAFKA_BROKER: "192.168.0.9:9091",
        KAFKA_TOPIC: "benchmark-messages",
        GROUP_ID: "go-ws-benchmark-pm2",
        PORT: 8092,
      },
      kill_timeout: 10000,
      listen_timeout: 10000,
      max_restarts: 10,
      restart_delay: 4000,
    },
  ],
};
