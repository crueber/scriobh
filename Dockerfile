# syntax=docker/dockerfile:1

# Scríobh container image.
#
#   docker build -t scriobh .                  production image (no development issuer)
#   docker build --build-arg TAGS= -t scriobh:dev .   development image (includes the stub issuer)
#
# The image holds the static server binary and the small agent CLI. It runs as uid 65532, expects a read-only
# root filesystem, and writes only under /data (SCRIOBH_DATA_DIR) and /tmp.

ARG GO_VERSION=1.27.4
ARG DISTROLESS=gcr.io/distroless/static-debian12:nonroot

FROM golang:${GO_VERSION} AS build
# TAGS=prod compiles the development issuer out of the binary. The development
# compose stack passes an empty value.
ARG TAGS=prod
ARG VERSION=dev
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false \
        -tags "${TAGS}" \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/scriobh ./cmd/scriobh \
    && CGO_ENABLED=0 go build -trimpath -buildvcs=false \
        -ldflags "-s -w -X main.version=${VERSION} -X github.com/crueber/scriobh/internal/cli.Version=${VERSION}" \
        -o /out/scriobh-cli ./cmd/scriobh-cli

# distroless has no shell, so the data directory is made here and copied with
# the right owner. A new named volume inherits this ownership.
RUN mkdir -p /out/data && chmod 0700 /out/data

FROM ${DISTROLESS}
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build /out/scriobh /scriobh
# The slim agent CLI, for `docker exec`; the server does not need it.
COPY --from=build /out/scriobh-cli /scriobh-cli

ENV SCRIOBH_LISTEN=:8080 \
    SCRIOBH_DATA_DIR=/data

USER 65532:65532
WORKDIR /data
EXPOSE 8080
VOLUME /data

# The binary checks itself: there is no shell or curl in this image.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/scriobh", "healthcheck"]

ENTRYPOINT ["/scriobh"]
CMD ["serve"]
