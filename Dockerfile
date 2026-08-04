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

FROM golang:1.25-bookworm AS runner-go

FROM rust:1.91-bookworm AS runner-rust

FROM node:24.11.1-bookworm-slim AS runner

ARG RUNNER_CLAUDE_CODE_VERSION=2.1.141
ARG RUNNER_CODEX_VERSION=0.130.0
ARG RUNNER_TYPESCRIPT_VERSION=6.0.3
ARG RUNNER_FLUTTER_VERSION=3.38.4

ENV DEBIAN_FRONTEND=noninteractive
ENV HOME=/home/agents
ENV FLUTTER_HOME=/opt/flutter
ENV RUSTUP_HOME=/usr/local/rustup
ENV CARGO_HOME=/usr/local/cargo
ENV PUB_CACHE=/home/agents/.pub-cache
ENV PATH=/usr/local/go/bin:/usr/local/cargo/bin:/opt/flutter/bin:/opt/flutter/bin/cache/dart-sdk/bin:/home/agents/.pub-cache/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

COPY --from=runner-go /usr/local/go /usr/local/go
COPY --from=runner-rust /usr/local/cargo /usr/local/cargo
COPY --from=runner-rust /usr/local/rustup /usr/local/rustup

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash \
        build-essential \
        ca-certificates \
        curl \
        git \
        gh \
        jq \
        libglu1-mesa \
        openssh-client \
        unzip \
        xz-utils \
        zip \
    && git clone --depth 1 --branch ${RUNNER_FLUTTER_VERSION} https://github.com/flutter/flutter.git ${FLUTTER_HOME} \
    && npm install -g \
        @anthropic-ai/claude-code@${RUNNER_CLAUDE_CODE_VERSION} \
        @openai/codex@${RUNNER_CODEX_VERSION} \
        typescript@${RUNNER_TYPESCRIPT_VERSION} \
    && npm cache clean --force \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

SHELL ["/bin/bash", "-c"]

RUN printf '%s\n' \
        'export FLUTTER_HOME=/opt/flutter' \
        'export RUSTUP_HOME=/usr/local/rustup' \
        'export CARGO_HOME=/usr/local/cargo' \
        'export PUB_CACHE=/home/agents/.pub-cache' \
        'export PATH=/usr/local/go/bin:/usr/local/cargo/bin:/opt/flutter/bin:/opt/flutter/bin/cache/dart-sdk/bin:/home/agents/.pub-cache/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' \
        > /etc/profile.d/agents-runner-path.sh

RUN useradd --create-home --home-dir /home/agents --shell /bin/bash agents \
    && mkdir -p /workspace /tmp/agents-run ${PUB_CACHE} \
    && chown -R agents:agents /home/agents /workspace /tmp/agents-run ${FLUTTER_HOME} ${CARGO_HOME} ${RUSTUP_HOME}

USER agents
RUN git config --global --add safe.directory ${FLUTTER_HOME} \
    && flutter config --no-analytics \
    && dart --disable-analytics \
    && flutter precache --universal

WORKDIR /workspace
