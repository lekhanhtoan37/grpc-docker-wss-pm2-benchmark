FROM node:20-alpine
WORKDIR /app
COPY package*.json ./
RUN npm ci --omit=dev
COPY server.js .
COPY proto/ ./proto/
ENV GRPC_PORT=50051
ENV CONTAINER_ID=default
CMD ["node", "server.js"]
