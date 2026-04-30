package config

import "os"

// resolveSecrets reads any *_env field and populates the corresponding
// non-yaml secret field at load time. Called once during Load / FinishLoad
// so that downstream code never has to call os.Getenv at request time.
//
// A missing or empty environment variable is left as the empty string here;
// validate() decides whether the empty value is acceptable for a given
// section (proxy in particular fails fast when api_key_env is set but the
// var is empty).
func (c *Config) resolveSecrets() {
	if c.Daemon.HTTP.WebhookSecret == "" && c.Daemon.HTTP.WebhookSecretEnv != "" {
		c.Daemon.HTTP.WebhookSecret = os.Getenv(c.Daemon.HTTP.WebhookSecretEnv)
	}
	if c.Daemon.Proxy.Upstream.APIKey == "" && c.Daemon.Proxy.Upstream.APIKeyEnv != "" {
		c.Daemon.Proxy.Upstream.APIKey = os.Getenv(c.Daemon.Proxy.Upstream.APIKeyEnv)
	}
}
