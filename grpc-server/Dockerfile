FROM node:20-bookworm-slim

WORKDIR /app
COPY package*.json ./
RUN npm install --omit=dev
COPY server.js .
COPY proto/ ./proto/
ENV GRPC_PORT=50051
ENV CONTAINER_ID=default
CMD ["node", "--max-old-space-size=16384", "server.js"]
