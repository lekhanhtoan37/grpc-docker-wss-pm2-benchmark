FROM node:20-bookworm-slim
WORKDIR /app
COPY package*.json ./
RUN npm install --omit=dev
COPY server.js .
ENV PORT=8091
ENV CONTAINER_ID=default
CMD ["node", "--max-old-space-size=16384", "server.js"]
