package api

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// AuthMiddleware enforces client-key authentication on the proxy endpoints.
//
// This is default-deny. With no enabled client key the proxy refuses requests
// instead of serving them openly: these endpoints spend the operator's upstream
// quota, so an unconfigured deployment has to fail closed rather than become an
// open relay for anyone who finds the host. The cost is that a fresh install is
// unusable until a key is created in the admin console, which has its own
// credentials and is unaffected by this middleware.
func (h *Handler) AuthMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		keys, err := h.Store.ListClientKeys()
		if err != nil {
			return openAIError(c, http.StatusInternalServerError, "server_error", "key store unavailable")
		}
		anyEnabled := false
		for _, k := range keys {
			if k.Enabled {
				anyEnabled = true
				break
			}
		}
		if !anyEnabled {
			return openAIError(c, http.StatusServiceUnavailable, "server_error",
				"no client key configured: create one in the admin console, then send it as \"Authorization: Bearer <key>\"")
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
