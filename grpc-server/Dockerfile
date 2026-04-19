FROM node:20-bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    librdkafka-dev \
    python3 \
    make \
    g++ \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY package*.json ./
RUN npm install --omit=dev
COPY server.js .
COPY proto/ ./proto/
ENV GRPC_PORT=50051
ENV CONTAINER_ID=default
CMD ["node", "server.js"]
