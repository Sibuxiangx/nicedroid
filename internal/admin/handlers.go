package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Sibuxiangx/nicedroid/internal/config"
	"github.com/Sibuxiangx/nicedroid/internal/store"
	"github.com/Sibuxiangx/nicedroid/internal/upstream"
)

// ---- dashboard ----

// Stats returns aggregated traffic for the dashboard.
func (a *Admin) Stats(c echo.Context) error {
	hours := 24
	if h := c.QueryParam("hours"); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n > 0 && n <= 24*30 {
			hours = n
		}
	}
	st, err := a.Store.GetStats(hours)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, st)
}

// Quotas returns the router's quota snapshot.
func (a *Admin) Quotas(c echo.Context) error {
	return c.JSON(http.StatusOK, a.Router.Snapshot())
}

// RefreshQuotas triggers an immediate quota poll.
func (a *Admin) RefreshQuotas(c echo.Context) error {
	a.Router.RefreshQuotas(c.Request().Context())
	return c.JSON(http.StatusOK, a.Router.Snapshot())
}

// ---- upstream keys ----

// ListUpstreamKeys returns the key pool with masked values.
func (a *Admin) ListUpstreamKeys(c echo.Context) error {
	keys, err := a.Store.ListUpstreamKeys()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]any{
			"id":           k.ID,
			"name":         k.Name,
			"key_masked":   maskKey(k.Key),
			"enabled":      k.Enabled,
			"weight":       k.Weight,
			"last_status":  k.LastStatus,
			"last_checked": k.LastChecked,
			"created_at":   k.CreatedAt,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"keys": out})
}

// CreateUpstreamKey adds a Factory API key to the pool.
func (a *Admin) CreateUpstreamKey(c echo.Context) error {
	var body struct {
		Name   string `json:"name"`
		Key    string `json:"key"`
		Weight int    `json:"weight"`
	}
	if err := c.Bind(&body); err != nil || strings.TrimSpace(body.Key) == "" {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "key is required"})
	}
	id, err := a.Store.AddUpstreamKey(strings.TrimSpace(body.Name), strings.TrimSpace(body.Key), body.Weight)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	_ = a.Router.Reload()
	a.Router.RefreshQuotas(c.Request().Context())
	return c.JSON(http.StatusOK, map[string]any{"id": id})
}

// PatchUpstreamKey updates name/enabled/weight.
func (a *Admin) PatchUpstreamKey(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "invalid id"})
	}
	var body struct {
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
		Weight  *int    `json:"weight"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "invalid body"})
	}
	if err := a.Store.UpdateUpstreamKey(id, body.Name, body.Enabled, body.Weight); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	_ = a.Router.Reload()
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// DeleteUpstreamKey removes a key.
func (a *Admin) DeleteUpstreamKey(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "invalid id"})
	}
	if err := a.Store.DeleteUpstreamKey(id); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	_ = a.Router.Reload()
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// VerifyUpstreamKey runs a whoami check and records the result.
func (a *Admin) VerifyUpstreamKey(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "invalid id"})
	}
	keys, err := a.Store.ListUpstreamKeys()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	for _, k := range keys {
		if k.ID == id {
			ctx, cancel := timeoutCtx(c, 20*time.Second)
			defer cancel()
			if err := a.Client.VerifyKey(ctx, k.Key); err != nil {
				_ = a.Store.SetUpstreamKeyStatus(id, "invalid: "+err.Error())
				return c.JSON(http.StatusOK, map[string]any{"ok": false, "status": "invalid: " + err.Error()})
			}
			_ = a.Store.SetUpstreamKeyStatus(id, "ok")
			return c.JSON(http.StatusOK, map[string]any{"ok": true, "status": "ok"})
		}
	}
	return c.JSON(http.StatusNotFound, map[string]any{"error": "key not found"})
}

// ---- client keys ----

// ListClientKeys returns downstream keys.
func (a *Admin) ListClientKeys(c echo.Context) error {
	keys, err := a.Store.ListClientKeys()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"keys": keys})
}

// CreateClientKey issues a new downstream key.
func (a *Admin) CreateClientKey(c echo.Context) error {
	var body struct {
		Name        string `json:"name"`
		RPMLimit    int    `json:"rpm_limit"`
		DailyTokens int    `json:"daily_tokens"`
		Models      string `json:"models"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "invalid body"})
	}
	key, err := a.Store.CreateClientKey(body.Name, body.RPMLimit, body.DailyTokens, body.Models)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, key)
}

// PatchClientKey updates a downstream key.
func (a *Admin) PatchClientKey(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "invalid id"})
	}
	var body struct {
		Name        *string `json:"name"`
		Enabled     *bool   `json:"enabled"`
		RPMLimit    *int    `json:"rpm_limit"`
		DailyTokens *int    `json:"daily_tokens"`
		Models      *string `json:"models"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "invalid body"})
	}
	if err := a.Store.UpdateClientKey(id, body.Name, body.Enabled, body.RPMLimit, body.DailyTokens, body.Models); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// DeleteClientKey removes a downstream key.
func (a *Admin) DeleteClientKey(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "invalid id"})
	}
	if err := a.Store.DeleteClientKey(id); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// ---- logs ----

// Logs lists recent request logs.
func (a *Admin) Logs(c echo.Context) error {
	f := store.LogFilter{}
	if v := c.QueryParam("limit"); v != "" {
		f.Limit = atoi(v)
	}
	if v := c.QueryParam("offset"); v != "" {
		f.Offset = atoi(v)
	}
	f.Model = c.QueryParam("model")
	f.Client = c.QueryParam("client")
	f.ErrorsOnly = c.QueryParam("errors_only") == "true"
	if v := c.QueryParam("hours"); v != "" {
		if n := atoi(v); n > 0 {
			t := time.Now().Add(-time.Duration(n) * time.Hour)
			f.Since = &t
		}
	}
	logs, total, err := a.Store.ListLogs(f)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"logs": logs, "total": total})
}

// ---- settings ----

// GetSettings returns editable settings.
func (a *Admin) GetSettings(c echo.Context) error {
	systemPrompt, _ := a.Store.GetSetting("system_prompt", a.Cfg.DroidSystemPrompt)
	return c.JSON(http.StatusOK, map[string]any{
		"system_prompt": systemPrompt,
		"upstream":      a.Cfg.UpstreamBaseURL,
	})
}

// PutSettings updates editable settings.
func (a *Admin) PutSettings(c echo.Context) error {
	var body struct {
		SystemPrompt string `json:"system_prompt"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "invalid body"})
	}
	if strings.TrimSpace(body.SystemPrompt) == "" {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "system_prompt cannot be empty"})
	}
	if err := a.Store.SetSetting("system_prompt", body.SystemPrompt); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// ---- playground ----

// Playground runs a one-shot non-streaming completion so operators can verify
// a model end to end.
func (a *Admin) Playground(c echo.Context) error {
	var body struct {
		Model   string `json:"model"`
		Message string `json:"message"`
	}
	if err := c.Bind(&body); err != nil || body.Model == "" || body.Message == "" {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "model and message are required"})
	}
	m, ok := config.LookupModel(body.Model)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]any{"error": "model not found"})
	}
	if m.Passthrough {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "passthrough models cannot be tested here"})
	}
	key, ok := a.Router.NextKey(m.Tier)
	if !ok {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"error": "no upstream key configured"})
	}
	creq := &upstream.ChatRequest{
		Model: m.ID,
		Messages: []upstream.ChatMessage{
			{Role: "user", Content: json.RawMessage(strconv.Quote(body.Message))},
		},
	}
	prompt, _ := a.Store.GetSetting("system_prompt", a.Cfg.DroidSystemPrompt)
	var upBody map[string]any
	switch m.Family {
	case config.FamilyMessages:
		upBody = upstream.ToMessages(creq, prompt, 4096)
	case config.FamilyResponses:
		upBody = upstream.ToResponses(creq, prompt)
	default:
		upBody = upstream.ToChatCompletions(creq, prompt)
	}
	raw, err := json.Marshal(upBody)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	ctx, cancel := timeoutCtx(c, 120*time.Second)
	defer cancel()
	resp, err := a.Client.Do(ctx, upstream.Request{
		Path:   upstream.Endpoint(m.Family),
		APIKey: key.Key,
		Body:   raw,
	}, a.currentProvider(m))
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]any{"error": "upstream error: " + err.Error()})
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return c.JSON(resp.StatusCode, map[string]any{"error": string(payload)})
	}
	var up map[string]any
	if err := json.Unmarshal(payload, &up); err != nil {
		return c.JSON(http.StatusBadGateway, map[string]any{"error": "invalid upstream payload"})
	}
	text := extractText(m.Family, up)
	return c.JSON(http.StatusOK, map[string]any{
		"model":     m.ID,
		"text":      text,
		"raw_usage": up["usage"],
	})
}

// currentProvider resolves the provider for a model: the admin override when
// set, otherwise the catalog default.
func (a *Admin) currentProvider(m config.Model) string {
	if v, err := a.Store.GetProviderOverride(m.ID); err == nil && v != "" {
		return v
	}
	return m.DefaultProvider()
}

func extractText(family config.ModelFamily, up map[string]any) string {
	switch family {
	case config.FamilyMessages:
		var out string
		if blocks, ok := up["content"].([]any); ok {
			for _, b := range blocks {
				if blk, ok := b.(map[string]any); ok && blk["type"] == "text" {
					if t, ok := blk["text"].(string); ok {
						out += t
					}
				}
			}
		}
		return out
	case config.FamilyResponses:
		var out string
		if items, ok := up["output"].([]any); ok {
			for _, it := range items {
				item, _ := it.(map[string]any)
				if item == nil || item["type"] != "message" {
					continue
				}
				if parts, ok := item["content"].([]any); ok {
					for _, p := range parts {
						if part, ok := p.(map[string]any); ok && part["type"] == "output_text" {
							if t, ok := part["text"].(string); ok {
								out += t
							}
						}
					}
				}
			}
		}
		return out
	default:
		if choices, ok := up["choices"].([]any); ok && len(choices) > 0 {
			if ch, ok := choices[0].(map[string]any); ok {
				if m, ok := ch["message"].(map[string]any); ok {
					if t, ok := m["content"].(string); ok {
						return t
					}
					if rc, ok := m["reasoning_content"].(string); ok {
						return rc
					}
				}
			}
		}
		return ""
	}
}

// ---- helpers ----

func maskKey(k string) string {
	if len(k) <= 12 {
		return "****"
	}
	return k[:10] + "****" + k[len(k)-4:]
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// timeoutCtx builds a bounded context for outbound calls.
func timeoutCtx(c echo.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request().Context(), d)
}
