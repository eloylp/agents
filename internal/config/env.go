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

	envProxyEnabled                = "AGENTS_PROXY_ENABLED"
	envProxyPath                   = "AGENTS_PROXY_PATH"
	envProxyUpstreamURL            = "AGENTS_PROXY_UPSTREAM_URL"
	envProxyUpstreamModel          = "AGENTS_PROXY_UPSTREAM_MODEL"
	envProxyUpstreamAPIKeyEnv      = "AGENTS_PROXY_UPSTREAM_API_KEY_ENV"
	envProxyUpstreamTimeoutSeconds = "AGENTS_PROXY_UPSTREAM_TIMEOUT_SECONDS"

	envRunnerImage            = "AGENTS_RUNNER_IMAGE"
	envRunnerCPUs             = "AGENTS_RUNNER_CPUS"
	envRunnerMemory           = "AGENTS_RUNNER_MEMORY"
	envRunnerPidsLimit        = "AGENTS_RUNNER_PIDS_LIMIT"
	envRunnerTimeoutSeconds   = "AGENTS_RUNNER_TIMEOUT_SECONDS"
	envRunnerNetworkMode      = "AGENTS_RUNNER_NETWORK_MODE"
	envRunnerFilesystemPolicy = "AGENTS_RUNNER_FILESYSTEM"
)

// applyEnvOverrides applies startup-only daemon runtime overrides. Empty env
// vars are ignored so the default process configuration remains authoritative
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
	if err := applyBoolEnv(envProxyEnabled, &c.Daemon.Proxy.Enabled); err != nil {
		return err
	}
	if err := applyPathEnv(envProxyPath, &c.Daemon.Proxy.Path); err != nil {
		return err
	}
	applyStringEnv(envProxyUpstreamURL, &c.Daemon.Proxy.Upstream.URL)
	applyStringEnv(envProxyUpstreamModel, &c.Daemon.Proxy.Upstream.Model)
	applyStringEnv(envProxyUpstreamAPIKeyEnv, &c.Daemon.Proxy.Upstream.APIKeyEnv)
	if err := applyPositiveIntEnv(envProxyUpstreamTimeoutSeconds, &c.Daemon.Proxy.Upstream.TimeoutSeconds); err != nil {
		return err
	}
	applyStringEnv(envRunnerImage, &c.Runtime.RunnerImage)
	applyStringEnv(envRunnerCPUs, &c.Runtime.Constraints.CPUs)
	applyStringEnv(envRunnerMemory, &c.Runtime.Constraints.Memory)
	if err := applyPositiveInt64Env(envRunnerPidsLimit, &c.Runtime.Constraints.PidsLimit); err != nil {
		return err
	}
	if err := applyPositiveIntEnv(envRunnerTimeoutSeconds, &c.Runtime.Constraints.TimeoutSeconds); err != nil {
		return err
	}
	applyStringEnv(envRunnerNetworkMode, &c.Runtime.Constraints.NetworkMode)
	applyStringEnv(envRunnerFilesystemPolicy, &c.Runtime.Constraints.Filesystem)
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

func applyBoolEnv(name string, dst *bool) error {
	value, ok := nonEmptyEnv(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("config: %s must be a boolean, got %q", name, value)
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
