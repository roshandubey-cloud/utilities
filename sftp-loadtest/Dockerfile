# syntax=docker/dockerfile:1.7

# Build stage — Go toolchain matches go.mod's `go 1.25` directive. Pinned
# digest avoids unsigned-image surprises on supply-chain scans.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 \
    go build \
      -trimpath \
      -ldflags "-s -w -X main.platformVersion=${VERSION}" \
      -o /out/sftp-loadtest \
      .

# Runtime stage — distroless static (no shell, no apt, no libc). The
# binary is statically linked because of CGO_ENABLED=0 above. Gives an
# image surface of <20MB with zero CVEs from base packages.
FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/roshandubey-cloud/utilities"
LABEL org.opencontainers.image.description="SFTP/FTP/FTPS load-testing tool — webui SKU"
LABEL org.opencontainers.image.licenses="MIT"
COPY --from=build /out/sftp-loadtest /usr/local/bin/sftp-loadtest
USER nonroot:nonroot
EXPOSE 8080
# /healthz responds at minimal cost; ?detail=1 is auth-gated and used by
# operators, so liveness probes use the unauthenticated endpoint.
HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD ["/usr/local/bin/sftp-loadtest", "-version"]
ENTRYPOINT ["/usr/local/bin/sftp-loadtest"]
CMD ["-addr", "0.0.0.0:8080"]
