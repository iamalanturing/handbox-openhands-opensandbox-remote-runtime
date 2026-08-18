// Package opensandbox is an HTTP client for the OpenSandbox sandbox lifecycle API.
package opensandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const apiKeyHeader = "OPEN-SANDBOX-API-KEY"

// Client is an HTTP client for the OpenSandbox sandbox lifecycle API.
// PATCH /sandboxes/{id}/metadata is deliberately not implemented — the
// spec calls it out as not needed for v1.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient returns a Client for the OpenSandbox API at baseURL,
// authenticating with apiKey (sent as the OPEN-SANDBOX-API-KEY header on
// every request). httpClient may be nil, in which case http.DefaultClient
// is used; tests pass one pointed at an httptest.Server.
func NewClient(baseURL, apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: httpClient,
	}
}

// APIError is returned when OpenSandbox responds with a non-2xx status.
// The spec doesn't document an error response body schema, so this
// preserves the raw body rather than assuming one.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("opensandbox: unexpected status %d: %s", e.StatusCode, e.Body)
}

// CreateSandbox issues POST /sandboxes. OpenSandbox responds 202 Accepted
// with a partial Sandbox; callers needing fields it omits should follow up
// with GetSandbox.
func (c *Client) CreateSandbox(ctx context.Context, req CreateSandboxRequest) (*Sandbox, error) {
	var sb Sandbox
	if err := c.do(ctx, http.MethodPost, "/sandboxes", req, &sb); err != nil {
		return nil, err
	}
	return &sb, nil
}

// GetSandbox issues GET /sandboxes/{id}.
func (c *Client) GetSandbox(ctx context.Context, id string) (*Sandbox, error) {
	var sb Sandbox
	if err := c.do(ctx, http.MethodGet, "/sandboxes/"+url.PathEscape(id), nil, &sb); err != nil {
		return nil, err
	}
	return &sb, nil
}

// ListSandboxes issues GET /sandboxes.
func (c *Client) ListSandboxes(ctx context.Context) ([]Sandbox, error) {
	var sbs []Sandbox
	if err := c.do(ctx, http.MethodGet, "/sandboxes", nil, &sbs); err != nil {
		return nil, err
	}
	return sbs, nil
}

// DeleteSandbox issues DELETE /sandboxes/{id}.
func (c *Client) DeleteSandbox(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/sandboxes/"+url.PathEscape(id), nil, nil)
}

// PauseSandbox issues POST /sandboxes/{id}/pause.
func (c *Client) PauseSandbox(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/sandboxes/"+url.PathEscape(id)+"/pause", nil, nil)
}

// ResumeSandbox issues POST /sandboxes/{id}/resume.
func (c *Client) ResumeSandbox(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/sandboxes/"+url.PathEscape(id)+"/resume", nil, nil)
}

// Endpoint issues GET /sandboxes/{id}/endpoints/{port} and returns the
// resulting URL. useServerProxy controls the use_server_proxy query
// param — the spec flags which setting is actually reachable from
// handbox's network position as unverified (Known Unknown #3).
func (c *Client) Endpoint(ctx context.Context, id string, port int, useServerProxy bool) (string, error) {
	path := fmt.Sprintf("/sandboxes/%s/endpoints/%d?use_server_proxy=%t", url.PathEscape(id), port, useServerProxy)
	var resp EndpointResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return "", err
	}
	return resp.URL, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set(apiKeyHeader, c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("opensandbox request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
