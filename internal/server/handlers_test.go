package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/levels"
	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/opensandbox"
)

// fakeSandboxClient implements sandboxClient for tests, with configurable
// behavior per method and call capture for the ones tests need to assert
// against.
type fakeSandboxClient struct {
	mu sync.Mutex

	createSandboxFunc func(req opensandbox.CreateSandboxRequest) (*opensandbox.Sandbox, error)
	getSandboxFunc    func(id string) (*opensandbox.Sandbox, error)
	listSandboxesFunc func() ([]opensandbox.Sandbox, error)
	deleteSandboxFunc func(id string) error
	pauseSandboxFunc  func(id string) error
	resumeSandboxFunc func(id string) error
	endpointFunc      func(id string, port int, useServerProxy bool) (string, error)

	createCalls []opensandbox.CreateSandboxRequest
	deleteCalls []string
	pauseCalls  []string
	resumeCalls []string
}

func (f *fakeSandboxClient) CreateSandbox(_ context.Context, req opensandbox.CreateSandboxRequest) (*opensandbox.Sandbox, error) {
	f.mu.Lock()
	f.createCalls = append(f.createCalls, req)
	f.mu.Unlock()
	if f.createSandboxFunc != nil {
		return f.createSandboxFunc(req)
	}
	return &opensandbox.Sandbox{ID: "sbx_default", Status: opensandbox.SandboxStatus{State: "Running"}}, nil
}

func (f *fakeSandboxClient) GetSandbox(_ context.Context, id string) (*opensandbox.Sandbox, error) {
	if f.getSandboxFunc != nil {
		return f.getSandboxFunc(id)
	}
	return &opensandbox.Sandbox{ID: id, Status: opensandbox.SandboxStatus{State: "Running"}}, nil
}

func (f *fakeSandboxClient) ListSandboxes(_ context.Context) ([]opensandbox.Sandbox, error) {
	if f.listSandboxesFunc != nil {
		return f.listSandboxesFunc()
	}
	return nil, nil
}

func (f *fakeSandboxClient) DeleteSandbox(_ context.Context, id string) error {
	f.mu.Lock()
	f.deleteCalls = append(f.deleteCalls, id)
	f.mu.Unlock()
	if f.deleteSandboxFunc != nil {
		return f.deleteSandboxFunc(id)
	}
	return nil
}

func (f *fakeSandboxClient) PauseSandbox(_ context.Context, id string) error {
	f.mu.Lock()
	f.pauseCalls = append(f.pauseCalls, id)
	f.mu.Unlock()
	if f.pauseSandboxFunc != nil {
		return f.pauseSandboxFunc(id)
	}
	return nil
}

func (f *fakeSandboxClient) ResumeSandbox(_ context.Context, id string) error {
	f.mu.Lock()
	f.resumeCalls = append(f.resumeCalls, id)
	f.mu.Unlock()
	if f.resumeSandboxFunc != nil {
		return f.resumeSandboxFunc(id)
	}
	return nil
}

func (f *fakeSandboxClient) Endpoint(_ context.Context, id string, port int, useServerProxy bool) (string, error) {
	if f.endpointFunc != nil {
		return f.endpointFunc(id, port, useServerProxy)
	}
	return "https://sandbox.example.com:8000", nil
}

func (f *fakeSandboxClient) createCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.createCalls)
}

const testAPIKey = "test-key"

func newTestServer(t *testing.T, fake *fakeSandboxClient, singleUse bool) (*Server, *levels.State) {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "level.state")
	state := levels.New(statePath, singleUse)
	srv := New(Config{SandboxAPIKey: testAPIKey}, state, fake)
	return srv, state
}

func doRequest(handler http.Handler, method, path, apiKey string, body interface{}) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, reader)
	if apiKey != "" {
		req.Header.Set(sandboxAPIKeyHeader, apiKey)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAuth_RejectsMissingOrWrongKey(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSandboxClient{}, true)
	handler := srv.Handler()

	if rec := doRequest(handler, http.MethodGet, "/health", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("missing key: status = %d, want 401", rec.Code)
	}
	if rec := doRequest(handler, http.MethodGet, "/health", "wrong-key", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: status = %d, want 401", rec.Code)
	}
	if rec := doRequest(handler, http.MethodGet, "/health", testAPIKey, nil); rec.Code == http.StatusUnauthorized {
		t.Errorf("correct key: status = 401, want request to pass auth")
	}
}

func TestStart_AskStateReturns409AndDoesNotCallCreate(t *testing.T) {
	fake := &fakeSandboxClient{}
	srv, _ := newTestServer(t, fake, true)
	handler := srv.Handler()

	rec := doRequest(handler, http.MethodPost, "/start", testAPIKey, startRequest{
		Image:     "ghcr.io/openhands/agent-server:1.26.0-python",
		Command:   []string{"/start"},
		SessionID: "sess-1",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var resp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if !strings.Contains(resp.Error, "ai-level") {
		t.Errorf("error message = %q, want it to mention ai-level", resp.Error)
	}
	if fake.createCallCount() != 0 {
		t.Errorf("CreateSandbox called %d times, want 0", fake.createCallCount())
	}
}

func TestStart_HappyPath(t *testing.T) {
	fake := &fakeSandboxClient{
		createSandboxFunc: func(req opensandbox.CreateSandboxRequest) (*opensandbox.Sandbox, error) {
			return &opensandbox.Sandbox{ID: "sbx_123", Status: opensandbox.SandboxStatus{State: "Running"}}, nil
		},
		endpointFunc: func(id string, port int, useServerProxy bool) (string, error) {
			if id != "sbx_123" || port != agentServerPort {
				t.Errorf("Endpoint called with id=%q port=%d, want sbx_123/%d", id, port, agentServerPort)
			}
			return "https://sandbox.example.com:8000", nil
		},
	}
	srv, state := newTestServer(t, fake, true)
	if err := state.Set(levels.GitHub); err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()

	rec := doRequest(handler, http.MethodPost, "/start", testAPIKey, startRequest{
		Image:          "ghcr.io/openhands/agent-server:1.26.0-python",
		Command:        []string{"/start"},
		Environment:    map[string]string{"LLM_MODEL": "ollama/devstral"},
		SessionID:      "sess-1",
		ResourceFactor: 2,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp startResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RuntimeID != "sbx_123" {
		t.Errorf("RuntimeID = %q", resp.RuntimeID)
	}
	if resp.SessionID != "sess-1" {
		t.Errorf("SessionID = %q", resp.SessionID)
	}
	if resp.URL != "https://sandbox.example.com:8000" {
		t.Errorf("URL = %q", resp.URL)
	}
	if resp.SessionAPIKey == "" {
		t.Error("SessionAPIKey is empty")
	}
	if resp.Status != "running" || resp.PodStatus != "running" {
		t.Errorf("Status/PodStatus = %q/%q", resp.Status, resp.PodStatus)
	}
	if resp.WorkHosts == nil {
		t.Error("WorkHosts is nil, want {}")
	}

	if fake.createCallCount() != 1 {
		t.Fatalf("CreateSandbox called %d times, want 1", fake.createCallCount())
	}
	sent := fake.createCalls[0]
	if sent.Environment[sessionAPIKeyEnvVar] != resp.SessionAPIKey {
		t.Errorf("sent environment[%s] = %q, want %q", sessionAPIKeyEnvVar, sent.Environment[sessionAPIKeyEnvVar], resp.SessionAPIKey)
	}
	if sent.Environment["LLM_MODEL"] != "ollama/devstral" {
		t.Error("sent environment did not preserve LLM_MODEL from the request")
	}
	if sent.ResourceLimits == nil || sent.ResourceLimits.CPU != "2000m" || sent.ResourceLimits.Memory != "4096Mi" {
		t.Errorf("ResourceLimits = %+v, want CPU=2000m Memory=4096Mi (factor 2)", sent.ResourceLimits)
	}
	if sent.NetworkPolicy == nil || sent.NetworkPolicy.DefaultAction != "deny" {
		t.Errorf("NetworkPolicy = %+v, want GitHub-level deny policy", sent.NetworkPolicy)
	}
	if sent.Timeout != sandboxTimeoutSeconds {
		t.Errorf("Timeout = %d, want %d", sent.Timeout, sandboxTimeoutSeconds)
	}

	level, err := state.Peek()
	if err != nil {
		t.Fatal(err)
	}
	if level != levels.Ask {
		t.Errorf("state after start = %q, want Ask (consumed)", level)
	}
}

func TestStart_ResourceFactorDefaultsToOneWhenMissing(t *testing.T) {
	fake := &fakeSandboxClient{}
	srv, state := newTestServer(t, fake, true)
	if err := state.Set(levels.Full); err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()

	rec := doRequest(handler, http.MethodPost, "/start", testAPIKey, startRequest{
		Image: "img", Command: []string{"/start"}, SessionID: "sess-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	sent := fake.createCalls[0]
	if sent.ResourceLimits.CPU != "1000m" || sent.ResourceLimits.Memory != "2048Mi" {
		t.Errorf("ResourceLimits = %+v, want the factor-1 base values", sent.ResourceLimits)
	}
}

func TestStart_FullOmitsNetworkPolicy(t *testing.T) {
	fake := &fakeSandboxClient{}
	srv, state := newTestServer(t, fake, true)
	if err := state.Set(levels.Full); err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()

	rec := doRequest(handler, http.MethodPost, "/start", testAPIKey, startRequest{
		Image: "img", Command: []string{"/start"}, SessionID: "sess-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if fake.createCalls[0].NetworkPolicy != nil {
		t.Errorf("NetworkPolicy = %+v, want nil for Full", fake.createCalls[0].NetworkPolicy)
	}
}

func TestStart_RejectsOversizedBody(t *testing.T) {
	fake := &fakeSandboxClient{}
	srv, _ := newTestServer(t, fake, true)
	handler := srv.Handler()

	// A body larger than maxRequestBodyBytes, disguised as a legitimate
	// field so this exercises the size cap rather than a JSON syntax error.
	oversizedEnv := make(map[string]string, 1)
	oversizedEnv["PADDING"] = strings.Repeat("x", maxRequestBodyBytes+1)
	body, err := json.Marshal(startRequest{
		Image: "img", Command: []string{"/start"}, SessionID: "sess-1", Environment: oversizedEnv,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/start", bytes.NewReader(body))
	req.Header.Set(sandboxAPIKeyHeader, testAPIKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for oversized body", rec.Code)
	}
	if fake.createCallCount() != 0 {
		t.Errorf("CreateSandbox called %d times, want 0", fake.createCallCount())
	}
}

func TestStart_MissingSessionID(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSandboxClient{}, true)
	rec := doRequest(srv.Handler(), http.MethodPost, "/start", testAPIKey, startRequest{Image: "img", Command: []string{"/start"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestStart_CreateFailureRestoresLevel(t *testing.T) {
	fake := &fakeSandboxClient{
		createSandboxFunc: func(req opensandbox.CreateSandboxRequest) (*opensandbox.Sandbox, error) {
			return nil, errors.New("opensandbox unavailable")
		},
	}
	srv, state := newTestServer(t, fake, true)
	if err := state.Set(levels.Research); err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()

	rec := doRequest(handler, http.MethodPost, "/start", testAPIKey, startRequest{
		Image: "img", Command: []string{"/start"}, SessionID: "sess-1",
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}

	level, err := state.Peek()
	if err != nil {
		t.Fatal(err)
	}
	if level != levels.Research {
		t.Errorf("state after failed create = %q, want Research restored", level)
	}
}

func TestStart_EndpointFailureDoesNotRestoreLevel(t *testing.T) {
	fake := &fakeSandboxClient{
		createSandboxFunc: func(req opensandbox.CreateSandboxRequest) (*opensandbox.Sandbox, error) {
			return &opensandbox.Sandbox{ID: "sbx_1"}, nil
		},
		endpointFunc: func(id string, port int, useServerProxy bool) (string, error) {
			return "", errors.New("endpoint lookup failed")
		},
	}
	srv, state := newTestServer(t, fake, true)
	if err := state.Set(levels.Full); err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()

	rec := doRequest(handler, http.MethodPost, "/start", testAPIKey, startRequest{
		Image: "img", Command: []string{"/start"}, SessionID: "sess-1",
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}

	level, err := state.Peek()
	if err != nil {
		t.Fatal(err)
	}
	if level != levels.Ask {
		t.Errorf("state after endpoint-failed start = %q, want Ask (not restored - a sandbox exists)", level)
	}
}

// TestStart_ConcurrentSingleUse_OnlyOneSucceeds is the concurrency
// scenario called out explicitly in the implementation spec's acceptance
// criteria: with single_use_level enabled, one ai-level call followed by
// two concurrent /start calls must yield exactly one success and one
// ask-state conflict - never two successes.
func TestStart_ConcurrentSingleUse_OnlyOneSucceeds(t *testing.T) {
	var mu sync.Mutex
	created := 0
	fake := &fakeSandboxClient{
		createSandboxFunc: func(req opensandbox.CreateSandboxRequest) (*opensandbox.Sandbox, error) {
			mu.Lock()
			created++
			id := fmt.Sprintf("sbx_%d", created)
			mu.Unlock()
			return &opensandbox.Sandbox{ID: id}, nil
		},
	}
	srv, state := newTestServer(t, fake, true)
	if err := state.Set(levels.GitHub); err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()

	var wg sync.WaitGroup
	codes := make([]int, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			rec := doRequest(handler, http.MethodPost, "/start", testAPIKey, startRequest{
				Image: "img", Command: []string{"/start"}, SessionID: fmt.Sprintf("sess-%d", i),
			})
			codes[i] = rec.Code
		}()
	}
	wg.Wait()

	okCount, conflictCount := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			okCount++
		case http.StatusConflict:
			conflictCount++
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if okCount != 1 || conflictCount != 1 {
		t.Errorf("codes = %v, want exactly one 200 and one 409", codes)
	}
	if fake.createCallCount() != 1 {
		t.Errorf("CreateSandbox called %d times, want 1", fake.createCallCount())
	}
}

// TestStart_SequentialDifferentLevelsBothSucceed confirms the atomic
// consume doesn't block legitimate concurrent use - two conversations
// started one after another, each with its own ai-level call, both
// succeed independently.
func TestStart_SequentialDifferentLevelsBothSucceed(t *testing.T) {
	var mu sync.Mutex
	created := 0
	fake := &fakeSandboxClient{
		createSandboxFunc: func(req opensandbox.CreateSandboxRequest) (*opensandbox.Sandbox, error) {
			mu.Lock()
			created++
			id := fmt.Sprintf("sbx_%d", created)
			mu.Unlock()
			return &opensandbox.Sandbox{ID: id}, nil
		},
	}
	srv, state := newTestServer(t, fake, true)
	handler := srv.Handler()

	if err := state.Set(levels.GitHub); err != nil {
		t.Fatal(err)
	}
	rec1 := doRequest(handler, http.MethodPost, "/start", testAPIKey, startRequest{Image: "img", Command: []string{"/start"}, SessionID: "sess-a"})
	if rec1.Code != http.StatusOK {
		t.Fatalf("first start: status = %d, body=%s", rec1.Code, rec1.Body.String())
	}

	if err := state.Set(levels.Research); err != nil {
		t.Fatal(err)
	}
	rec2 := doRequest(handler, http.MethodPost, "/start", testAPIKey, startRequest{Image: "img", Command: []string{"/start"}, SessionID: "sess-b"})
	if rec2.Code != http.StatusOK {
		t.Fatalf("second start: status = %d, body=%s", rec2.Code, rec2.Body.String())
	}

	if fake.createCallCount() != 2 {
		t.Errorf("CreateSandbox called %d times, want 2", fake.createCallCount())
	}
}

func TestStopPauseResume(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		calls func(*fakeSandboxClient) []string
	}{
		{"stop", "/stop", func(f *fakeSandboxClient) []string { return f.deleteCalls }},
		{"pause", "/pause", func(f *fakeSandboxClient) []string { return f.pauseCalls }},
		{"resume", "/resume", func(f *fakeSandboxClient) []string { return f.resumeCalls }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeSandboxClient{}
			srv, _ := newTestServer(t, fake, true)
			handler := srv.Handler()

			rec := doRequest(handler, http.MethodPost, tt.path, testAPIKey, runtimeIDRequest{RuntimeID: "sbx_1"})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			calls := tt.calls(fake)
			if len(calls) != 1 || calls[0] != "sbx_1" {
				t.Errorf("calls = %v, want [sbx_1]", calls)
			}
		})
	}
}

func TestStop_MissingRuntimeID(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSandboxClient{}, true)
	rec := doRequest(srv.Handler(), http.MethodPost, "/stop", testAPIKey, runtimeIDRequest{})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestList(t *testing.T) {
	fake := &fakeSandboxClient{
		listSandboxesFunc: func() ([]opensandbox.Sandbox, error) {
			return []opensandbox.Sandbox{
				{ID: "sbx_1", Status: opensandbox.SandboxStatus{State: "Running"}},
				{ID: "sbx_2", Status: opensandbox.SandboxStatus{State: "Paused"}},
			}, nil
		},
	}
	srv, _ := newTestServer(t, fake, true)
	rec := doRequest(srv.Handler(), http.MethodGet, "/list", testAPIKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out []runtimeInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d runtimes, want 2", len(out))
	}
}

func TestGetRuntime(t *testing.T) {
	fake := &fakeSandboxClient{
		getSandboxFunc: func(id string) (*opensandbox.Sandbox, error) {
			if id != "sbx_1" {
				t.Errorf("GetSandbox called with %q, want sbx_1", id)
			}
			return &opensandbox.Sandbox{ID: id, Status: opensandbox.SandboxStatus{State: "Running"}}, nil
		},
	}
	srv, _ := newTestServer(t, fake, true)
	rec := doRequest(srv.Handler(), http.MethodGet, "/runtime/sbx_1", testAPIKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var info runtimeInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.RuntimeID != "sbx_1" || info.Status != "Running" {
		t.Errorf("info = %+v", info)
	}
}

func TestGetSession_LookupBySessionID(t *testing.T) {
	fake := &fakeSandboxClient{
		createSandboxFunc: func(req opensandbox.CreateSandboxRequest) (*opensandbox.Sandbox, error) {
			return &opensandbox.Sandbox{ID: "sbx_1", Status: opensandbox.SandboxStatus{State: "Running"}}, nil
		},
	}
	srv, state := newTestServer(t, fake, true)
	if err := state.Set(levels.Full); err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()

	if rec := doRequest(handler, http.MethodGet, "/sessions/sess-1", testAPIKey, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("before start: status = %d, want 404", rec.Code)
	}

	startRec := doRequest(handler, http.MethodPost, "/start", testAPIKey, startRequest{
		Image: "img", Command: []string{"/start"}, SessionID: "sess-1",
	})
	if startRec.Code != http.StatusOK {
		t.Fatalf("start: status = %d, body=%s", startRec.Code, startRec.Body.String())
	}

	rec := doRequest(handler, http.MethodGet, "/sessions/sess-1", testAPIKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("after start: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var info runtimeInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.RuntimeID != "sbx_1" {
		t.Errorf("RuntimeID = %q, want sbx_1", info.RuntimeID)
	}
}

func TestSessionsBatch(t *testing.T) {
	fake := &fakeSandboxClient{
		createSandboxFunc: func(req opensandbox.CreateSandboxRequest) (*opensandbox.Sandbox, error) {
			return &opensandbox.Sandbox{ID: "sbx_1", Status: opensandbox.SandboxStatus{State: "Running"}}, nil
		},
	}
	srv, state := newTestServer(t, fake, true)
	if err := state.Set(levels.Full); err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()

	startRec := doRequest(handler, http.MethodPost, "/start", testAPIKey, startRequest{
		Image: "img", Command: []string{"/start"}, SessionID: "sess-known",
	})
	if startRec.Code != http.StatusOK {
		t.Fatalf("start: status = %d, body=%s", startRec.Code, startRec.Body.String())
	}

	rec := doRequest(handler, http.MethodGet, "/sessions/batch?ids=sess-known,sess-unknown", testAPIKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var out map[string]*runtimeInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	known, ok := out["sess-known"]
	if !ok || known == nil || known.RuntimeID != "sbx_1" {
		t.Errorf("sess-known = %+v", known)
	}
	unknown, present := out["sess-unknown"]
	if !present || unknown != nil {
		t.Errorf("sess-unknown = %+v, want present with nil value", unknown)
	}
}

func TestRegistryPrefix(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSandboxClient{}, true)
	rec := doRequest(srv.Handler(), http.MethodGet, "/registry_prefix", testAPIKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["registry_prefix"] != registryPrefix {
		t.Errorf("registry_prefix = %q, want %q", out["registry_prefix"], registryPrefix)
	}
}

func TestImageExists(t *testing.T) {
	srv, _ := newTestServer(t, &fakeSandboxClient{}, true)
	rec := doRequest(srv.Handler(), http.MethodGet, "/image_exists?image=ghcr.io/openhands/agent-server:1.26.0-python", testAPIKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out["exists"] {
		t.Error("exists = false, want true (unconditional per spec for v1)")
	}
}

func TestHealth_HealthyWhenOpenSandboxReachable(t *testing.T) {
	fake := &fakeSandboxClient{listSandboxesFunc: func() ([]opensandbox.Sandbox, error) { return nil, nil }}
	srv, _ := newTestServer(t, fake, true)
	rec := doRequest(srv.Handler(), http.MethodGet, "/health", testAPIKey, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHealth_UnhealthyWhenOpenSandboxUnreachable(t *testing.T) {
	fake := &fakeSandboxClient{listSandboxesFunc: func() ([]opensandbox.Sandbox, error) {
		return nil, errors.New("connection refused")
	}}
	srv, _ := newTestServer(t, fake, true)
	rec := doRequest(srv.Handler(), http.MethodGet, "/health", testAPIKey, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}
