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

# Runtime stage
FROM gcr.io/distroless/static-debian13:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server
EXPOSE 8080
USER nonroot:nonroot
# No shell/curl in distroless — the binary probes its own /healthz. Only
# meaningful for the http transport (the compose service runs it).
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/app/server", "-health-check"]
ENTRYPOINT ["/app/server"]
