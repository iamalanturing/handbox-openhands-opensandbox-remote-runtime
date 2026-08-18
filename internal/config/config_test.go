package config

import (
	"strings"
	"testing"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv(envOpenSandboxURL, "https://opensandbox.example.com")
	t.Setenv(envOpenSandboxAPIKey, "os-key")
	t.Setenv(envSandboxAPIKey, "sandbox-key")
	// Clear everything else so tests don't inherit the running
	// environment's actual values.
	for _, key := range []string{envListenAddr, envStateFilePath, envSingleUseLevel, envResearchAllowlist, envOllamaHost} {
		t.Setenv(key, "")
	}
}

func TestLoad_RequiredFieldsAndDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.OpenSandboxURL != "https://opensandbox.example.com" {
		t.Errorf("OpenSandboxURL = %q", cfg.OpenSandboxURL)
	}
	if cfg.OpenSandboxAPIKey != "os-key" {
		t.Errorf("OpenSandboxAPIKey = %q", cfg.OpenSandboxAPIKey)
	}
	if cfg.SandboxAPIKey != "sandbox-key" {
		t.Errorf("SandboxAPIKey = %q", cfg.SandboxAPIKey)
	}
	if cfg.ListenAddr != defaultListenAddr {
		t.Errorf("ListenAddr = %q, want default %q", cfg.ListenAddr, defaultListenAddr)
	}
	if cfg.StateFilePath != defaultStateFile {
		t.Errorf("StateFilePath = %q, want default %q", cfg.StateFilePath, defaultStateFile)
	}
	if cfg.SingleUseLevel != true {
		t.Errorf("SingleUseLevel = %v, want true (default)", cfg.SingleUseLevel)
	}
	if cfg.ResearchAllowlist != nil {
		t.Errorf("ResearchAllowlist = %v, want nil", cfg.ResearchAllowlist)
	}
	if cfg.OllamaHost != "" {
		t.Errorf("OllamaHost = %q, want empty", cfg.OllamaHost)
	}
}

func TestLoad_MissingRequiredVars(t *testing.T) {
	t.Setenv(envOpenSandboxURL, "")
	t.Setenv(envOpenSandboxAPIKey, "")
	t.Setenv(envSandboxAPIKey, "")
	t.Setenv(envListenAddr, "")
	t.Setenv(envStateFilePath, "")
	t.Setenv(envSingleUseLevel, "")
	t.Setenv(envResearchAllowlist, "")
	t.Setenv(envOllamaHost, "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error for missing required vars")
	}
	for _, want := range []string{envOpenSandboxURL, envOpenSandboxAPIKey, envSandboxAPIKey} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention missing var %q", err, want)
		}
	}
}

func TestLoad_InvalidSingleUseLevel(t *testing.T) {
	setRequired(t)
	t.Setenv(envSingleUseLevel, "not-a-bool")

	if _, err := Load(); err == nil {
		t.Error("Load() error = nil, want error for invalid bool")
	}
}

func TestLoad_ResearchAllowlistParsing(t *testing.T) {
	setRequired(t)
	t.Setenv(envResearchAllowlist, " a.example.com ,b.example.com,, c.example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"a.example.com", "b.example.com", "c.example.com"}
	if len(cfg.ResearchAllowlist) != len(want) {
		t.Fatalf("ResearchAllowlist = %v, want %v", cfg.ResearchAllowlist, want)
	}
	for i, host := range want {
		if cfg.ResearchAllowlist[i] != host {
			t.Errorf("ResearchAllowlist[%d] = %q, want %q", i, cfg.ResearchAllowlist[i], host)
		}
	}
}

func TestLoad_OllamaHostRejectsIP(t *testing.T) {
	setRequired(t)
	for _, host := range []string{"127.0.0.1", "172.17.0.1", "::1"} {
		t.Setenv(envOllamaHost, host)
		if _, err := Load(); err == nil {
			t.Errorf("Load() with %s=%q error = nil, want error rejecting the IP", envOllamaHost, host)
		}
	}
}

func TestLoad_OllamaHostAcceptsHostname(t *testing.T) {
	setRequired(t)
	t.Setenv(envOllamaHost, "host.docker.internal")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.OllamaHost != "host.docker.internal" {
		t.Errorf("OllamaHost = %q", cfg.OllamaHost)
	}
}

func TestLoad_CustomListenAddrAndStateFile(t *testing.T) {
	setRequired(t)
	t.Setenv(envListenAddr, "127.0.0.1:9000")
	t.Setenv(envStateFilePath, "/tmp/custom-level.state")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9000" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.StateFilePath != "/tmp/custom-level.state" {
		t.Errorf("StateFilePath = %q", cfg.StateFilePath)
	}
}

func TestLoad_InvalidOpenSandboxURL(t *testing.T) {
	setRequired(t)
	t.Setenv(envOpenSandboxURL, "not-a-url")

	if _, err := Load(); err == nil {
		t.Error("Load() error = nil, want error for invalid OpenSandbox URL")
	}
}
