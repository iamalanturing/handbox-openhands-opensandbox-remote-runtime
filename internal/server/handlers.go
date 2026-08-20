package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/levels"
	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/opensandbox"
)

// agentServerPort is the port ghcr.io/openhands/agent-server listens on,
// confirmed from observed port mappings on running oh-agent-server-*
// containers (implementation spec Section 5).
const agentServerPort = 8000

// sandboxTimeoutSeconds matches the OpenSandbox create-sandbox example in
// the spec. Not made configurable for v1 — the spec doesn't call it out
// as a required config item, unlike the fields internal/config covers.
const sandboxTimeoutSeconds = 3600

// baseCPUMilli and baseMemoryMiB are the resourceLimits handbox sends for
// resource_factor == 1, matching the spec's own "1000m"/"2Gi" example.
// Other factors scale linearly. OpenHands' /start doesn't document what
// resource_factor is meant to scale from, so this mapping is an
// assumption, not a confirmed contract.
const (
	baseCPUMilli  = 1000
	baseMemoryMiB = 2048
)

// sessionAPIKeyEnvVar is confirmed (not guessed): the agent-server checks
// SESSION_API_KEY (or OH_SESSION_API_KEYS_*) against the X-Session-API-Key
// header on incoming requests. handbox generates this value itself and
// must inject it into the sandbox's environment for the key it hands back
// to OpenHands to actually work.
const sessionAPIKeyEnvVar = "SESSION_API_KEY"

// registryPrefix is static: handbox handles exactly one image
// (ghcr.io/openhands/agent-server) in v1.
const registryPrefix = "ghcr.io/openhands"

// maxRequestBodyBytes caps every decoded request body. Auth runs before
// any body is read, so this only bounds a caller that already holds a
// valid SANDBOX_API_KEY - but "authenticated" isn't the same as
// "trusted to send an unbounded body", and json.Decode has no size limit
// of its own.
const maxRequestBodyBytes = 1 << 20 // 1 MiB

type startRequest struct {
	Image          string            `json:"image"`
	Command        []string          `json:"command"`
	WorkingDir     string            `json:"working_dir"`
	Environment    map[string]string `json:"environment"`
	SessionID      string            `json:"session_id"`
	ResourceFactor int               `json:"resource_factor"`
	// RuntimeClass is accepted but intentionally ignored — secure runtime
	// (Kata) is a one-time OpenSandbox server-side config, not per-request.
	RuntimeClass string `json:"runtime_class,omitempty"`
}

type startResponse struct {
	RuntimeID     string            `json:"runtime_id"`
	SessionID     string            `json:"session_id"`
	URL           string            `json:"url"`
	SessionAPIKey string            `json:"session_api_key"`
	Status        string            `json:"status"`
	PodStatus     string            `json:"pod_status"`
	WorkHosts     map[string]string `json:"work_hosts"`
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	level, err := s.state.Consume()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read network level: "+err.Error())
		return
	}

	policy, err := levels.Policy(level, s.cfg.ResearchAllowlist, s.cfg.OllamaHost)
	if errors.Is(err, levels.ErrAskRequired) {
		writeError(w, http.StatusConflict, "No network level selected. Run: ai-level <offline|github|research|full>")
		return
	}
	if err != nil {
		// Config already validates OllamaHost at startup, so this
		// shouldn't be reachable in practice - defense in depth only.
		writeError(w, http.StatusInternalServerError, "failed to build network policy: "+err.Error())
		return
	}

	sessionAPIKey, err := generateSessionAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate session API key")
		return
	}

	environment := make(map[string]string, len(req.Environment)+1)
	for k, v := range req.Environment {
		environment[k] = v
	}
	environment[sessionAPIKeyEnvVar] = sessionAPIKey

	factor := req.ResourceFactor
	if factor <= 0 {
		factor = 1
	}

	createReq := opensandbox.CreateSandboxRequest{
		Image:      opensandbox.Image{URI: req.Image},
		Entrypoint: req.Command,
		Timeout:    sandboxTimeoutSeconds,
		ResourceLimits: &opensandbox.ResourceLimits{
			CPU:    fmt.Sprintf("%dm", baseCPUMilli*factor),
			Memory: fmt.Sprintf("%dMi", baseMemoryMiB*factor),
		},
		NetworkPolicy: policy,
		Environment:   environment,
	}

	sb, err := s.sandbox.CreateSandbox(r.Context(), createReq)
	if err != nil {
		// Best-effort restore: nothing was actually created in OpenSandbox,
		// so a transient failure here shouldn't silently burn the user's
		// ai-level selection for nothing. Safe to call unconditionally -
		// it's a no-op when single_use_level is off (the file was never
		// reset in the first place). Narrow accepted race: a concurrent
		// ai-level call landing in this exact window could be clobbered by
		// the restore.
		if level != levels.Ask {
			_ = s.state.Set(level)
		}
		writeError(w, http.StatusBadGateway, "failed to create sandbox: "+err.Error())
		return
	}

	url, err := s.sandbox.Endpoint(r.Context(), sb.ID, agentServerPort, true)
	if err != nil {
		// Deliberately NOT restoring the level here: a real sandbox now
		// exists in OpenSandbox. Restoring would invite the user to retry
		// and create a second one, compounding the problem instead of
		// fixing it.
		writeError(w, http.StatusBadGateway, "sandbox created but failed to resolve endpoint: "+err.Error())
		return
	}

	s.registry.put(&runtimeRecord{SessionID: req.SessionID, RuntimeID: sb.ID, SessionAPIKey: sessionAPIKey})

	writeJSON(w, http.StatusOK, startResponse{
		RuntimeID:     sb.ID,
		SessionID:     req.SessionID,
		URL:           url,
		SessionAPIKey: sessionAPIKey,
		Status:        "running",
		PodStatus:     "running",
		WorkHosts:     map[string]string{},
	})
}

func generateSessionAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type runtimeIDRequest struct {
	RuntimeID string `json:"runtime_id"`
}

func (s *Server) decodeRuntimeIDRequest(w http.ResponseWriter, r *http.Request) (runtimeIDRequest, bool) {
	var req runtimeIDRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RuntimeID == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return runtimeIDRequest{}, false
	}
	return req, true
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeRuntimeIDRequest(w, r)
	if !ok {
		return
	}
	if err := s.sandbox.DeleteSandbox(r.Context(), req.RuntimeID); err != nil {
		writeError(w, http.StatusBadGateway, "failed to stop sandbox: "+err.Error())
		return
	}
	s.registry.deleteByRuntimeID(req.RuntimeID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeRuntimeIDRequest(w, r)
	if !ok {
		return
	}
	if err := s.sandbox.PauseSandbox(r.Context(), req.RuntimeID); err != nil {
		writeError(w, http.StatusBadGateway, "failed to pause sandbox: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeRuntimeIDRequest(w, r)
	if !ok {
		return
	}
	if err := s.sandbox.ResumeSandbox(r.Context(), req.RuntimeID); err != nil {
		writeError(w, http.StatusBadGateway, "failed to resume sandbox: "+err.Error())
		return
	}
	url, err := s.sandbox.Endpoint(r.Context(), req.RuntimeID, agentServerPort, true)
	if err != nil {
		writeError(w, http.StatusBadGateway, "sandbox resumed but failed to resolve endpoint: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "running", "runtime_id": req.RuntimeID, "url": url})
}

type runtimeInfo struct {
	RuntimeID string `json:"runtime_id"`
	Status    string `json:"status"`
}

func toRuntimeInfo(sb opensandbox.Sandbox) runtimeInfo {
	return runtimeInfo{RuntimeID: sb.ID, Status: sb.Status.State}
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	sbs, err := s.sandbox.ListSandboxes(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to list sandboxes: "+err.Error())
		return
	}
	out := make([]runtimeInfo, 0, len(sbs))
	for _, sb := range sbs {
		out = append(out, toRuntimeInfo(sb))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetRuntime(w http.ResponseWriter, r *http.Request) {
	runtimeID := r.PathValue("runtime_id")
	sb, err := s.sandbox.GetSandbox(r.Context(), runtimeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toRuntimeInfo(*sb))
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	rec, ok := s.registry.getBySessionID(sessionID)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown session_id")
		return
	}
	sb, err := s.sandbox.GetSandbox(r.Context(), rec.RuntimeID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to fetch runtime: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toRuntimeInfo(*sb))
}

func (s *Server) handleSessionsBatch(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("ids")
	result := make(map[string]*runtimeInfo)
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		rec, ok := s.registry.getBySessionID(id)
		if !ok {
			result[id] = nil
			continue
		}
		sb, err := s.sandbox.GetSandbox(r.Context(), rec.RuntimeID)
		if err != nil {
			result[id] = nil
			continue
		}
		info := toRuntimeInfo(*sb)
		result[id] = &info
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRegistryPrefix(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"registry_prefix": registryPrefix})
}

func (s *Server) handleImageExists(w http.ResponseWriter, r *http.Request) {
	// Per the spec: safe to return true unconditionally for v1, since the
	// image is expected to already be pulled as a prerequisite.
	writeJSON(w, http.StatusOK, map[string]bool{"exists": true})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// OpenSandbox's documented API table (spec Section 5) doesn't include
	// a dedicated health endpoint, so this uses GET /sandboxes as a
	// reachability + auth check - it's the lightest documented endpoint
	// available, not a confirmed health-check mechanism.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, err := s.sandbox.ListSandboxes(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}
