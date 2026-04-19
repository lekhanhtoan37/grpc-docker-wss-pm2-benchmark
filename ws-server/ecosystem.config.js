module.exports = {
  apps: [
    {
      name: "ws-benchmark",
      script: "./server.js",
      instances: 3,
      exec_mode: "cluster",
      max_memory_restart: "1G",
      env: {
        PORT: 8090,
        KAFKA_BROKER: "192.168.0.5:9091",
        KAFKA_TOPIC: "benchmark-messages",
        UV_THREADPOOL_SIZE: "16",
      },
      kill_timeout: 10000,
      listen_timeout: 10000,
      wait_ready: true,
      max_restarts: 10,
      restart_delay: 4000,
    },
  ],
};
