package api

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// AuthMiddleware enforces client-key authentication when at least one enabled
// client key exists. With no keys configured the proxy runs in open mode, so
// a fresh install works immediately.
func (h *Handler) AuthMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		keys, err := h.Store.ListClientKeys()
		if err != nil {
			return openAIError(c, http.StatusInternalServerError, "server_error", "key store unavailable")
		}
		requireAuth := false
		for _, k := range keys {
			if k.Enabled {
				requireAuth = true
				break
			}
		}
		if !requireAuth {
			return next(c)
		}
		token := bearer(c)
		if token == "" {
			token = c.Request().Header.Get("x-api-key")
		}
		if token == "" {
			return openAIError(c, http.StatusUnauthorized, "authentication_error", "missing API key")
		}
		k, enabled, err := h.Store.LookupClientKey(token)
		if err != nil || !enabled {
			return openAIError(c, http.StatusUnauthorized, "authentication_error", "invalid API key")
		}
		c.Set("client_key_name", k.Name)
		return next(c)
	}
}
