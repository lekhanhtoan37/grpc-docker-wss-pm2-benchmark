const fs = require("fs");
const path = require("path");
let node20 = "node";
try {
  const v20dir = path.join(require("os").homedir(), ".nvm/versions/node");
  const dirs = fs.readdirSync(v20dir).filter((d) => d.startsWith("v20")).sort();
  if (dirs.length) node20 = path.join(v20dir, dirs[dirs.length - 1], "bin/node");
} catch {}

module.exports = {
  apps: [
    {
      name: "uws-benchmark",
      script: path.join(__dirname, "server.js"),
      interpreter: node20,
      instances: 3,
      exec_mode: "cluster",
      node_args: "--max-old-space-size=16384",
      env: {
        PORT: 8091,
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
