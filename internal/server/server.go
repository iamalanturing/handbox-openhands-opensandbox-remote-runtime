// Package server implements the OpenHands Remote Runtime HTTP API contract.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/levels"
	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/opensandbox"
)

// sandboxClient is the subset of *opensandbox.Client the server needs.
// Declaring it here (rather than depending on the concrete type directly)
// lets tests inject a fake without hitting a real OpenSandbox instance —
// *opensandbox.Client satisfies this interface structurally.
type sandboxClient interface {
	CreateSandbox(ctx context.Context, req opensandbox.CreateSandboxRequest) (*opensandbox.Sandbox, error)
	GetSandbox(ctx context.Context, id string) (*opensandbox.Sandbox, error)
	ListSandboxes(ctx context.Context) ([]opensandbox.Sandbox, error)
	DeleteSandbox(ctx context.Context, id string) error
	PauseSandbox(ctx context.Context, id string) error
	ResumeSandbox(ctx context.Context, id string) error
	Endpoint(ctx context.Context, id string, port int, useServerProxy bool) (string, error)
}

// Config is the subset of handbox's own configuration the server needs.
type Config struct {
	// SandboxAPIKey is the value OpenHands must present for every request
	// to be accepted.
	SandboxAPIKey string
	// ResearchAllowlist and OllamaHost feed straight into levels.Policy.
	ResearchAllowlist []string
	OllamaHost        string
}

// Server implements the OpenHands Remote Runtime HTTP API, translating
// each call into the equivalent OpenSandbox API call via sandbox, and
// applying the network-level policy from state on every /start.
type Server struct {
	cfg      Config
	state    *levels.State
	sandbox  sandboxClient
	registry *registry
}

// New returns a Server. sandbox is typically *opensandbox.Client in
// production and a fake in tests.
func New(cfg Config, state *levels.State, sandbox sandboxClient) *Server {
	return &Server{
		cfg:      cfg,
		state:    state,
		sandbox:  sandbox,
		registry: newRegistry(),
	}
}

// Handler returns the http.Handler serving the full Remote Runtime API,
// with API-key auth applied to every route.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /start", s.handleStart)
	mux.HandleFunc("POST /stop", s.handleStop)
	mux.HandleFunc("POST /pause", s.handlePause)
	mux.HandleFunc("POST /resume", s.handleResume)
	mux.HandleFunc("GET /list", s.handleList)
	mux.HandleFunc("GET /runtime/{runtime_id}", s.handleGetRuntime)
	mux.HandleFunc("GET /sessions/batch", s.handleSessionsBatch)
	mux.HandleFunc("GET /sessions/{session_id}", s.handleGetSession)
	mux.HandleFunc("GET /registry_prefix", s.handleRegistryPrefix)
	mux.HandleFunc("GET /image_exists", s.handleImageExists)
	mux.HandleFunc("GET /health", s.handleHealth)
	return s.withAuth(mux)
}

// sandboxAPIKeyHeader is an assumption, not confirmed from any source
// available to this project: OpenHands sets SANDBOX_API_KEY as an env var
// on its own side, but nothing in the spec documents which HTTP header it
// sends that value in on outgoing Remote Runtime requests. X-API-Key is a
// common convention for simple bearer-style API key auth; verify this
// against a real openhands-app before relying on it.
const sandboxAPIKeyHeader = "X-API-Key"

// withAuth validates SANDBOX_API_KEY on every request per the spec's
// Section 7 requirement ("validate ... before processing anything") —
// applied uniformly, including /health, since the spec says "every
// request" without carving out an exception.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(sandboxAPIKeyHeader)
		// Constant-time comparison: a plain != leaks timing information
		// proportional to the number of matching leading bytes, which is
		// exactly the kind of oracle that makes brute-forcing a secret
		// practical over enough requests.
		match := s.cfg.SandboxAPIKey != "" &&
			subtle.ConstantTimeCompare([]byte(key), []byte(s.cfg.SandboxAPIKey)) == 1
		if !match {
			writeError(w, http.StatusUnauthorized, "invalid or missing API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

// runtimeRecord is what handbox itself needs to remember about a sandbox
// it created, beyond what OpenSandbox's API already tracks — namely the
// OpenHands session_id it belongs to (for the session-keyed endpoints)
// and the session_api_key generated for it (ephemeral, only ever
// returned once at /start time, so it must be kept here to be reused).
// In-memory only; lost on restart is accepted for v1 per the spec.
type runtimeRecord struct {
	SessionID     string
	RuntimeID     string
	SessionAPIKey string
}

// registry maps between OpenHands session_id and the OpenSandbox sandbox
// id (== runtime_id). Most endpoints don't need it at all: per the spec,
// /start's response makes runtime_id literally the OpenSandbox sandbox
// id, so /stop, /pause, /resume, and GET /runtime/{id} can pass it
// straight through to the OpenSandbox client statelessly. Only the two
// session_id-keyed endpoints (GET /sessions/{id}, /sessions/batch) need
// this lookup.
type registry struct {
	mu          sync.RWMutex
	byRuntimeID map[string]*runtimeRecord
	bySessionID map[string]*runtimeRecord
}

func newRegistry() *registry {
	return &registry{
		byRuntimeID: make(map[string]*runtimeRecord),
		bySessionID: make(map[string]*runtimeRecord),
	}
}

func (r *registry) put(rec *runtimeRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byRuntimeID[rec.RuntimeID] = rec
	r.bySessionID[rec.SessionID] = rec
}

func (r *registry) getBySessionID(id string) (*runtimeRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.bySessionID[id]
	return rec, ok
}

func (r *registry) deleteByRuntimeID(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.byRuntimeID[id]; ok {
		delete(r.byRuntimeID, id)
		delete(r.bySessionID, rec.SessionID)
	}
}
