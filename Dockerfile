FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

RUN apk add --no-cache ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /agents ./cmd/agents

FROM node:22-alpine

RUN apk add --no-cache bash github-cli \
    && npm install -g @anthropic-ai/claude-code @openai/codex \
    && npm cache clean --force

SHELL ["/bin/bash", "-c"]

RUN adduser -D -h /home/agents -s /bin/bash agents
ENV HOME=/home/agents

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /agents /agents
USER agents
ENTRYPOINT ["/agents"]
CMD ["-config", "/etc/agents/config.yaml"]
