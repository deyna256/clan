FROM golang:1.27.1@sha256:f44f6e88636cfb311f9ebace870ded69d943f227bb3cb27d32ffd84ea18c43ea AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/clan ./cmd/clan \
    && mkdir /out/data

# Static runtime with CA certificates and no shell.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/clan /usr/local/bin/clan
# The runtime cannot create /data itself; a new volume inherits this owner.
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV CLAN_LISTEN_ADDR=0.0.0.0:8080 \
    CLAN_OAUTH_CALLBACK_ADDR=0.0.0.0:1455 \
    CLAN_DB_PATH=/data/clan.db
EXPOSE 8080 1455
ENTRYPOINT ["/usr/local/bin/clan"]
