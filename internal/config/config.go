// Package config loads handbox's own configuration (listen address, the
// OpenSandbox API URL/key, the state file path, and the research-level
// allowlist) from environment variables.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const (
	envListenAddr        = "HANDBOX_LISTEN_ADDR"
	envOpenSandboxURL    = "HANDBOX_OPENSANDBOX_URL"
	envOpenSandboxAPIKey = "HANDBOX_OPENSANDBOX_API_KEY"
	envSandboxAPIKey     = "HANDBOX_SANDBOX_API_KEY"
	envStateFilePath     = "HANDBOX_STATE_FILE"
	envSingleUseLevel    = "HANDBOX_SINGLE_USE_LEVEL"
	envResearchAllowlist = "HANDBOX_RESEARCH_ALLOWLIST"
	envOllamaHost        = "HANDBOX_OLLAMA_HOST"

	defaultListenAddr = ":8080"
	defaultStateFile  = "/var/lib/handbox/level.state"
	defaultSingleUse  = true
)

// Config is handbox's own configuration, loaded once at startup.
type Config struct {
	// ListenAddr is the address handbox's own HTTP server listens on.
	ListenAddr string
	// OpenSandboxURL is the base URL of the OpenSandbox API.
	OpenSandboxURL string
	// OpenSandboxAPIKey authenticates handbox to OpenSandbox (sent as the
	// OPEN-SANDBOX-API-KEY header).
	OpenSandboxAPIKey string
	// SandboxAPIKey is the value OpenHands must present (as SANDBOX_API_KEY
	// on its side) for handbox to accept a Remote Runtime request.
	SandboxAPIKey string
	// StateFilePath is where the network-level state file lives. Per the
	// spec's security requirement, this must be a host path never
	// bind-mounted into any container the agent can reach.
	StateFilePath string
	// SingleUseLevel controls whether a selected level is consumed (reset
	// to Ask) by the /start call that reads it. Defaults to true.
	SingleUseLevel bool
	// ResearchAllowlist overrides levels.DefaultResearchAllowlist when
	// non-empty.
	ResearchAllowlist []string
	// OllamaHost is the FQDN Offline allows egress to, so the sandbox can
	// still reach the local LLM. Must be a hostname, not an IP —
	// OpenSandbox's egress rules don't support IP/CIDR targets. Empty
	// means Offline denies everything, including Ollama.
	OllamaHost string
}

// Load reads Config from environment variables, applying defaults for
// everything except the required OpenSandbox and SANDBOX_API_KEY values.
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:        getEnvDefault(envListenAddr, defaultListenAddr),
		OpenSandboxURL:    os.Getenv(envOpenSandboxURL),
		OpenSandboxAPIKey: os.Getenv(envOpenSandboxAPIKey),
		SandboxAPIKey:     os.Getenv(envSandboxAPIKey),
		StateFilePath:     getEnvDefault(envStateFilePath, defaultStateFile),
		SingleUseLevel:    defaultSingleUse,
		OllamaHost:        os.Getenv(envOllamaHost),
	}

	if raw := os.Getenv(envSingleUseLevel); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid bool %q", envSingleUseLevel, raw)
		}
		cfg.SingleUseLevel = parsed
	}

	if raw := os.Getenv(envResearchAllowlist); raw != "" {
		cfg.ResearchAllowlist = splitAndTrim(raw)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var missing []string
	if c.OpenSandboxURL == "" {
		missing = append(missing, envOpenSandboxURL)
	}
	if c.OpenSandboxAPIKey == "" {
		missing = append(missing, envOpenSandboxAPIKey)
	}
	if c.SandboxAPIKey == "" {
		missing = append(missing, envSandboxAPIKey)
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variable(s): %s", strings.Join(missing, ", "))
	}

	if c.OpenSandboxURL != "" {
		u, err := url.Parse(c.OpenSandboxURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("%s: %q is not a valid absolute URL", envOpenSandboxURL, c.OpenSandboxURL)
		}
	}

	if c.OllamaHost != "" && net.ParseIP(c.OllamaHost) != nil {
		return fmt.Errorf("%s: %q is an IP address; OpenSandbox's egress rules only support FQDN/wildcard targets, not IP/CIDR — configure a hostname instead", envOllamaHost, c.OllamaHost)
	}

	return nil
}

func getEnvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitAndTrim(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
