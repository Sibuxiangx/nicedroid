package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/Sibuxiangx/nicedroid/internal/admin"
	"github.com/Sibuxiangx/nicedroid/internal/api"
	"github.com/Sibuxiangx/nicedroid/internal/config"
	"github.com/Sibuxiangx/nicedroid/internal/router"
	"github.com/Sibuxiangx/nicedroid/internal/store"
	"github.com/Sibuxiangx/nicedroid/internal/upstream"
	"github.com/Sibuxiangx/nicedroid/web"
)

func main() {
	cfg := config.Load()

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[nicedroid] ")

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer st.Close()

	client := upstream.New(cfg.UpstreamBaseURL)
	rt := router.New(client, st, cfg)

	if err := rt.Reload(); err != nil {
		log.Fatalf("load upstream keys: %v", err)
	}

	adminAPI, err := admin.New(cfg, st, rt, client)
	if err != nil {
		log.Fatalf("init admin: %v", err)
	}
	admin.SetPasswordLogger(func(pw string) {
		log.Printf("=====================================================")
		log.Printf("  FIRST BOOT: generated admin password: %s", pw)
		log.Printf("  user: %s   (change it in nicedroid.db settings)", cfg.AdminUser)
		log.Printf("=====================================================")
	})

	proxy := api.NewHandler(cfg, st, client, rt)

	// background jobs
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.RunRefreshLoop(ctx)
	go func() {
		t := time.NewTicker(12 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = st.CleanupLogs(cfg.LogRetentionDays)
			}
		}
	}()

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowHeaders: []string{"*"},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
	}))

	// Proxy API (client-key auth applies when keys exist).
	g := e.Group("")
	g.Use(proxy.AuthMiddleware)
	g.GET("/v1/models", proxy.ListModels)
	g.POST("/v1/chat/completions", proxy.ChatCompletions)
	g.POST("/v1/messages", proxy.Messages)
	g.POST("/v1/messages/count_tokens", proxy.CountTokens)
	g.POST("/v1/responses", proxy.Responses)
	g.POST("/v1/generate", proxy.GeneratePassthrough)
	e.GET("/healthz", proxy.Health)

	// Admin API.
	adminAPI.Register(e.Group("/admin/api"))

	// Admin UI (embedded SPA).
	e.GET("/", echo.WrapHandler(web.Handler()))
	e.GET("/admin", echo.WrapHandler(web.Handler()))
	e.GET("/admin/*", echo.WrapHandler(web.Handler()))
	e.GET("/assets/*", echo.WrapHandler(web.Handler()))
	e.GET("/favicon.ico", echo.WrapHandler(web.Handler()))

	go func() {
		log.Printf("listening on %s (upstream: %s, db: %s)", cfg.Listen, cfg.UpstreamBaseURL, cfg.DBPath)
		if err := e.Start(cfg.Listen); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = e.Shutdown(shutdownCtx)
}
