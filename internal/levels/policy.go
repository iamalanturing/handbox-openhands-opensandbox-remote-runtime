package levels

import (
	"errors"

	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/opensandbox"
)

// ErrAskRequired is returned by Policy when no network level has been
// selected. Callers must reject the sandbox creation (e.g. HTTP 409)
// rather than falling back to a default level.
var ErrAskRequired = errors.New("no network level selected: run `ai-level <offline|github|research|full>`")

// githubEgress is the fixed allowlist for the GitHub-only level. Unlike
// the research allowlist, the spec doesn't call this out as configurable.
var githubEgress = []opensandbox.EgressRule{
	{Action: "allow", Target: "github.com"},
	{Action: "allow", Target: "api.github.com"},
	{Action: "allow", Target: "*.githubusercontent.com"},
	{Action: "allow", Target: "codeload.github.com"},
}

// DefaultResearchAllowlist is used for the Research level when handbox's
// config doesn't override it.
var DefaultResearchAllowlist = []string{
	"*.python.org",
	"pypi.org",
	"*.npmjs.org",
	"stackoverflow.com",
	"developer.mozilla.org",
	"en.wikipedia.org",
}

// Policy builds the OpenSandbox networkPolicy for level. A nil policy
// means the field should be omitted from the create request entirely
// (unrestricted default networking) — that's the case for both Offline
// and Full today. For Offline this relies on OpenSandbox's default
// sandbox networking being able to reach Ollama; that's flagged as an
// open question to verify empirically (implementation spec Known Unknown
// #1) rather than assumed here — if it turns out not to be reachable,
// Offline will need an explicit minimal allowlist instead of a nil
// policy.
func Policy(level Level, researchAllowlist []string) (*opensandbox.NetworkPolicy, error) {
	switch level {
	case Offline, Full:
		return nil, nil
	case GitHub:
		return &opensandbox.NetworkPolicy{
			DefaultAction: "deny",
			Egress:        append([]opensandbox.EgressRule(nil), githubEgress...),
		}, nil
	case Research:
		allowlist := researchAllowlist
		if len(allowlist) == 0 {
			allowlist = DefaultResearchAllowlist
		}
		egress := append([]opensandbox.EgressRule(nil), githubEgress...)
		for _, target := range allowlist {
			egress = append(egress, opensandbox.EgressRule{Action: "allow", Target: target})
		}
		return &opensandbox.NetworkPolicy{DefaultAction: "deny", Egress: egress}, nil
	default:
		return nil, ErrAskRequired
	}
}
