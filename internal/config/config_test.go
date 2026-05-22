package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsAreValid(t *testing.T) {
	t.Parallel()
	if err := Validate(Default()); err != nil {
		t.Fatalf("Default() failed validation: %v", err)
	}
}

// TestDefaultCompressTypesIncludesCode pins the v1.4.40 flip:
// CodeCompressor became content-preserving (no body elision, no
// signature-only skeleton), so "code" entered the conservative default
// allow-list alongside "json" and "logs". Reverting this default is a
// product decision and must be explicit.
func TestDefaultCompressTypesIncludesCode(t *testing.T) {
	t.Parallel()
	got := Default().Compression.Conversation.CompressTypes
	want := map[string]bool{"json": true, "logs": true, "code": true}
	if len(got) != len(want) {
		t.Fatalf("CompressTypes: got %v, want %v", got, want)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("unexpected default compress_type: %q (want only %v)", name, want)
		}
		delete(want, name)
	}
	if len(want) > 0 {
		t.Errorf("missing default compress_types: %v", want)
	}
}

func TestLoadAppliesGlobalTOML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	body := `
[observer]
log_level = "debug"

[observer.watch]
max_file_size_mb = 200

[proxy]
port = 9999
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(LoadOptions{GlobalPath: p, Env: func(string) string { return "" }})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Observer.LogLevel != "debug" {
		t.Errorf("log_level: got %q, want %q", cfg.Observer.LogLevel, "debug")
	}
	if cfg.Observer.Watch.MaxFileSizeMB != 200 {
		t.Errorf("max_file_size_mb: got %d, want 200", cfg.Observer.Watch.MaxFileSizeMB)
	}
	if cfg.Proxy.Port != 9999 {
		t.Errorf("proxy.port: got %d, want 9999", cfg.Proxy.Port)
	}
	// Unrelated defaults preserved.
	if !cfg.Compression.Shell.Enabled {
		t.Error("compression.shell.enabled should default true")
	}
}

// TestEmptyEnabledAdaptersDisablesWatch pins the explicit-empty-list
// semantics. A user who writes `enabled_adapters = []` in
// config.toml expects the watcher to skip every adapter — not to
// silently fall through to Default()'s populated list.
// BurntSushi/toml correctly preserves the nil vs. non-nil-empty
// distinction (key absent → leaves Default() alone, key present
// with `[]` → overwrites with []string{}). Pair this with the
// registry.Detected nil-vs-empty fix in internal/adapter/registry.go.
func TestEmptyEnabledAdaptersDisablesWatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	body := `
[observer.watch]
enabled_adapters = []
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(LoadOptions{GlobalPath: p, Env: func(string) string { return "" }})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Observer.Watch.EnabledAdapters == nil {
		t.Fatal("EnabledAdapters is nil — expected non-nil empty slice (the registry distinguishes these)")
	}
	if len(cfg.Observer.Watch.EnabledAdapters) != 0 {
		t.Errorf("EnabledAdapters: got %v, want non-nil empty slice", cfg.Observer.Watch.EnabledAdapters)
	}
}

func TestProjectOverridesGlobal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gp := filepath.Join(dir, "global.toml")
	pp := filepath.Join(dir, "project.toml")
	if err := os.WriteFile(gp, []byte(`[observer]
log_level = "warn"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pp, []byte(`[observer]
log_level = "debug"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{GlobalPath: gp, ProjectPath: pp, Env: func(string) string { return "" }})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Observer.LogLevel != "debug" {
		t.Errorf("project should override global: got %q", cfg.Observer.LogLevel)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"OBSERVER_OBSERVER_LOG_LEVEL":               "warn",
		"OBSERVER_PROXY_PORT":                       "1234",
		"OBSERVER_COMPRESSION_CONVERSATION_ENABLED": "true",
		"OBSERVER_OBSERVER_WATCH_ENABLED_ADAPTERS":  "claude-code,codex",
	}
	cfg, err := Load(LoadOptions{GlobalPath: "", Env: func(k string) string { return env[k] }})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Observer.LogLevel != "warn" {
		t.Errorf("log_level: got %q", cfg.Observer.LogLevel)
	}
	if cfg.Proxy.Port != 1234 {
		t.Errorf("port: got %d", cfg.Proxy.Port)
	}
	if !cfg.Compression.Conversation.Enabled {
		t.Errorf("conversation.enabled not overridden")
	}
	if got, want := cfg.Observer.Watch.EnabledAdapters, []string{"claude-code", "codex"}; !equalSlice(got, want) {
		t.Errorf("enabled_adapters: got %v want %v", got, want)
	}
}

func TestValidateRejectsBadLogLevel(t *testing.T) {
	t.Parallel()
	c := Default()
	c.Observer.LogLevel = "trace"
	if err := Validate(c); err == nil {
		t.Fatal("expected error for log_level=trace")
	}
}

func TestValidateRejectsBadConversationMode(t *testing.T) {
	t.Parallel()
	c := Default()
	c.Compression.Conversation.Enabled = true
	c.Compression.Conversation.Mode = "sillystring"
	if err := Validate(c); err == nil {
		t.Fatal("expected error for conversation.mode=sillystring")
	}
}

func TestMissingGlobalFileIsNotAnError(t *testing.T) {
	t.Parallel()
	cfg, err := Load(LoadOptions{
		GlobalPath: filepath.Join(t.TempDir(), "does-not-exist.toml"),
		Env:        func(string) string { return "" },
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Observer.LogLevel != "info" {
		t.Errorf("expected default log_level, got %q", cfg.Observer.LogLevel)
	}
}

func TestExpandHome(t *testing.T) {
	t.Parallel()
	cfg, err := Load(LoadOptions{GlobalPath: "", Env: func(string) string { return "" }})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Observer.DBPath == "~/.observer/observer.db" {
		t.Errorf("DBPath not expanded: %s", cfg.Observer.DBPath)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
