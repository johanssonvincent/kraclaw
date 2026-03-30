FROM node:22-slim AS builder
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY tsconfig.json ./
COPY src/ ./src/
RUN npm run build

FROM node:22-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git && rm -rf /var/lib/apt/lists/*
RUN npm install -g @anthropic-ai/claude-code
WORKDIR /app
COPY --from=builder /app/dist/ ./dist/
COPY --from=builder /app/node_modules/ ./node_modules/
COPY package.json ./
RUN mkdir -p /home/node/.claude /workspace /config && chown -R node:node /home/node/.claude /workspace /config
USER node
ENV NODE_ENV=production
ENV HOME=/home/node
ENTRYPOINT ["node", "/app/dist/index.js"]
