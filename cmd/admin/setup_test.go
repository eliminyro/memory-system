package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func runSetup(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newSetupCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestSetupEmitsValidConfig(t *testing.T) {
	// Trailing slash on the URL must be trimmed so the endpoint is <base>/mcp.
	out, err := runSetup(t, "--url", "https://mem.example.org/", "--token", "mmcp_secret", "--name", "memory")
	if err != nil {
		t.Fatal(err)
	}
	var cfg mcpConfig
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	entry, ok := cfg.MCPServers["memory"]
	if !ok {
		t.Fatalf("missing 'memory' server entry: %s", out)
	}
	if entry.Type != "http" {
		t.Errorf("type = %q, want http", entry.Type)
	}
	if entry.URL != "https://mem.example.org/mcp" {
		t.Errorf("url = %q, want https://mem.example.org/mcp", entry.URL)
	}
	if got := entry.Headers["Authorization"]; got != "Bearer mmcp_secret" {
		t.Errorf("Authorization = %q, want Bearer mmcp_secret", got)
	}
}

func TestSetupCustomName(t *testing.T) {
	out, err := runSetup(t, "--url", "http://localhost:8080", "--token", "t", "--name", "work-memory")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"work-memory"`) {
		t.Errorf("expected custom server name in output: %s", out)
	}
}

func TestSetupFromEnv(t *testing.T) {
	t.Setenv("MEMORY_URL", "https://mem.example.org")
	t.Setenv("MEMORY_TOKEN", "envtoken")
	out, err := runSetup(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Bearer envtoken") || !strings.Contains(out, "https://mem.example.org/mcp") {
		t.Errorf("env-driven config missing expected values: %s", out)
	}
}

func TestSetupRequiresURLAndToken(t *testing.T) {
	t.Setenv("MEMORY_URL", "")
	t.Setenv("MEMORY_TOKEN", "")
	if _, err := runSetup(t, "--url", "https://mem.example.org"); err == nil {
		t.Error("expected error when token missing")
	}
	if _, err := runSetup(t, "--token", "t"); err == nil {
		t.Error("expected error when url missing")
	}
}
