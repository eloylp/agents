package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	envLogLevel  = "AGENTS_LOG_LEVEL"
	envLogFormat = "AGENTS_LOG_FORMAT"

	envHTTPListenAddr             = "AGENTS_HTTP_LISTEN_ADDR"
	envHTTPStatusPath             = "AGENTS_HTTP_STATUS_PATH"
	envHTTPWebhookPath            = "AGENTS_HTTP_WEBHOOK_PATH"
	envHTTPWebhookSecretEnv       = "AGENTS_HTTP_WEBHOOK_SECRET_ENV"
	envHTTPReadTimeoutSeconds     = "AGENTS_HTTP_READ_TIMEOUT_SECONDS"
	envHTTPWriteTimeoutSeconds    = "AGENTS_HTTP_WRITE_TIMEOUT_SECONDS"
	envHTTPIdleTimeoutSeconds     = "AGENTS_HTTP_IDLE_TIMEOUT_SECONDS"
	envHTTPMaxBodyBytes           = "AGENTS_HTTP_MAX_BODY_BYTES"
	envHTTPDeliveryTTLSeconds     = "AGENTS_HTTP_DELIVERY_TTL_SECONDS"
	envHTTPShutdownTimeoutSeconds = "AGENTS_HTTP_SHUTDOWN_TIMEOUT_SECONDS"

	envProcessorEventQueueBuffer    = "AGENTS_PROCESSOR_EVENT_QUEUE_BUFFER"
	envProcessorMaxConcurrentAgents = "AGENTS_PROCESSOR_MAX_CONCURRENT_AGENTS"

	envDispatchMaxDepth           = "AGENTS_DISPATCH_MAX_DEPTH"
	envDispatchMaxFanout          = "AGENTS_DISPATCH_MAX_FANOUT"
	envDispatchDedupWindowSeconds = "AGENTS_DISPATCH_DEDUP_WINDOW_SECONDS"
)

// applyEnvOverrides applies startup-only daemon runtime overrides. Empty env
// vars are ignored so the YAML/default configuration remains authoritative
// unless an operator explicitly sets an AGENTS_* value.
func (c *Config) applyEnvOverrides() error {
	applyStringEnv(envLogLevel, &c.Daemon.Log.Level)
	applyStringEnv(envLogFormat, &c.Daemon.Log.Format)

	applyStringEnv(envHTTPListenAddr, &c.Daemon.HTTP.ListenAddr)
	if err := applyPathEnv(envHTTPStatusPath, &c.Daemon.HTTP.StatusPath); err != nil {
		return err
	}
	if err := applyPathEnv(envHTTPWebhookPath, &c.Daemon.HTTP.WebhookPath); err != nil {
		return err
	}
	applyStringEnv(envHTTPWebhookSecretEnv, &c.Daemon.HTTP.WebhookSecretEnv)
	if err := applyPositiveIntEnv(envHTTPReadTimeoutSeconds, &c.Daemon.HTTP.ReadTimeoutSeconds); err != nil {
		return err
	}
	if err := applyPositiveIntEnv(envHTTPWriteTimeoutSeconds, &c.Daemon.HTTP.WriteTimeoutSeconds); err != nil {
		return err
	}
	if err := applyPositiveIntEnv(envHTTPIdleTimeoutSeconds, &c.Daemon.HTTP.IdleTimeoutSeconds); err != nil {
		return err
	}
	if err := applyPositiveInt64Env(envHTTPMaxBodyBytes, &c.Daemon.HTTP.MaxBodyBytes); err != nil {
		return err
	}
	if err := applyPositiveIntEnv(envHTTPDeliveryTTLSeconds, &c.Daemon.HTTP.DeliveryTTLSeconds); err != nil {
		return err
	}
	if err := applyPositiveIntEnv(envHTTPShutdownTimeoutSeconds, &c.Daemon.HTTP.ShutdownTimeoutSeconds); err != nil {
		return err
	}

	if err := applyPositiveIntEnv(envProcessorEventQueueBuffer, &c.Daemon.Processor.EventQueueBuffer); err != nil {
		return err
	}
	if err := applyPositiveIntEnv(envProcessorMaxConcurrentAgents, &c.Daemon.Processor.MaxConcurrentAgents); err != nil {
		return err
	}
	if err := applyPositiveIntEnv(envDispatchMaxDepth, &c.Daemon.Processor.Dispatch.MaxDepth); err != nil {
		return err
	}
	if err := applyPositiveIntEnv(envDispatchMaxFanout, &c.Daemon.Processor.Dispatch.MaxFanout); err != nil {
		return err
	}
	if err := applyPositiveIntEnv(envDispatchDedupWindowSeconds, &c.Daemon.Processor.Dispatch.DedupWindowSeconds); err != nil {
		return err
	}
	return nil
}

func applyStringEnv(name string, dst *string) {
	if value, ok := nonEmptyEnv(name); ok {
		*dst = value
	}
}

func applyPathEnv(name string, dst *string) error {
	value, ok := nonEmptyEnv(name)
	if !ok {
		return nil
	}
	if !strings.HasPrefix(value, "/") {
		return fmt.Errorf("config: %s must start with '/', got %q", name, value)
	}
	*dst = value
	return nil
}

func applyPositiveIntEnv(name string, dst *int) error {
	value, ok := nonEmptyEnv(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fmt.Errorf("config: %s must be a positive integer, got %q", name, value)
	}
	*dst = parsed
	return nil
}

func applyPositiveInt64Env(name string, dst *int64) error {
	value, ok := nonEmptyEnv(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return fmt.Errorf("config: %s must be a positive integer, got %q", name, value)
	}
	*dst = parsed
	return nil
}

func nonEmptyEnv(name string) (string, bool) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}
