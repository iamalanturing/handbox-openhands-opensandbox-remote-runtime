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
