# Dicy Talks: one container for VCS with the website (Next.js, public on $PORT)
# and the Go API (private on 127.0.0.1:8081). See deploy/vcs/entrypoint.sh.
#
# VCS builds on ARM without emulation: every stage that RUNs uses the build
# machine's own platform and cross-compiles; the final stage only COPYs.
# The website is the pinned commit of ai-screener-interview-frontend below
# (`make vcs-deploy` bumps it to the latest main before deploying).
ARG FRONTEND_REF=317701e2283dbee16d50cdc728fb73ee9ec02581

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS api
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS web
ARG FRONTEND_REF
WORKDIR /web
ADD https://github.com/markzugenjer/ai-screener-interview-frontend/archive/${FRONTEND_REF}.tar.gz /tmp/web.tar.gz
RUN tar -xzf /tmp/web.tar.gz --strip-components=1 -C /web && rm /tmp/web.tar.gz && mkdir -p public
ENV NEXT_TELEMETRY_DISABLED=1 CI=1
RUN corepack enable && pnpm install --frozen-lockfile
# The standalone server is plain JavaScript (no native runtime modules), so a
# bundle built on ARM runs unchanged on the x86 runtime.
RUN pnpm build

FROM node:22-bookworm-slim
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=8080
COPY --from=web --chown=node:node /web/.next/standalone /app/web
COPY --from=web --chown=node:node /web/.next/static /app/web/.next/static
COPY --from=web --chown=node:node /web/public /app/web/public
COPY --from=api /out/api /app/bin/api
COPY --chmod=755 deploy/vcs/entrypoint.sh /app/entrypoint.sh
USER node
EXPOSE 8080
CMD ["/app/entrypoint.sh"]
