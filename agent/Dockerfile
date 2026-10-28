# Voice agent image. Hosted on LiveKit Cloud (VCS only runs HTTP services):
#   cd agent && lk agent create --secrets-file .env.production
FROM node:22-slim AS build
WORKDIR /app
RUN corepack enable
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY tsconfig.json ./
COPY src ./src
RUN pnpm build && pnpm prune --prod

FROM node:22-slim
WORKDIR /app
ENV NODE_ENV=production
COPY --from=build /app/node_modules ./node_modules
COPY --from=build /app/dist ./dist
COPY package.json ./
# Download the VAD / turn-detector model weights into the image as the user that
# runs the agent (so it can read its own cache) — startup is then fast.
ENV HOME=/home/node
RUN chown -R node:node /app
USER node
RUN node dist/main.js download-files
CMD ["node", "dist/main.js", "start"]
