FROM node:22-bookworm-slim
WORKDIR /app
COPY server.js .
COPY node_modules/ ./node_modules/
COPY package.json ./
RUN npm rebuild uWebSockets.js 2>/dev/null || true
ENV PORT=8091
ENV CONTAINER_ID=default
CMD ["node", "--max-old-space-size=16384", "server.js"]
