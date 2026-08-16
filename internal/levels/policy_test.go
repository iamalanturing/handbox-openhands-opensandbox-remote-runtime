package levels

import (
	"errors"
	"testing"

	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/opensandbox"
)

func TestPolicy_AskAndUnrecognized(t *testing.T) {
	for _, level := range []Level{Ask, Level(""), Level("bogus")} {
		policy, err := Policy(level, nil, "")
		if !errors.Is(err, ErrAskRequired) {
			t.Errorf("Policy(%q) error = %v, want ErrAskRequired", level, err)
		}
		if policy != nil {
			t.Errorf("Policy(%q) policy = %+v, want nil", level, policy)
		}
	}
}

func TestPolicy_FullOmitsField(t *testing.T) {
	policy, err := Policy(Full, nil, "ollama.internal")
	if err != nil {
		t.Fatalf("Policy(Full) error = %v, want nil", err)
	}
	if policy != nil {
		t.Errorf("Policy(Full) policy = %+v, want nil (field omitted)", policy)
	}
}

func TestPolicy_OfflineWithOllamaHostAllowsOnlyThat(t *testing.T) {
	policy, err := Policy(Offline, nil, "ollama.internal")
	if err != nil {
		t.Fatalf("Policy(Offline) error = %v", err)
	}
	if policy == nil {
		t.Fatal("Policy(Offline) = nil, want an explicit deny-all policy")
	}
	if policy.DefaultAction != "deny" {
		t.Errorf("DefaultAction = %q, want deny", policy.DefaultAction)
	}
	assertTargets(t, policy.Egress, []string{"ollama.internal"})
}

func TestPolicy_OfflineWithoutOllamaHostDeniesEverything(t *testing.T) {
	// Fail closed: an unconfigured Ollama host must not silently fall back
	// to unrestricted networking.
	policy, err := Policy(Offline, nil, "")
	if err != nil {
		t.Fatalf("Policy(Offline) error = %v", err)
	}
	if policy == nil {
		t.Fatal("Policy(Offline) = nil, want an explicit deny-all policy")
	}
	if policy.DefaultAction != "deny" {
		t.Errorf("DefaultAction = %q, want deny", policy.DefaultAction)
	}
	if len(policy.Egress) != 0 {
		t.Errorf("Egress = %+v, want empty (no ollamaHost configured)", policy.Egress)
	}
}

func TestPolicy_GitHub(t *testing.T) {
	policy, err := Policy(GitHub, nil, "")
	if err != nil {
		t.Fatalf("Policy(GitHub) error = %v", err)
	}
	if policy == nil {
		t.Fatal("Policy(GitHub) = nil, want a policy")
	}
	if policy.DefaultAction != "deny" {
		t.Errorf("DefaultAction = %q, want deny", policy.DefaultAction)
	}
	wantTargets := []string{"github.com", "api.github.com", "*.githubusercontent.com", "codeload.github.com"}
	assertTargets(t, policy.Egress, wantTargets)
}

func TestPolicy_ResearchUsesDefaultAllowlistWhenUnconfigured(t *testing.T) {
	policy, err := Policy(Research, nil, "")
	if err != nil {
		t.Fatalf("Policy(Research) error = %v", err)
	}
	want := append([]string{"github.com", "api.github.com", "*.githubusercontent.com", "codeload.github.com"}, DefaultResearchAllowlist...)
	assertTargets(t, policy.Egress, want)
}

func TestPolicy_ResearchUsesConfiguredAllowlist(t *testing.T) {
	custom := []string{"internal-docs.example.com"}
	policy, err := Policy(Research, custom, "")
	if err != nil {
		t.Fatalf("Policy(Research) error = %v", err)
	}
	want := append([]string{"github.com", "api.github.com", "*.githubusercontent.com", "codeload.github.com"}, custom...)
	assertTargets(t, policy.Egress, want)
}

func TestPolicy_DoesNotMutateSharedGithubAllowlist(t *testing.T) {
	// Regression guard: Policy must not return a slice that aliases the
	// package-level githubEgress backing array, or repeated calls (e.g.
	// GitHub then Research) would corrupt each other.
	first, err := Policy(GitHub, nil, "")
	if err != nil {
		t.Fatalf("Policy(GitHub) error = %v", err)
	}
	first.Egress[0].Target = "mutated.example.com"

	second, err := Policy(GitHub, nil, "")
	if err != nil {
		t.Fatalf("Policy(GitHub) error = %v", err)
	}
	if second.Egress[0].Target != "github.com" {
		t.Errorf("second call saw mutation from first: Egress[0].Target = %q", second.Egress[0].Target)
	}
}

func assertTargets(t *testing.T, egress []opensandbox.EgressRule, wantTargets []string) {
	t.Helper()
	if len(egress) != len(wantTargets) {
		t.Fatalf("got %d egress rules, want %d: %+v", len(egress), len(wantTargets), egress)
	}
	for i, target := range wantTargets {
		if egress[i].Action != "allow" {
			t.Errorf("Egress[%d].Action = %q, want allow", i, egress[i].Action)
		}
		if egress[i].Target != target {
			t.Errorf("Egress[%d].Target = %q, want %q", i, egress[i].Target, target)
		}
	}
}
