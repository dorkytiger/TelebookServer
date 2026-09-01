package config

import (
	"strings"
	"testing"
)

func TestLoadMissingRequired(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("SYNC_SECRET", "")
	t.Setenv("JWT_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when required env missing")
	}
}

func TestLoadOK(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/telebook")
	t.Setenv("SYNC_SECRET", "secret")
	t.Setenv("JWT_SECRET", "jwt")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("expected default :8080, got %s", cfg.HTTPAddr)
	}
	if cfg.Version == "" || !strings.Contains(cfg.Version, ".") {
		t.Fatalf("bad version: %s", cfg.Version)
	}
}
