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

FROM alpine:3.22 AS daemon

RUN apk add --no-cache ca-certificates

RUN adduser -D -h /home/agents -s /bin/sh agents \
    && mkdir -p /var/lib/agents/memory \
    && chown -R agents:agents /var/lib/agents
ENV HOME=/home/agents

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /agents /usr/local/bin/agents
USER agents
ENTRYPOINT ["agents"]
CMD ["--db", "/var/lib/agents/agents.db"]

FROM node:22-alpine AS runner

RUN apk add --no-cache bash build-base ca-certificates cargo curl git github-cli go jq rust \
    && npm install -g @anthropic-ai/claude-code @openai/codex typescript \
    && npm cache clean --force

SHELL ["/bin/bash", "-c"]

RUN adduser -D -h /home/agents -s /bin/bash agents \
    && mkdir -p /workspace /tmp/agents-run \
    && chown -R agents:agents /home/agents /workspace /tmp/agents-run
ENV HOME=/home/agents

USER agents
WORKDIR /workspace
