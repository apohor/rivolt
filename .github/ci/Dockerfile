# syntax=docker/dockerfile:1.28@sha256:bb22d9815c728170f72750f4e5b0d672e06176142e1d602c7e66c050100b7e5b
#
# CI-only image assembly. The workflow's `bin` job has already built the static binary (warm Go cache, the same
# `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=…"` as the root Dockerfile, with the SPA embedded),
# so nothing is compiled here: this only copies it onto the same distroless base, with the same user, /data
# directory, port, environment and entrypoint as the root Dockerfile. The root Dockerfile is unchanged and remains
# what self-hosters and local builds use. Keep the two runtime stages identical.
#
# Build context: a directory holding `rivolt` (the binary, mode 0755) and an empty `data/` directory.
# Same digest as the root Dockerfile's runtime stage (keep them identical; Renovate bumps both in one PR).
FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
WORKDIR /app
COPY rivolt /app/rivolt
# Pre-created /data owned by distroless "nonroot" (uid 65532) so a named volume mounts it with the right ownership.
COPY --chown=nonroot:nonroot data /data
USER nonroot:nonroot
EXPOSE 8080
# SQLite cache lives here; mount as a volume in compose.
ENV DATA_DIR=/data
VOLUME ["/data"]
ENTRYPOINT ["/app/rivolt"]
