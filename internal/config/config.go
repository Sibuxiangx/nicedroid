package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the process-wide static configuration, loaded from the environment
// with sane defaults. Dynamic settings (key pool, routing policy, etc.) live in
// the database and are managed through the admin UI.
type Config struct {
	Listen            string
	DBPath            string
	UpstreamBaseURL   string
	DroidSystemPrompt string
	AdminUser         string
	AdminPassword     string
	JWTSecret         string
	QuotaPollInterval time.Duration
	LogRetentionDays  int
	RequestTimeout    time.Duration
	// PromptCache injects Anthropic cache breakpoints. On by default: without
	// them every turn of an agent loop is billed as fresh input.
	PromptCache bool
}

// Getenv reads an env var with a default.
func Getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load builds Config from the environment.
func Load() Config {
	ret, err := strconv.Atoi(Getenv("NICEDROID_LOG_RETENTION_DAYS", "30"))
	if err != nil || ret <= 0 {
		ret = 30
	}
	poll, err := strconv.Atoi(Getenv("NICEDROID_QUOTA_POLL_SECONDS", "60"))
	if err != nil || poll <= 0 {
		poll = 60
	}
	timeout, err := strconv.Atoi(Getenv("NICEDROID_REQUEST_TIMEOUT_SECONDS", "600"))
	if err != nil || timeout <= 0 {
		timeout = 600
	}
	// Default on: the proxy exists to serve agent loops, and an uncached loop
	// pays full input price on every turn. NICEDROID_PROMPT_CACHE=off disables it.
	promptCache := !isOff(Getenv("NICEDROID_PROMPT_CACHE", "on"))
	return Config{
		Listen:            Getenv("NICEDROID_LISTEN", ":3000"),
		DBPath:            Getenv("NICEDROID_DB", "nicedroid.db"),
		UpstreamBaseURL:   Getenv("NICEDROID_UPSTREAM", "https://api.factory.ai"),
		DroidSystemPrompt: Getenv("NICEDROID_SYSTEM_PROMPT", DefaultDroidSystemPrompt),
		AdminUser:         Getenv("NICEDROID_ADMIN_USER", "admin"),
		AdminPassword:     Getenv("NICEDROID_ADMIN_PASSWORD", ""),
		JWTSecret:         Getenv("NICEDROID_JWT_SECRET", ""),
		QuotaPollInterval: time.Duration(poll) * time.Second,
		LogRetentionDays:  ret,
		RequestTimeout:    time.Duration(timeout) * time.Second,
		PromptCache:       promptCache,
	}
}

// isOff reports whether a boolean-ish env value means "disabled".
func isOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "false", "no", "0", "disabled":
		return true
	}
	return false
}

// DefaultDroidSystemPrompt is the gateway's required content gate: every
// upstream LLM request must carry a leading system message with this exact
// phrase, or the gateway answers 403 Forbidden.
const DefaultDroidSystemPrompt = "You are Droid, an AI software engineering agent built by Factory."
