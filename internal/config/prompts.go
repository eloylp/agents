package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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

// loadPromptFiles reads any prompt_file references in skills and agents,
// populating the Prompt field with the resolved content. Paths are resolved
// relative to the config file's directory. Only called during the YAML Load
// path; FinishLoad assumes prompts are already inline (e.g. read from the
// SQLite store).
func (c *Config) loadPromptFiles() error {
	for name, skill := range c.Skills {
		content, err := c.resolvePrompt("skill "+name, skill.Prompt, skill.PromptFile)
		if err != nil {
			return err
		}
		skill.Prompt = content
		c.Skills[name] = skill
	}
	for i := range c.Agents {
		content, err := c.resolvePrompt("agent "+c.Agents[i].Name, c.Agents[i].Prompt, c.Agents[i].PromptFile)
		if err != nil {
			return err
		}
		c.Agents[i].Prompt = content
	}
	return nil
}

// resolvePrompt returns the resolved prompt text. Exactly one of prompt or
// promptFile must be set.
func (c *Config) resolvePrompt(ownerLabel, prompt, promptFile string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	promptFile = strings.TrimSpace(promptFile)
	switch {
	case prompt != "" && promptFile != "":
		return "", fmt.Errorf("%s: set either prompt or prompt_file, not both", ownerLabel)
	case prompt != "":
		return prompt, nil
	case promptFile != "":
		path := promptFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(c.configDir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s: read prompt_file %s: %w", ownerLabel, path, err)
		}
		return strings.TrimSpace(string(data)), nil
	default:
		return "", fmt.Errorf("%s: must set either prompt or prompt_file", ownerLabel)
	}
}
