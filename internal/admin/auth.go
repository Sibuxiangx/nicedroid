package admin

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"

	"github.com/Sibuxiangx/nicedroid/internal/config"
	"github.com/Sibuxiangx/nicedroid/internal/router"
	"github.com/Sibuxiangx/nicedroid/internal/store"
	"github.com/Sibuxiangx/nicedroid/internal/upstream"
)

// Admin serves the management API for the admin UI.
type Admin struct {
	Cfg    config.Config
	Store  *store.Store
	Router *router.Router
	Client *upstream.Client
	secret []byte
}

// New builds the admin API and initializes credentials/JWT secret.
func New(cfg config.Config, st *store.Store, rt *router.Router, client *upstream.Client) (*Admin, error) {
	a := &Admin{Cfg: cfg, Store: st, Router: rt, Client: client}
	if err := a.initCredentials(); err != nil {
		return nil, err
	}
	return a, nil
}

// initCredentials ensures admin credentials and the JWT secret exist.
func (a *Admin) initCredentials() error {
	username, _ := a.Store.GetSetting("admin_user", "")
	if username == "" {
		username = a.Cfg.AdminUser
		if username == "" {
			username = "admin"
		}
		if err := a.Store.SetSetting("admin_user", username); err != nil {
			return err
		}
	}
	hash, _ := a.Store.GetSetting("admin_pass_hash", "")
	if hash == "" {
		password := a.Cfg.AdminPassword
		if password == "" {
			b := make([]byte, 12)
			rand.Read(b)
			password = hex.EncodeToString(b)[:16]
			logGeneratedPassword(password)
		}
		h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if err := a.Store.SetSetting("admin_pass_hash", string(h)); err != nil {
			return err
		}
	}
	secret, _ := a.Store.GetSetting("jwt_secret", "")
	if secret == "" {
		secret = a.Cfg.JWTSecret
		if secret == "" {
			b := make([]byte, 32)
			rand.Read(b)
			secret = hex.EncodeToString(b)
		}
		if err := a.Store.SetSetting("jwt_secret", secret); err != nil {
			return err
		}
	}
	a.secret = []byte(secret)
	return nil
}

// logGeneratedPassword is overridden in main for structured logging.
var logGeneratedPassword = func(password string) {}

// SetPasswordLogger wires the first-boot password log line.
func SetPasswordLogger(fn func(string)) {
	if fn != nil {
		logGeneratedPassword = fn
	}
}

// Register mounts the admin routes. protected wraps handlers with auth.
func (a *Admin) Register(g *echo.Group) {
	g.POST("/login", a.Login)
	g.POST("/logout", a.Logout)

	auth := g.Group("", a.RequireAuth)
	auth.GET("/session", a.Session)
	auth.GET("/stats", a.Stats)
	auth.GET("/quotas", a.Quotas)
	auth.POST("/quotas/refresh", a.RefreshQuotas)
	auth.GET("/upstream-keys", a.ListUpstreamKeys)
	auth.POST("/upstream-keys", a.CreateUpstreamKey)
	auth.PATCH("/upstream-keys/:id", a.PatchUpstreamKey)
	auth.DELETE("/upstream-keys/:id", a.DeleteUpstreamKey)
	auth.POST("/upstream-keys/:id/verify", a.VerifyUpstreamKey)
	auth.GET("/client-keys", a.ListClientKeys)
	auth.POST("/client-keys", a.CreateClientKey)
	auth.PATCH("/client-keys/:id", a.PatchClientKey)
	auth.DELETE("/client-keys/:id", a.DeleteClientKey)
	auth.GET("/models", a.ListModels)
	auth.PUT("/models/:id/provider", a.SetModelProvider)
	auth.POST("/models/:id/benchmark", a.BenchmarkProviders)
	auth.GET("/logs", a.Logs)
	auth.GET("/settings", a.GetSettings)
	auth.PUT("/settings", a.PutSettings)
	auth.POST("/playground", a.Playground)
}

// RequireAuth validates the JWT from the Authorization header or cookie.
func (a *Admin) RequireAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		tokenStr := ""
		h := c.Request().Header.Get("Authorization")
		if len(h) > 7 && h[:7] == "Bearer " {
			tokenStr = h[7:]
		}
		if tokenStr == "" {
			if ck, err := c.Cookie("nicedroid_token"); err == nil {
				tokenStr = ck.Value
			}
		}
		if tokenStr == "" {
			return c.JSON(http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		}
		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return a.secret, nil
		})
		if err != nil || !token.Valid {
			return c.JSON(http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		}
		claims, _ := token.Claims.(jwt.MapClaims)
		c.Set("admin_user", claims["sub"])
		return next(c)
	}
}

// Login authenticates the admin user and issues a JWT cookie.
func (a *Admin) Login(c echo.Context) error {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "invalid body"})
	}
	username, _ := a.Store.GetSetting("admin_user", "admin")
	hash, _ := a.Store.GetSetting("admin_pass_hash", "")
	if body.Username != username || bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)) != nil {
		return c.JSON(http.StatusUnauthorized, map[string]any{"error": "invalid credentials"})
	}
	exp := time.Now().Add(7 * 24 * time.Hour)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": username,
		"exp": exp.Unix(),
	})
	signed, err := token.SignedString(a.secret)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "signing failed"})
	}
	c.SetCookie(&http.Cookie{
		Name:     "nicedroid_token",
		Value:    signed,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return c.JSON(http.StatusOK, map[string]any{"token": signed, "username": username})
}

// Logout clears the auth cookie.
func (a *Admin) Logout(c echo.Context) error {
	c.SetCookie(&http.Cookie{
		Name:     "nicedroid_token",
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
	})
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// Session returns the current admin identity.
func (a *Admin) Session(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{"username": c.Get("admin_user")})
}
