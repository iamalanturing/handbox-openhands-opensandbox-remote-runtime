package opensandbox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "test-api-key", srv.Client()), srv
}

func TestNewClient_DefaultsToATimeoutWhenHTTPClientIsNil(t *testing.T) {
	// http.DefaultClient has no timeout at all - a hung OpenSandbox server
	// would block the caller forever. NewClient(nil) must not fall back to
	// it directly.
	c := NewClient("https://example.com", "key", nil)
	if c.httpClient.Timeout <= 0 {
		t.Errorf("httpClient.Timeout = %v, want a positive default", c.httpClient.Timeout)
	}
	if c.httpClient == http.DefaultClient {
		t.Error("httpClient is http.DefaultClient, want a client with its own timeout")
	}
}

func TestCreateSandbox_RequestShapeAndResponseParsing(t *testing.T) {
	var gotMethod, gotPath, gotHeader string
	var gotBody []byte

	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotHeader = r.Header.Get(apiKeyHeader)
		gotBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"id":"sbx_abc123","status":{"state":"Running"},"metadata":{},"createdAt":"2026-08-15T00:00:00Z","entrypoint":["/start"]}`))
	})

	req := CreateSandboxRequest{
		Image:      Image{URI: "ghcr.io/openhands/agent-server:1.26.0-python"},
		Entrypoint: []string{"/start"},
		Timeout:    3600,
		ResourceLimits: &ResourceLimits{
			CPU:    "1000m",
			Memory: "2Gi",
		},
		NetworkPolicy: &NetworkPolicy{
			DefaultAction: "deny",
			Egress:        []EgressRule{{Action: "allow", Target: "github.com"}},
		},
	}

	sb, err := client.CreateSandbox(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/sandboxes" {
		t.Errorf("path = %q, want /sandboxes", gotPath)
	}
	if gotHeader != "test-api-key" {
		t.Errorf("%s header = %q, want test-api-key", apiKeyHeader, gotHeader)
	}

	var sent map[string]interface{}
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	image, _ := sent["image"].(map[string]interface{})
	if image["uri"] != "ghcr.io/openhands/agent-server:1.26.0-python" {
		t.Errorf("image.uri = %v", image["uri"])
	}
	if sent["timeout"].(float64) != 3600 {
		t.Errorf("timeout = %v", sent["timeout"])
	}
	networkPolicy, _ := sent["networkPolicy"].(map[string]interface{})
	if networkPolicy["defaultAction"] != "deny" {
		t.Errorf("networkPolicy.defaultAction = %v", networkPolicy["defaultAction"])
	}

	if sb.ID != "sbx_abc123" {
		t.Errorf("ID = %q", sb.ID)
	}
	if sb.Status.State != "Running" {
		t.Errorf("Status.State = %q", sb.Status.State)
	}
}

func TestCreateSandbox_NilNetworkPolicyOmittedFromRequest(t *testing.T) {
	var gotBody []byte
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"id":"sbx_1","status":{"state":"Running"}}`))
	})

	req := CreateSandboxRequest{
		Image:      Image{URI: "ghcr.io/openhands/agent-server:1.26.0-python"},
		Entrypoint: []string{"/start"},
		// NetworkPolicy intentionally nil - Full/Offline-without-Ollama-host
		// semantics depend on this being an absent key, not null.
	}
	if _, err := client.CreateSandbox(context.Background(), req); err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}

	var sent map[string]interface{}
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body not valid JSON: %v", err)
	}
	if _, present := sent["networkPolicy"]; present {
		t.Errorf("networkPolicy key present in request body = %s, want field omitted entirely", gotBody)
	}
}

func TestGetSandbox(t *testing.T) {
	var gotMethod, gotPath string
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		json.NewEncoder(w).Encode(Sandbox{ID: "sbx_1", Status: SandboxStatus{State: "Running"}})
	})

	sb, err := client.GetSandbox(context.Background(), "sbx_1")
	if err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/sandboxes/sbx_1" {
		t.Errorf("got %s %s, want GET /sandboxes/sbx_1", gotMethod, gotPath)
	}
	if sb.ID != "sbx_1" {
		t.Errorf("ID = %q", sb.ID)
	}
}

func TestListSandboxes(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/sandboxes" {
			t.Errorf("got %s %s, want GET /sandboxes", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode([]Sandbox{{ID: "sbx_1"}, {ID: "sbx_2"}})
	})

	sbs, err := client.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes() error = %v", err)
	}
	if len(sbs) != 2 {
		t.Fatalf("got %d sandboxes, want 2", len(sbs))
	}
}

func TestDeletePauseResumeSandbox(t *testing.T) {
	tests := []struct {
		name       string
		call       func(*Client) error
		wantMethod string
		wantPath   string
	}{
		{"delete", func(c *Client) error { return c.DeleteSandbox(context.Background(), "sbx_1") }, http.MethodDelete, "/sandboxes/sbx_1"},
		{"pause", func(c *Client) error { return c.PauseSandbox(context.Background(), "sbx_1") }, http.MethodPost, "/sandboxes/sbx_1/pause"},
		{"resume", func(c *Client) error { return c.ResumeSandbox(context.Background(), "sbx_1") }, http.MethodPost, "/sandboxes/sbx_1/resume"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath string
			client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				w.WriteHeader(http.StatusNoContent)
			})
			if err := tt.call(client); err != nil {
				t.Fatalf("%s() error = %v", tt.name, err)
			}
			if gotMethod != tt.wantMethod || gotPath != tt.wantPath {
				t.Errorf("got %s %s, want %s %s", gotMethod, gotPath, tt.wantMethod, tt.wantPath)
			}
		})
	}
}

func TestEndpoint(t *testing.T) {
	var gotPath, gotQuery string
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(EndpointResponse{URL: "https://sandbox.example.com:8000"})
	})

	got, err := client.Endpoint(context.Background(), "sbx_1", 8000, true)
	if err != nil {
		t.Fatalf("Endpoint() error = %v", err)
	}
	if gotPath != "/sandboxes/sbx_1/endpoints/8000" {
		t.Errorf("path = %q", gotPath)
	}
	if gotQuery != "use_server_proxy=true" {
		t.Errorf("query = %q", gotQuery)
	}
	if got != "https://sandbox.example.com:8000" {
		t.Errorf("Endpoint() = %q", got)
	}
}

func TestAPIError_OnNon2xxStatus(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"sandbox not found"}`))
	})

	_, err := client.GetSandbox(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("GetSandbox() error = nil, want APIError")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "sandbox not found") {
		t.Errorf("Body = %q, want it to contain the response body", apiErr.Body)
	}
	if !strings.Contains(apiErr.Error(), "404") {
		t.Errorf("Error() = %q, want it to mention the status code", apiErr.Error())
	}
}

func TestNewClient_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(Sandbox{ID: "sbx_1"})
	}))
	t.Cleanup(srv.Close)

	client := NewClient(srv.URL+"/", "key", srv.Client())
	if _, err := client.GetSandbox(context.Background(), "sbx_1"); err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	if gotPath != "/sandboxes/sbx_1" {
		t.Errorf("path = %q, want /sandboxes/sbx_1 (no double slash)", gotPath)
	}
}
