// Package config reads settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // so IANA zone names resolve in minimal containers
)

type TokenEntry struct {
	Device string
	Token  string
}

type Config struct {
	ListenAddr      string
	DatabaseURL     string
	Timezone        string
	Tokens          []TokenEntry
	IngestPerMinute int
	StaticDir       string // built frontend to serve at / (optional)
}

// Load reads DATABASE_URL (required), LISTEN_ADDR (default :8080), TIMEZONE (default UTC)
// STATIC_DIR (built frontend to serve; optional), INGEST_RATE_PER_MIN (per-device POST rate, default 30, 0 = unlimited) and API_TOKENS ("device=token,device2=token2"; empty disables the ingest endpoints).
func Load() (Config, error) {
	c := Config{
		ListenAddr:      getenv("LISTEN_ADDR", ":8080"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		Timezone:        getenv("TIMEZONE", "UTC"),
		Tokens:          ParseTokens(os.Getenv("API_TOKENS")),
		IngestPerMinute: 30,
		StaticDir:       os.Getenv("STATIC_DIR"),
	}
	if v := os.Getenv("INGEST_RATE_PER_MIN"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return c, fmt.Errorf("INGEST_RATE_PER_MIN must be a non-negative integer, got %q", v)
		}
		c.IngestPerMinute = n
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required (e.g. postgres://user:pass@host:5432/speedtestdb?sslmode=disable)")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return c, fmt.Errorf("TIMEZONE %q is not a valid IANA zone: %w", c.Timezone, err)
	}
	return c, nil
}

// ParseTokens parses "device=token,device2=token2". Malformed or empty entries are skipped.
func ParseTokens(s string) []TokenEntry {
	var out []TokenEntry
	for _, pair := range strings.Split(s, ",") {
		device, token, ok := strings.Cut(strings.TrimSpace(pair), "=")
		device, token = strings.TrimSpace(device), strings.TrimSpace(token)
		if ok && device != "" && token != "" {
			out = append(out, TokenEntry{Device: device, Token: token})
		}
	}
	return out
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
