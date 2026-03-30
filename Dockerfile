# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o kraclaw \
    ./cmd/kraclaw

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o kraclaw-tui \
    ./cmd/kraclaw-tui

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /build/kraclaw /build/kraclaw-tui /app/

RUN addgroup -g 1000 kraclaw && \
    adduser -D -u 1000 -G kraclaw kraclaw
USER kraclaw

EXPOSE 50051 8080 3001

LABEL org.opencontainers.image.source="https://github.com/johanssonvincent/kraclaw"
LABEL org.opencontainers.image.description="Kraclaw - K8s-native AI agent orchestrator"

ENTRYPOINT ["/app/kraclaw"]
