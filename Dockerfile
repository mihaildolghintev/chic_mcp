# Build stage
FROM golang:1.25 AS build
WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Version is passed in (the .git tree is dockerignored, so the toolchain can't
# derive it). Build with: docker build --build-arg VERSION=$(git describe ...).
ARG VERSION=dev
# CGO disabled so the binary is fully static (matches modernc.org/sqlite, the
# pure-Go SQLite driver) — keeps the runtime image tiny and cross-compilable.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X mcp.chic.md/internal/buildinfo.Version=${VERSION}" \
    -o /out/server ./cmd/server
# Staging dir for the runtime /data mount point (see COPY --chown below).
RUN mkdir -p /out/data

# Runtime stage
FROM gcr.io/distroless/static-debian13:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server
# Pre-create /data owned by nonroot: a fresh named volume mounted there
# inherits this ownership, so SQLite can create its files (no shell in
# distroless to chown at runtime).
COPY --from=build --chown=nonroot:nonroot /out/data /data
EXPOSE 8080
USER nonroot:nonroot
# No shell/curl in distroless — the binary probes its own /healthz (bot mode,
# the container default).
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/app/server", "-health-check"]
ENTRYPOINT ["/app/server"]
