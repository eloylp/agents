FROM node:22-alpine AS ui-builder
WORKDIR /ui
COPY internal/ui/package.json internal/ui/package-lock.json ./
RUN npm ci
COPY internal/ui/ .
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

RUN apk add --no-cache ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui-builder /ui/dist internal/ui/dist
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /agents ./cmd/agents

FROM node:22-alpine

RUN apk add --no-cache bash \
    && npm install -g @anthropic-ai/claude-code @openai/codex \
    && npm cache clean --force

SHELL ["/bin/bash", "-c"]

RUN adduser -D -h /home/agents -s /bin/bash agents \
    && mkdir -p /var/lib/agents/memory \
    && chown agents:agents /var/lib/agents/memory
ENV HOME=/home/agents

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /agents /agents
USER agents
ENTRYPOINT ["/agents"]
CMD ["--db", "/var/lib/agents/agents.db"]
