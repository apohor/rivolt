# syntax=docker/dockerfile:1.7

# ---- web builder ----------------------------------------------------------
# Always run on the build host's native platform — the output is static
# assets (JS/CSS/HTML) so there's nothing arch-specific to produce.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
# Vite writes to ../internal/web/dist (relative to /web), so mirror that path.
RUN mkdir -p /internal/web && npm run build

# ---- go builder -----------------------------------------------------------
# Also native — we cross-compile using GOOS/GOARCH from TARGET* args, which
# is dramatically faster than running `go build` under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# Overlay the built web assets produced in the `web` stage.
COPY --from=web /internal/web/dist ./internal/web/dist
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/rivolt ./cmd/rivolt
# Pre-create a /data directory owned by distroless "nonroot" (uid 65532) so
# the named volume mounts it with the right ownership on first run.
RUN mkdir -p /out/data && chown -R 65532:65532 /out/data

# ---- runtime -------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/rivolt /app/rivolt
COPY --from=build --chown=nonroot:nonroot /out/data /data
USER nonroot:nonroot
EXPOSE 8080
# SQLite cache lives here; mount as a volume in compose.
ENV DATA_DIR=/data
VOLUME ["/data"]
ENTRYPOINT ["/app/rivolt"]
