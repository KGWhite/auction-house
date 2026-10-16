# syntax=docker/dockerfile:1
#
# Multi-stage build. Targets: server, worker, tools, web.
#   docker build --target server -t auctionhouse-server .

ARG GO_VERSION=1.27
ARG NODE_VERSION=24
ARG ALPINE_VERSION=3.22

# ---------------------------------------------------------------- Go build
FROM golang:${GO_VERSION} AS gobuild
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-buildvcs=false
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/ \
      ./cmd/server ./cmd/worker ./cmd/reconcile ./cmd/seed ./cmd/k6prep ./cmd/demo-race
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOBIN=/out go install github.com/pressly/goose/v3/cmd/goose@v3.26.0

# ---------------------------------------------------------------- server
FROM alpine:${ALPINE_VERSION} AS server
RUN adduser -D -u 10001 app
COPY --from=gobuild /out/server /usr/local/bin/server
USER app
EXPOSE 8080
HEALTHCHECK --interval=3s --timeout=3s --start-period=2s --retries=20 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/api/auctions || exit 1
ENTRYPOINT ["server"]

# ---------------------------------------------------------------- worker
FROM alpine:${ALPINE_VERSION} AS worker
RUN adduser -D -u 10001 app
COPY --from=gobuild /out/worker /usr/local/bin/worker
USER app
ENTRYPOINT ["worker"]

# ---------------------------------------------------------------- tools
# One-shot commands: migrate, seed, reconcile, demo-race, k6prep, goose.
FROM alpine:${ALPINE_VERSION} AS tools
RUN adduser -D -u 10001 app
COPY --from=gobuild /out/reconcile /out/seed /out/k6prep /out/demo-race /out/goose /usr/local/bin/
COPY migrations /migrations
COPY docker/migrate.sh /usr/local/bin/migrate
RUN chmod +x /usr/local/bin/migrate
USER app

# ---------------------------------------------------------------- web
FROM node:${NODE_VERSION}-alpine AS webbuild
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM nginx:1.29-alpine AS web
COPY docker/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=webbuild /web/dist /usr/share/nginx/html
EXPOSE 80
HEALTHCHECK --interval=3s --timeout=3s --retries=20 CMD wget -q -O /dev/null http://127.0.0.1/ || exit 1
