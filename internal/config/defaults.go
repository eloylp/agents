package config

import (
	"strings"

	"github.com/eloylp/agents/internal/fleet"
)

const (
	defaultLogLevel  = "info"
	defaultLogFormat = "text"

	defaultHTTPListenAddr          = ":8080"
	defaultHTTPStatusPath          = "/status"
	defaultHTTPWebhookPath         = "/webhooks/github"
	defaultHTTPWebhookSecretEnv    = "GITHUB_WEBHOOK_SECRET"
	defaultHTTPReadTimeoutSeconds  = 15
	defaultHTTPWriteTimeoutSeconds = 15
	defaultHTTPIdleTimeoutSeconds  = 60
	defaultHTTPMaxBodyBytes        = 1 << 20
	defaultDeliveryTTLSeconds      = 3600
	defaultHTTPShutdownSeconds     = 15

	defaultEventQueueBufferSize = 256
	defaultMaxConcurrentAgents  = 4

	defaultProxyPath           = "/v1/messages"
	defaultProxyTimeoutSeconds = 120
)

// applyDefaults fills in zero-valued config fields with their documented
// defaults. Called once at the end of Load and FinishLoad so every code path
// that obtains a *Config sees the same effective values without each caller
// having to remember which field has a non-zero default.
func (c *Config) applyDefaults() {
	// daemon.log
	setDefault(&c.Daemon.Log.Level, defaultLogLevel)
	setDefault(&c.Daemon.Log.Format, defaultLogFormat)

	// daemon.http
	setDefault(&c.Daemon.HTTP.ListenAddr, defaultHTTPListenAddr)
	setDefault(&c.Daemon.HTTP.StatusPath, defaultHTTPStatusPath)
	setDefault(&c.Daemon.HTTP.WebhookPath, defaultHTTPWebhookPath)
	setDefault(&c.Daemon.HTTP.WebhookSecretEnv, defaultHTTPWebhookSecretEnv)
	setDefaultInt(&c.Daemon.HTTP.ReadTimeoutSeconds, defaultHTTPReadTimeoutSeconds)
	setDefaultInt(&c.Daemon.HTTP.WriteTimeoutSeconds, defaultHTTPWriteTimeoutSeconds)
	setDefaultInt(&c.Daemon.HTTP.IdleTimeoutSeconds, defaultHTTPIdleTimeoutSeconds)
	setDefaultInt(&c.Daemon.HTTP.MaxBodyBytes, defaultHTTPMaxBodyBytes)
	setDefaultInt(&c.Daemon.HTTP.DeliveryTTLSeconds, defaultDeliveryTTLSeconds)
	setDefaultInt(&c.Daemon.HTTP.ShutdownTimeoutSeconds, defaultHTTPShutdownSeconds)

	// daemon.processor
	setDefaultInt(&c.Daemon.Processor.EventQueueBuffer, defaultEventQueueBufferSize)
	setDefaultInt(&c.Daemon.Processor.MaxConcurrentAgents, defaultMaxConcurrentAgents)
	setDefaultInt(&c.Daemon.Processor.Dispatch.MaxDepth, 3)
	setDefaultInt(&c.Daemon.Processor.Dispatch.MaxFanout, 4)
	setDefaultInt(&c.Daemon.Processor.Dispatch.DedupWindowSeconds, 300)

	// daemon.proxy defaults (only applied when proxy is enabled or path is set)
	setDefault(&c.Daemon.Proxy.Path, defaultProxyPath)
	setDefaultInt(&c.Daemon.Proxy.Upstream.TimeoutSeconds, defaultProxyTimeoutSeconds)

	// daemon.ai_backends defaults
	for name, backend := range c.Daemon.AIBackends {
		fleet.ApplyBackendDefaults(&backend)
		c.Daemon.AIBackends[name] = backend
	}

	// repos: default enabled to true when field absent is ambiguous; YAML
	// zero-value is false. We leave it as-is — absent means false here,
	// because repos are an explicit allow-list.
}

// normalize lowercases / trims keys and string fields so case-insensitive
// matching works downstream. Called once after applyDefaults.
func (c *Config) normalize() {
	// Lowercase backend keys for case-insensitive matching.
	if len(c.Daemon.AIBackends) > 0 {
		lower := make(map[string]fleet.Backend, len(c.Daemon.AIBackends))
		for name, backend := range c.Daemon.AIBackends {
			key := fleet.NormalizeBackendName(name)
			fleet.NormalizeBackend(&backend)
			lower[key] = backend
		}
		c.Daemon.AIBackends = lower
	}

	// Lowercase skill keys.
	if len(c.Skills) > 0 {
		lower := make(map[string]fleet.Skill, len(c.Skills))
		for name, skill := range c.Skills {
			key := fleet.NormalizeSkillName(name)
			fleet.NormalizeSkill(&skill)
			lower[key] = skill
		}
		c.Skills = lower
	}

	// Agents.
	for i := range c.Agents {
		fleet.NormalizeAgent(&c.Agents[i])
	}

	// Repos.
	for i := range c.Repos {
		fleet.NormalizeRepo(&c.Repos[i])
	}

	// Log.
	c.Daemon.Log.Level = strings.ToLower(strings.TrimSpace(c.Daemon.Log.Level))
	c.Daemon.Log.Format = strings.ToLower(strings.TrimSpace(c.Daemon.Log.Format))
}

// setDefault writes def into *dst if *dst is empty (after trimming).
func setDefault(dst *string, def string) {
	if strings.TrimSpace(*dst) == "" {
		*dst = def
	}
}

// setDefaultInt writes def into *dst if *dst is zero. Generic over int and
// int64 so HTTP timeouts (int) and MaxBodyBytes (int64) share one helper.
func setDefaultInt[T int | int64](dst *T, def T) {
	if *dst == 0 {
		*dst = def
	}
}
