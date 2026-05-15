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

FROM node:24.11.1-alpine3.23 AS runner

ARG RUNNER_BASH_VERSION=5.3.3-r1
ARG RUNNER_BUILD_BASE_VERSION=0.5-r3
ARG RUNNER_CA_CERTIFICATES_VERSION=20260413-r0
ARG RUNNER_CARGO_VERSION=1.91.1-r1
ARG RUNNER_CURL_VERSION=8.17.0-r1
ARG RUNNER_GIT_VERSION=2.52.0-r0
ARG RUNNER_GITHUB_CLI_VERSION=2.83.0-r5
ARG RUNNER_GO_VERSION=1.25.9-r0
ARG RUNNER_JQ_VERSION=1.8.1-r0
ARG RUNNER_RUST_VERSION=1.91.1-r1
ARG RUNNER_CLAUDE_CODE_VERSION=2.1.141
ARG RUNNER_CODEX_VERSION=0.130.0
ARG RUNNER_TYPESCRIPT_VERSION=6.0.3

RUN apk add --no-cache \
        bash=${RUNNER_BASH_VERSION} \
        build-base=${RUNNER_BUILD_BASE_VERSION} \
        ca-certificates=${RUNNER_CA_CERTIFICATES_VERSION} \
        cargo=${RUNNER_CARGO_VERSION} \
        curl=${RUNNER_CURL_VERSION} \
        git=${RUNNER_GIT_VERSION} \
        github-cli=${RUNNER_GITHUB_CLI_VERSION} \
        go=${RUNNER_GO_VERSION} \
        jq=${RUNNER_JQ_VERSION} \
        rust=${RUNNER_RUST_VERSION} \
    && npm install -g \
        @anthropic-ai/claude-code@${RUNNER_CLAUDE_CODE_VERSION} \
        @openai/codex@${RUNNER_CODEX_VERSION} \
        typescript@${RUNNER_TYPESCRIPT_VERSION} \
    && npm cache clean --force

SHELL ["/bin/bash", "-c"]

RUN adduser -D -h /home/agents -s /bin/bash agents \
    && mkdir -p /workspace /tmp/agents-run \
    && chown -R agents:agents /home/agents /workspace /tmp/agents-run
ENV HOME=/home/agents

USER agents
WORKDIR /workspace
