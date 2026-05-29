package config

import (
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Host          string
	Port          string
	DBPath        string
	ModelFile     string
	SessionSecret string
	StaticDir     string
	TemplateDir   string
}

func Load() Config {
	cwd, _ := os.Getwd()

	cfg := Config{
		Host:          envOr("LEOSTUDIO_HOST", "127.0.0.1"),
		Port:          envOr("LEOSTUDIO_PORT", "8000"),
		DBPath:        envOr("LEOSTUDIO_DB", filepath.Join(cwd, "data", "app.db")),
		ModelFile:     envOr("LEOSTUDIO_MODEL_FILE", filepath.Join(cwd, "model_id.txt")),
		SessionSecret: envOr("LEOSTUDIO_SESSION_SECRET", "change-this-session-secret"),
		StaticDir:     envOr("LEOSTUDIO_STATIC_DIR", filepath.Join(cwd, "internal", "server", "static")),
		TemplateDir:   envOr("LEOSTUDIO_TEMPLATE_DIR", filepath.Join(cwd, "internal", "server", "templates")),
	}
	return cfg
}

func (c Config) Addr() string {
	return fmt.Sprintf("%s:%s", c.Host, c.Port)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
