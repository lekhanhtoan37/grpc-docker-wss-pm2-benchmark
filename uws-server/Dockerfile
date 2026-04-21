FROM node:20-bookworm-slim
WORKDIR /app
COPY server.js .
COPY node_modules/ ./node_modules/
COPY package.json ./
ENV PORT=8091
ENV CONTAINER_ID=default
CMD ["node", "--max-old-space-size=16384", "server.js"]
