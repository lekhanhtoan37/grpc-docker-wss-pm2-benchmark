FROM node:20-bookworm-slim
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*
COPY package*.json ./
RUN npm install --omit=dev
COPY server.js .
ENV PORT=8091
ENV CONTAINER_ID=default
CMD ["node", "--max-old-space-size=16384", "server.js"]
