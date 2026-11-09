# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

RUN apk add --no-cache git

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/openapi-mocker ./cmd/server

# ---- runtime stage ----
FROM alpine:3.20
LABEL org.opencontainers.image.title="openapi-mocker" \
      org.opencontainers.image.description="Mock server for OpenAPI, GraphQL, MCP and gRPC contracts"
RUN apk add --no-cache ca-certificates && \
    addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=build /out/openapi-mocker /app/openapi-mocker

ENV PORT=8080
ENV DATA_DIR=/data
RUN mkdir -p /data && chown -R app:app /data
VOLUME ["/data"]

USER app
EXPOSE 8080
# The binary checks itself (no curl or wget needed) and follows $PORT. The short
# interval and start period keep CI pipelines from waiting long.
HEALTHCHECK --interval=10s --timeout=3s --start-period=3s --retries=3 \
    CMD ["/app/openapi-mocker", "healthcheck"]

ENTRYPOINT ["/app/openapi-mocker"]
