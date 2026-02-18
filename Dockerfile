FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

RUN apk add --no-cache ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /agentd ./cmd/agentd

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /agentd /agentd
ENTRYPOINT ["/agentd"]
CMD ["-config", "/etc/agentd/config.yaml"]
