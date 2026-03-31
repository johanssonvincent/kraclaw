FROM golang:1.26-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o kraclaw-agent-openai \
    ./cmd/kraclaw-agent-openai

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /build/kraclaw-agent-openai /agent

LABEL org.opencontainers.image.source="https://github.com/johanssonvincent/kraclaw"
LABEL org.opencontainers.image.description="Kraclaw OpenAI Agent"
LABEL org.opencontainers.image.licenses="Apache-2.0"

ENTRYPOINT ["/agent"]
