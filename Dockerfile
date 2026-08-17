FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zora ./cmd/zora \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zora-mcp-files ./cmd/zora-mcp-files \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zora-mcp-microsoft ./cmd/zora-mcp-microsoft

FROM alpine:3.23

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S zora \
    && adduser -S -G zora zora \
    && mkdir -p /app/data \
    && chown -R zora:zora /app

WORKDIR /app
COPY --from=build /out/zora /usr/local/bin/zora
COPY --from=build /out/zora-mcp-files /usr/local/bin/zora-mcp-files
COPY --from=build /out/zora-mcp-microsoft /usr/local/bin/zora-mcp-microsoft

USER zora
ENV ZORA_ADDR=:8088 \
    ZORA_DATA_DIR=/app/data \
    ZORA_MODEL_PROVIDER=mock
VOLUME ["/app/data"]
EXPOSE 8088
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8088/api/health || exit 1

ENTRYPOINT ["/usr/local/bin/zora"]
