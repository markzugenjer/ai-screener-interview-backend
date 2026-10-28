// Package config loads all runtime settings from environment variables.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env         string // "development" or "production"
	HTTPAddr    string // e.g. ":8080"
	PublicURL   string // URL of the frontend, e.g. http://localhost:3000
	Store       string // "postgres" or "memory"
	DatabaseURL string

	// Security
	EncryptionKey  []byte        // 32 bytes, AES-256-GCM for personal data at rest
	CookieSecure   bool          // send cookies only over HTTPS
	SessionTTL     time.Duration // how long a login lasts
	AllowedOrigins []string      // origins allowed to send state-changing requests
	TrustProxy     bool          // trust X-Forwarded-For (only behind your own proxy)
	InternalSecret string        // shared secret for the voice agent -> API calls
	MaxResumeBytes int64

	// Storage for encrypted files (resumes, audio)
	StorageDir string

	// LiveKit
	LiveKitURL       string
	LiveKitAPIKey    string
	LiveKitAPISecret string
	LiveKitAgentName string

	// Anthropic (scoring)
	AnthropicAPIKey  string
	AnthropicModel   string
	AnthropicBaseURL string

	// Worker
	RunWorkerInAPI   bool // run the scoring worker inside the API process (handy for local dev)
	FeedbackLanguage string
}

func Load() (*Config, error) {
	c := &Config{
		Env:              get("APP_ENV", "development"),
		HTTPAddr:         get("HTTP_ADDR", ":8080"),
		PublicURL:        strings.TrimRight(get("PUBLIC_URL", "http://localhost:3000"), "/"),
		Store:            get("STORE", "postgres"),
		DatabaseURL:      get("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/screener?sslmode=disable"),
		CookieSecure:     getBool("COOKIE_SECURE", false),
		SessionTTL:       getDuration("SESSION_TTL", 7*24*time.Hour),
		TrustProxy:       getBool("TRUST_PROXY", true),
		InternalSecret:   os.Getenv("INTERNAL_API_SECRET"),
		MaxResumeBytes:   int64(getInt("MAX_RESUME_MB", 5)) << 20,
		StorageDir:       get("STORAGE_DIR", "./data/files"),
		LiveKitURL:       os.Getenv("LIVEKIT_URL"),
		LiveKitAPIKey:    os.Getenv("LIVEKIT_API_KEY"),
		LiveKitAPISecret: os.Getenv("LIVEKIT_API_SECRET"),
		LiveKitAgentName: get("LIVEKIT_AGENT_NAME", "ai-screener"),
		AnthropicAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),
		AnthropicModel:   get("ANTHROPIC_MODEL", "claude-sonnet-5-5"),
		AnthropicBaseURL: get("ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
		RunWorkerInAPI:   getBool("RUN_WORKER_IN_API", false),
		FeedbackLanguage: get("FEEDBACK_LANGUAGE", "English"),
	}

	origins := get("ALLOWED_ORIGINS", c.PublicURL)
	for _, o := range strings.Split(origins, ",") {
		if o = strings.TrimSpace(strings.TrimRight(o, "/")); o != "" {
			c.AllowedOrigins = append(c.AllowedOrigins, o)
		}
	}

	key := os.Getenv("APP_ENCRYPTION_KEY")
	if key == "" {
		return nil, errors.New("APP_ENCRYPTION_KEY is required (32 random bytes, base64). Generate one with: openssl rand -base64 32")
	}
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("APP_ENCRYPTION_KEY must be exactly 32 bytes encoded as base64")
	}
	c.EncryptionKey = raw

	if len(c.InternalSecret) < 32 {
		return nil, errors.New("INTERNAL_API_SECRET is required and must be at least 32 characters")
	}
	if c.Store != "postgres" && c.Store != "memory" {
		return nil, fmt.Errorf("STORE must be postgres or memory, got %q", c.Store)
	}
	if c.IsProduction() && !c.CookieSecure {
		return nil, errors.New("COOKIE_SECURE must be true in production (HTTPS only)")
	}
	return c, nil
}

func (c *Config) IsProduction() bool { return c.Env == "production" }

func (c *Config) LiveKitConfigured() bool {
	return c.LiveKitURL != "" && c.LiveKitAPIKey != "" && c.LiveKitAPISecret != ""
}

func get(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func getBool(k string, def bool) bool {
	v, err := strconv.ParseBool(os.Getenv(k))
	if err != nil {
		return def
	}
	return v
}

func getInt(k string, def int) int {
	v, err := strconv.Atoi(os.Getenv(k))
	if err != nil {
		return def
	}
	return v
}

func getDuration(k string, def time.Duration) time.Duration {
	v, err := time.ParseDuration(os.Getenv(k))
	if err != nil {
		return def
	}
	return v
}
