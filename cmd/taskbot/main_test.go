package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigPreflightDoesNotStartBotOrCreateState(t *testing.T) {
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	os.Args = []string{"taskbot", "check-config"}
	directory := t.TempDir()
	config := filepath.Join(directory, "config.json")
	state := filepath.Join(directory, "state.json")
	t.Setenv("CONFIG_FILE", config)
	t.Setenv("STATE_FILE", state)
	t.Setenv("WORKER_TOKEN", strings.Repeat("x", 32))
	// No GitHub or Telegram credentials: preflight must not initialize clients.
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	if err := os.WriteFile(config, []byte(`{"-1001":{"users":[7],"repositories":["example/project"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("preflight created state: %v", err)
	}
	if err := os.WriteFile(config, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil {
		t.Fatal("invalid config accepted")
	}
}

func TestProxyPreflightNeedsNoApplicationCredentials(t *testing.T) {
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	os.Args = []string{"taskbot", "check-proxy-config"}
	t.Setenv("WORKER_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	file := filepath.Join(t.TempDir(), "proxy.json")
	if err := os.WriteFile(file, []byte(`{"url":"http://192.0.2.10:8080","username":"synthetic","password":"not-real"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROXY_CONFIG_FILE", file)
	if err := run(); err != nil {
		t.Fatal(err)
	}
}
