package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Sibuxiangx/nicedroid/internal/config"
	"github.com/google/uuid"
)

// Client talks to the Factory LLM gateway.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New builds a gateway client.
func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP: &http.Client{
			Timeout: 0, // streaming; per-request context enforces deadlines
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 32,
				IdleConnTimeout:     90 * time.Second,
				ForceAttemptHTTP2:   true,
			},
		},
	}
}

// Paths for each wire format.
const (
	PathChatCompletions = "/api/llm/o/v1/chat/completions"
	PathMessages        = "/api/llm/a/v1/messages"
	PathResponses       = "/api/llm/o/v1/responses"
	PathGoogle          = "/api/llm/g/v1/generate"
	PathBillingLimits   = "/api/billing/limits"
)

// Endpoint returns the upstream path for a model family.
func Endpoint(f config.ModelFamily) string {
	switch f {
	case config.FamilyMessages:
		return PathMessages
	case config.FamilyResponses:
		return PathResponses
	case config.FamilyGoogle:
		return PathGoogle
	default:
		return PathChatCompletions
	}
}

// Request is a prepared upstream call.
type Request struct {
	Path   string
	APIKey string
	Body   []byte
	Stream bool
	// SessionID identifies the upstream conversation for quota accounting.
	SessionID string
}

// buildHeaders assembles the minimal header set the gateway requires.
//
// The user agent and client version come from the embedded manifest rather than
// being written here, so refreshing the catalog after a CLI release also moves
// the version this proxy claims to be.
func (c *Client) buildHeaders(req *http.Request, apiKey, provider, sessionID string) {
	h := req.Header
	h.Set("authorization", "Bearer "+apiKey)
	h.Set("content-type", "application/json")
	h.Set("accept", "application/json")
	h.Set("user-agent", config.ManifestUserAgent())
	h.Set("x-api-provider", provider)
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	h.Set("x-session-id", sessionID)
	h.Set("x-assistant-message-id", uuid.NewString())
	h.Set("x-factory-client", "cli")
	h.Set("x-client-version", config.ManifestVersion())
}

// Do sends an upstream request and returns the raw response. The caller owns
// the response body.
func (c *Client) Do(ctx context.Context, r Request, provider string) (*http.Response, error) {
	url := c.BaseURL + r.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(r.Body))
	if err != nil {
		return nil, err
	}
	c.buildHeaders(req, r.APIKey, provider, r.SessionID)
	return c.HTTP.Do(req)
}

// Limits fetches the current quota limits for a key.
func (c *Client) Limits(ctx context.Context, apiKey string) (*Limits, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+PathBillingLimits, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+apiKey)
	req.Header.Set("user-agent", config.ManifestUserAgent())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("limits: HTTP %d: %s", resp.StatusCode, truncate(body, 300))
	}
	var l Limits
	if err := json.Unmarshal(body, &l); err != nil {
		return nil, fmt.Errorf("limits: decode: %w", err)
	}
	return &l, nil
}

// VerifyKey checks that a key is valid by calling the whoami endpoint.
func (c *Client) VerifyKey(ctx context.Context, apiKey string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/cli/whoami", nil)
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "Bearer "+apiKey)
	req.Header.Set("user-agent", config.ManifestUserAgent())
	req.Header.Set("x-factory-whoami-extended", "true")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}

// Limits mirrors GET /api/billing/limits.
type Limits struct {
	Limits struct {
		Standard PoolLimits `json:"standard"`
		Core     PoolLimits `json:"core"`
	} `json:"limits"`
	OveragePreference      string `json:"overagePreference"`
	ExtraUsageBalanceCents int    `json:"extraUsageBalanceCents"`
	ExtraUsageAllowed      bool   `json:"extraUsageAllowed"`
}

// PoolLimits groups the three rolling windows.
type PoolLimits struct {
	FiveHour Window `json:"fiveHour"`
	Weekly   Window `json:"weekly"`
	Monthly  Window `json:"monthly"`
}

// Window is one rolling rate-limit window.
type Window struct {
	UsedPercent      int     `json:"usedPercent"`
	WindowEnd        *string `json:"windowEnd"`
	SecondsRemaining *int    `json:"secondsRemaining"`
}

// MaxUsedPercent returns the highest usage across a pool's windows.
func (p PoolLimits) MaxUsedPercent() int {
	m := p.FiveHour.UsedPercent
	if p.Weekly.UsedPercent > m {
		m = p.Weekly.UsedPercent
	}
	if p.Monthly.UsedPercent > m {
		m = p.Monthly.UsedPercent
	}
	return m
}

// ParseSSE reads an SSE stream and calls fn for each `data:` payload.
// fn receives the raw payload bytes (without the "data: " prefix).
func ParseSSE(r io.Reader, fn func(data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 {
			continue
		}
		if err := fn(payload); err != nil {
			return err
		}
	}
	return sc.Err()
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
