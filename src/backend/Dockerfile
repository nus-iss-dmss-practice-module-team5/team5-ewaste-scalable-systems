FROM golang:1.26-alpine AS builder

WORKDIR /src

ARG SERVICE=server

COPY go.mod go.sum ./
RUN for attempt in 1 2 3; do \
      go mod download && exit 0; \
      if [ "$attempt" = 3 ]; then exit 1; fi; \
      sleep "$attempt"; \
    done

COPY . .

RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" \
    -o /out/service ./cmd/${SERVICE}

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
RUN addgroup -S app && adduser -S app -G app

WORKDIR /app
COPY --from=builder /out/service ./service
COPY --from=builder /src/config.yaml ./config.yaml
ENV EWASTE_CONFIG_FILE=/app/config.yaml
RUN mkdir -p /app/logs && chown -R app:app /app

USER app

EXPOSE 8080

ENTRYPOINT ["/app/service"]
