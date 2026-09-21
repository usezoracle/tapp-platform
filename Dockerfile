# syntax=docker/dockerfile:1
#
# The API image, built from the repository root.
#
# It lives here rather than in apps/api because Railway builds a
# GitHub-connected service from the repository root, and a root directory is
# a dashboard-only setting. Everything the build needs is copied from
# apps/api; .dockerignore keeps the PWA, the merchant app and node_modules
# out of the context.

# ---- build: static binary, no CGO (prod DB driver is pure-Go pgx) ----
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY apps/api/go.mod apps/api/go.sum ./
RUN go mod download
COPY apps/api/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/rails .
# cdp-reissue retires a seed-derived deposit address and issues its CDP
# replacement. The app does this lazily, when somebody opens the deposit
# screen; this is the same operation for people who will not open it soon.
# It ships in the image because the production database is on Railway's
# private network and cannot be reached from a workstation, so `railway ssh`
# into this container is the only place the command can run.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/cdp-reissue ./cmd/cdp-reissue
# abandon-payout returns money reserved for a payout that will never be sent.
# Ships here for the same reason: Railway's Postgres is private-network only,
# so this container is the only place a command can reach it.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/abandon-payout ./cmd/abandon-payout
# reverse-deposit takes back a credit the chain never justified.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/reverse-deposit ./cmd/reverse-deposit
# reverse-tap refunds a card payment a merchant cannot reach from their app.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/reverse-tap ./cmd/reverse-tap

# ---- runtime: minimal, non-root ----
FROM alpine:3.20
# ca-certificates: outbound HTTPS (Fintava, Paycrest, CDP, Base RPC).
# tzdata: app calls time.LoadLocation (SERVER_TIMEZONE, e.g. Africa/Lagos).
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 app
USER app
COPY --from=build /out/rails /usr/local/bin/rails
COPY --from=build /out/cdp-reissue /usr/local/bin/cdp-reissue
COPY --from=build /out/abandon-payout /usr/local/bin/abandon-payout
COPY --from=build /out/reverse-deposit /usr/local/bin/reverse-deposit
COPY --from=build /out/reverse-tap /usr/local/bin/reverse-tap
# Railway/containers inject PORT; the app honours it (falls back to SERVER_PORT).
EXPOSE 8000
ENTRYPOINT ["rails"]
