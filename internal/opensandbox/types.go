package opensandbox

// NetworkPolicy is the OpenSandbox egress policy attached to a sandbox at
// creation time. A nil *NetworkPolicy means the field is omitted from the
// create request entirely, which results in no egress sidecar and
// unrestricted default networking — distinct from a non-nil NetworkPolicy
// with DefaultAction left empty, which OpenSandbox itself defaults to
// "deny".
type NetworkPolicy struct {
	DefaultAction string       `json:"defaultAction,omitempty"`
	Egress        []EgressRule `json:"egress,omitempty"`
}

// EgressRule is a single allow rule within a NetworkPolicy. Target accepts
// an FQDN or wildcard domain ("example.com", "*.example.com") — IP/CIDR is
// not supported by OpenSandbox's egress MVP.
type EgressRule struct {
	Action string `json:"action"`
	Target string `json:"target"`
}

// Image identifies the container image to create a sandbox from.
type Image struct {
	URI string `json:"uri"`
}

// ResourceLimits caps a sandbox's CPU/memory, e.g. "1000m" / "2Gi".
type ResourceLimits struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// CreateSandboxRequest is the body of POST /sandboxes. Entrypoint is
// required when creating from Image (not required when restoring from a
// snapshot, which handbox v1 never does). NetworkPolicy is optional; a nil
// pointer here must serialize as an absent field, not null or {} — per the
// spec, omitting the field entirely and sending an empty object mean
// different things to OpenSandbox (no egress sidecar vs. an allow-all
// sidecar).
//
// Environment is a new assumption, not confirmed from the spec: the
// create-sandbox example in Section 5 doesn't show any environment
// variable mechanism at all, even though OpenHands' /start request always
// carries one (LLM_MODEL, etc.) that has to reach the container somehow.
// "env" as a plain string map is a guess at the field name and shape,
// picked because it's the most common convention and matches this API's
// existing camelCase style — not verified against the real OpenAPI spec.
// Flag this explicitly when testing against a live OpenSandbox instance.
type CreateSandboxRequest struct {
	Image          Image             `json:"image"`
	Entrypoint     []string          `json:"entrypoint"`
	Timeout        int               `json:"timeout,omitempty"`
	ResourceLimits *ResourceLimits   `json:"resourceLimits,omitempty"`
	NetworkPolicy  *NetworkPolicy    `json:"networkPolicy,omitempty"`
	Environment    map[string]string `json:"env,omitempty"`
}

// SandboxStatus is a sandbox's lifecycle state as reported by OpenSandbox.
type SandboxStatus struct {
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// Sandbox is OpenSandbox's representation of a sandbox, as returned by
// create/get/list. The create response deliberately omits some fields per
// the spec's own description; callers needing more should follow up with
// GetSandbox.
type Sandbox struct {
	ID         string                 `json:"id"`
	Status     SandboxStatus          `json:"status"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt  string                 `json:"createdAt,omitempty"`
	Entrypoint []string               `json:"entrypoint,omitempty"`
}

// EndpointResponse is the body of GET /sandboxes/{id}/endpoints/{port}.
// The spec documents this endpoint's purpose ("the public URL for a
// port") but not its exact response shape — this assumes {"url": "..."}
// pending verification against a live OpenSandbox instance (implementation
// spec Known Unknown #3, on use_server_proxy behavior).
type EndpointResponse struct {
	URL string `json:"url"`
}
