package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupRegistry(t *testing.T) *Registry {
	t.Helper()
	return setupRegistryWithConfig(t, "")
}

func setupRegistryWithConfig(t *testing.T, configPath string) *Registry {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(Options{
		BinaryPath:    "/opt/observer/bin/observer",
		HomeDir:       home,
		ChecksumsPath: filepath.Join(home, ".observer", "hook_checksums.json"),
		ConfigPath:    configPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRegisterClaudeCodeFreshInstall(t *testing.T) {
	t.Parallel()
	r := setupRegistry(t)
	res := r.Register("claude-code")
	if res.Error != nil {
		t.Fatalf("Register: %v", res.Error)
	}
	if len(res.HooksAdded) != len(claudeCodeEvents) {
		t.Errorf("added %d want %d", len(res.HooksAdded), len(claudeCodeEvents))
	}

	body, err := os.ReadFile(res.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, body)
	}
	hooksBlock, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks block missing")
	}
	for _, event := range claudeCodeEvents {
		groups, ok := hooksBlock[event].([]any)
		if !ok || len(groups) == 0 {
			t.Errorf("event %s missing", event)
		}
	}

	// Checksum file should be written.
	csPath := filepath.Join(r.opts.HomeDir, ".observer", "hook_checksums.json")
	if _, err := os.Stat(csPath); err != nil {
		t.Errorf("checksum file not created: %v", err)
	}
}

func TestRegisterClaudeCodeIdempotent(t *testing.T) {
	t.Parallel()
	r := setupRegistry(t)
	if res := r.Register("claude-code"); res.Error != nil {
		t.Fatalf("first: %v", res.Error)
	}
	res := r.Register("claude-code")
	if res.Error != nil {
		t.Fatalf("second: %v", res.Error)
	}
	if len(res.HooksAdded) != 0 {
		t.Errorf("second register added %d (want 0): %v", len(res.HooksAdded), res.HooksAdded)
	}
	if len(res.AlreadySet) != len(claudeCodeEvents) {
		t.Errorf("AlreadySet %d want %d", len(res.AlreadySet), len(claudeCodeEvents))
	}
}

func TestRegisterClaudeCodePreservesOtherKeys(t *testing.T) {
	t.Parallel()
	r := setupRegistry(t)
	// Write a settings.json with unrelated fields that we must not clobber.
	pre := `{"theme":"dark","permissions":{"allow":["bash"]}}`
	path := filepath.Join(r.opts.HomeDir, ".claude", "settings.json")
	if err := os.WriteFile(path, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}

	res := r.Register("claude-code")
	if res.Error != nil {
		t.Fatalf("Register: %v", res.Error)
	}
	body, _ := os.ReadFile(path)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["theme"] != "dark" {
		t.Errorf("theme lost: %v", got["theme"])
	}
	if _, ok := got["permissions"]; !ok {
		t.Error("permissions lost")
	}
}

func TestRegisterClaudeCodeConflict(t *testing.T) {
	t.Parallel()
	r := setupRegistry(t)
	path := filepath.Join(r.opts.HomeDir, ".claude", "settings.json")
	pre := `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"/usr/local/bin/other-hook"}]}]}}`
	if err := os.WriteFile(path, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}
	res := r.Register("claude-code")
	if res.Error == nil {
		t.Fatal("expected conflict error")
	}

	// With Force, should succeed and add our hook alongside.
	r.opts.Force = true
	res = r.Register("claude-code")
	if res.Error != nil {
		t.Fatalf("force register: %v", res.Error)
	}
}

func TestRegisterCursorFreshInstall(t *testing.T) {
	t.Parallel()
	r := setupRegistry(t)
	res := r.Register("cursor")
	if res.Error != nil {
		t.Fatalf("Register: %v", res.Error)
	}
	body, err := os.ReadFile(res.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, body)
	}
	if got["version"] == nil {
		t.Error("cursor version missing")
	}
	hooks, ok := got["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks block missing")
	}
	for _, event := range cursorEvents {
		if _, ok := hooks[event]; !ok {
			t.Errorf("event %s missing", event)
		}
	}
}

func TestRegisterCursorIdempotent(t *testing.T) {
	t.Parallel()
	r := setupRegistry(t)
	_ = r.Register("cursor")
	res := r.Register("cursor")
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if len(res.HooksAdded) != 0 {
		t.Errorf("second add added %d", len(res.HooksAdded))
	}
}

func TestRegisterUnknownTool(t *testing.T) {
	t.Parallel()
	r := setupRegistry(t)
	res := r.Register("notarealthing")
	if res.Error == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestDryRunDoesNotWrite(t *testing.T) {
	t.Parallel()
	r := setupRegistry(t)
	r.opts.DryRun = true
	res := r.Register("claude-code")
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	_, err := os.Stat(res.ConfigPath)
	if err == nil {
		t.Error("dry run wrote the config")
	}
	if !res.DryRun {
		t.Error("result should flag dry run")
	}
}

func TestInstalledDetectsDirs(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(Options{
		BinaryPath: "/x",
		HomeDir:    home,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := r.Installed()
	// "claude-code" must be present (the .claude/ in HomeDir was the
	// signal). Other entries (notably "cursor-windows" on WSL hosts
	// where crossmount detects a Windows-side .cursor/) are platform-
	// dependent — the test only asserts the .claude/ trigger, not the
	// absence of cross-mount auto-detection.
	if !containsString(got, "claude-code") {
		t.Errorf("Installed = %v, want it to contain claude-code", got)
	}
}

func containsString(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

func TestCommandContainsBinaryPath(t *testing.T) {
	t.Parallel()
	r := setupRegistry(t)
	if res := r.Register("claude-code"); res.Error != nil {
		t.Fatal(res.Error)
	}
	body, _ := os.ReadFile(filepath.Join(r.opts.HomeDir, ".claude", "settings.json"))
	if !strings.Contains(string(body), r.opts.BinaryPath) {
		t.Errorf("binary path not in settings: %s", body)
	}
}

// TestRegisterClaudeCodeWithConfigPath pins the v1.4.43+ hook→DB-wiring
// fix: when Options.ConfigPath is set, every registered hook command
// gets `--config <path>` appended so the spawned hook handler reads
// the same DB the proxy writes to. Without this, /compact rows always
// land on ~/.observer/observer.db regardless of which proxy daemon
// fired the hook — D23's Injector then queries the proxy's DB and
// finds nothing. Surfaced 2026-05-08 dogfood.
func TestRegisterClaudeCodeWithConfigPath(t *testing.T) {
	t.Parallel()
	r := setupRegistryWithConfig(t, "/tmp/ab-claude/on/observer-config.toml")
	res := r.Register("claude-code")
	if res.Error != nil {
		t.Fatalf("Register: %v", res.Error)
	}
	body, err := os.ReadFile(res.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	want := r.opts.BinaryPath + " hook claude-code pre-compact --config '/tmp/ab-claude/on/observer-config.toml'"
	if !strings.Contains(string(body), want) {
		t.Errorf("expected hook command to include --config; got:\n%s", body)
	}
}

// TestRegisterClaudeCodeRefreshesOnConfigPathChange pins that re-running
// init with a different config silently overwrites the registration
// instead of being treated as already-set. Without this, switching
// configs would leave the hook pointing at the old DB.
func TestRegisterClaudeCodeRefreshesOnConfigPathChange(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	first, err := NewRegistry(Options{
		BinaryPath:    "/opt/observer/bin/observer",
		HomeDir:       home,
		ChecksumsPath: filepath.Join(home, ".observer", "hook_checksums.json"),
		ConfigPath:    "/tmp/old.toml",
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := first.Register("claude-code"); r.Error != nil {
		t.Fatalf("first: %v", r.Error)
	}

	second, err := NewRegistry(Options{
		BinaryPath:    "/opt/observer/bin/observer",
		HomeDir:       home,
		ChecksumsPath: filepath.Join(home, ".observer", "hook_checksums.json"),
		ConfigPath:    "/tmp/new.toml",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := second.Register("claude-code")
	if res.Error != nil {
		t.Fatalf("second: %v", res.Error)
	}
	if len(res.HooksAdded) == 0 {
		t.Fatal("expected refresh to re-add events with new config")
	}
	body, _ := os.ReadFile(res.ConfigPath)
	if strings.Contains(string(body), "/tmp/old.toml") {
		t.Errorf("old config path leaked into refreshed registration: %s", body)
	}
	if !strings.Contains(string(body), "/tmp/new.toml") {
		t.Errorf("new config path missing: %s", body)
	}
}

// TestRegisterCursorWindowsFreshInstall pins the wsl.exe-prefixed
// hook command shape against a fake Windows-side .cursor/. The
// resulting hooks.json must carry every cursorEvents entry, each
// shaped `wsl.exe -d <distro> -- <linux-bin> hook cursor <event>
// [--config ...]`. Tests the explicit-WindowsCursorHome path so the
// crossmount auto-detect doesn't depend on /mnt/c presence.
func TestRegisterCursorWindowsFreshInstall(t *testing.T) {
	t.Parallel()
	winHome := t.TempDir() // stand-in for /mnt/c/Users/<u>
	cursorDir := filepath.Join(winHome, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wslHome := t.TempDir()
	r, err := NewRegistry(Options{
		BinaryPath:        "/home/marmutapp/superbased-observer/bin/observer",
		HomeDir:           wslHome,
		ChecksumsPath:     filepath.Join(wslHome, ".observer", "hook_checksums.json"),
		WindowsCursorHome: winHome,
		WSLDistro:         "Ubuntu-20.04",
		ConfigPath:        "/home/marmutapp/.observer/config.toml",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := r.Register("cursor-windows")
	if res.Error != nil {
		t.Fatalf("Register: %v", res.Error)
	}
	if len(res.HooksAdded) != len(cursorEvents) {
		t.Errorf("HooksAdded count = %d want %d", len(res.HooksAdded), len(cursorEvents))
	}
	if res.ConfigPath != filepath.Join(cursorDir, "hooks.json") {
		t.Errorf("ConfigPath = %q want %q", res.ConfigPath, filepath.Join(cursorDir, "hooks.json"))
	}

	body, err := os.ReadFile(res.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, body)
	}
	hooksBlock, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks block missing")
	}
	for _, event := range cursorEvents {
		entries, ok := hooksBlock[event].([]any)
		if !ok || len(entries) == 0 {
			t.Errorf("event %s missing", event)
			continue
		}
		first, _ := entries[0].(map[string]any)
		cmd, _ := first["command"].(string)
		wantPrefix := "wsl.exe -d Ubuntu-20.04 -- /home/marmutapp/superbased-observer/bin/observer hook cursor " + event
		if !strings.HasPrefix(cmd, wantPrefix) {
			t.Errorf("event %s cmd = %q want prefix %q", event, cmd, wantPrefix)
		}
		if !strings.Contains(cmd, "--config '/home/marmutapp/.observer/config.toml'") {
			t.Errorf("event %s cmd missing --config: %q", event, cmd)
		}
	}
}

// TestRegisterCursorWindowsRequiresDistro pins the
// "missing distro" error path. Without a distro, `wsl.exe` would be
// ambiguous on a host with multiple WSL distros — better to fail
// loudly at install time than write a broken hooks.json.
func TestRegisterCursorWindowsRequiresDistro(t *testing.T) {
	winHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(winHome, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	wslHome := t.TempDir()
	t.Setenv("WSL_DISTRO_NAME", "")
	r, err := NewRegistry(Options{
		BinaryPath:        "/x",
		HomeDir:           wslHome,
		WindowsCursorHome: winHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := r.Register("cursor-windows")
	if res.Error == nil {
		t.Fatal("expected error when distro unset, got nil")
	}
	if !strings.Contains(res.Error.Error(), "WSL distro unknown") {
		t.Errorf("error message = %q want it to mention 'WSL distro unknown'", res.Error)
	}
}

// TestRegisterCursorWindowsCrossBinaryPathRefresh pins the upgrade
// path: when a hooks.json was written by a previous observer
// install (different binary path), re-registering with a new
// binary should silently refresh the entries — not error with
// "non-observer hook" as if a foreign tool had written them. This
// is the staleness-detection bug that surfaced when the
// auto-register pass on `observer start` ran twice with different
// binary paths (e.g. /tmp smoke-test vs production install).
func TestRegisterCursorWindowsCrossBinaryPathRefresh(t *testing.T) {
	t.Parallel()
	winHome := t.TempDir()
	cursorDir := filepath.Join(winHome, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wslHome := t.TempDir()

	// First install: binary at /tmp/observer-A.
	first, err := NewRegistry(Options{
		BinaryPath:        "/tmp/observer-A",
		HomeDir:           wslHome,
		ChecksumsPath:     filepath.Join(wslHome, ".observer", "hook_checksums.json"),
		WindowsCursorHome: winHome,
		WSLDistro:         "Ubuntu-20.04",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res := first.Register("cursor-windows"); res.Error != nil {
		t.Fatalf("first: %v", res.Error)
	}

	// Second install: same registry, NEW binary path. Should
	// silently refresh, not error.
	second, err := NewRegistry(Options{
		BinaryPath:        "/usr/local/bin/observer",
		HomeDir:           wslHome,
		ChecksumsPath:     filepath.Join(wslHome, ".observer", "hook_checksums.json"),
		WindowsCursorHome: winHome,
		WSLDistro:         "Ubuntu-20.04",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := second.Register("cursor-windows")
	if res.Error != nil {
		t.Fatalf("second (cross-binary refresh): %v", res.Error)
	}
	if len(res.HooksAdded) != len(cursorEvents) {
		t.Errorf("HooksAdded = %d want %d", len(res.HooksAdded), len(cursorEvents))
	}

	// Verify the file ONLY references the new binary now.
	body, _ := os.ReadFile(res.ConfigPath)
	if strings.Contains(string(body), "/tmp/observer-A") {
		t.Errorf("stale binary path leaked into refreshed hooks.json:\n%s", body)
	}
	if !strings.Contains(string(body), "/usr/local/bin/observer") {
		t.Errorf("new binary path missing:\n%s", body)
	}
}

// TestRegisterCursorWindowsRespectsForeignEntry pins the
// safety-first behavior: a non-observer-shaped command (e.g. user
// has a different tool's hook wired in) must NOT be silently
// overwritten. Force=false → return error; Force=true → overwrite.
func TestRegisterCursorWindowsRespectsForeignEntry(t *testing.T) {
	t.Parallel()
	winHome := t.TempDir()
	cursorDir := filepath.Join(winHome, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Plant a foreign hook in beforeShellExecution.
	hooksPath := filepath.Join(cursorDir, "hooks.json")
	foreign := `{"version":1,"hooks":{"beforeShellExecution":[{"command":"powershell.exe Write-Host hi"}]}}`
	if err := os.WriteFile(hooksPath, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	wslHome := t.TempDir()
	r, err := NewRegistry(Options{
		BinaryPath:        "/tmp/observer",
		HomeDir:           wslHome,
		WindowsCursorHome: winHome,
		WSLDistro:         "Ubuntu-20.04",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := r.Register("cursor-windows")
	if res.Error == nil {
		t.Fatal("expected error when foreign hook present, got nil")
	}
	if !strings.Contains(res.Error.Error(), "non-observer") {
		t.Errorf("error message = %q want it to mention 'non-observer'", res.Error)
	}
}

// TestRegisterCursorWindowsIdempotent re-runs registration on an
// already-installed hooks.json and asserts no events get re-added.
func TestRegisterCursorWindowsIdempotent(t *testing.T) {
	t.Parallel()
	winHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(winHome, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	wslHome := t.TempDir()
	r, err := NewRegistry(Options{
		BinaryPath:        "/opt/observer/bin/observer",
		HomeDir:           wslHome,
		ChecksumsPath:     filepath.Join(wslHome, ".observer", "hook_checksums.json"),
		WindowsCursorHome: winHome,
		WSLDistro:         "Ubuntu-22.04",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res := r.Register("cursor-windows"); res.Error != nil {
		t.Fatalf("first: %v", res.Error)
	}
	res := r.Register("cursor-windows")
	if res.Error != nil {
		t.Fatalf("second: %v", res.Error)
	}
	if len(res.HooksAdded) != 0 {
		t.Errorf("second register added %d (want 0): %v", len(res.HooksAdded), res.HooksAdded)
	}
	if len(res.AlreadySet) != len(cursorEvents) {
		t.Errorf("AlreadySet %d want %d", len(res.AlreadySet), len(cursorEvents))
	}
}

// TestShellQuoteEscapesSingleQuotes pins the shell-quoting helper so
// pathological config paths (with embedded ' or spaces) don't break
// the bash -c invocation Claude Code uses to run the hook command.
func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"", "''"},
		{"/plain/path", "'/plain/path'"},
		{"/path with space", "'/path with space'"},
		{"/o'malley/x", `'/o'\''malley/x'`},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

// codexRegistry mirrors setupRegistry but seeds ~/.codex instead of
// ~/.claude / ~/.cursor so codex registration has a directory to scan.
func codexRegistry(t *testing.T) *Registry {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := NewRegistry(Options{
		BinaryPath:    "/opt/observer/bin/observer",
		HomeDir:       home,
		ChecksumsPath: filepath.Join(home, ".observer", "hook_checksums.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRegisterCodexFreshInstall(t *testing.T) {
	t.Parallel()
	r := codexRegistry(t)
	res := r.Register("codex")
	if res.Error != nil {
		t.Fatalf("Register: %v", res.Error)
	}
	if len(res.HooksAdded) != len(codexEvents) {
		t.Errorf("added %d want %d", len(res.HooksAdded), len(codexEvents))
	}
	if got, want := res.ConfigPath, filepath.Join(r.opts.HomeDir, ".codex", "hooks.json"); got != want {
		t.Errorf("ConfigPath=%q want %q", got, want)
	}

	// hooks.json shape: {"hooks": {<event>: [<group>...]}}
	body, err := os.ReadFile(res.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("hooks.json not valid JSON: %v\n%s", err, body)
	}
	hooks, ok := got["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks.json missing top-level hooks block: %s", body)
	}
	for _, event := range codexEvents {
		groups, ok := hooks[event].([]any)
		if !ok || len(groups) == 0 {
			t.Errorf("event %s missing in hooks.json", event)
		}
	}

	// config.toml feature flag: [features].hooks = true must be present.
	cfgRaw, err := os.ReadFile(filepath.Join(r.opts.HomeDir, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("config.toml not written: %v", err)
	}
	if !bytes.Contains(cfgRaw, []byte("hooks = true")) {
		t.Errorf("config.toml missing hooks=true:\n%s", cfgRaw)
	}
}

func TestRegisterCodexIdempotent(t *testing.T) {
	t.Parallel()
	r := codexRegistry(t)
	if res := r.Register("codex"); res.Error != nil {
		t.Fatalf("first: %v", res.Error)
	}
	res := r.Register("codex")
	if res.Error != nil {
		t.Fatalf("second: %v", res.Error)
	}
	if len(res.HooksAdded) != 0 {
		t.Errorf("second register added %d (want 0)", len(res.HooksAdded))
	}
	if len(res.AlreadySet) != len(codexEvents) {
		t.Errorf("AlreadySet %d want %d", len(res.AlreadySet), len(codexEvents))
	}
}

func TestRegisterCodexPreservesExistingConfigToml(t *testing.T) {
	t.Parallel()
	r := codexRegistry(t)
	// Pre-existing config with unrelated keys our flag insert must
	// preserve. Mirrors a real ~/.codex/config.toml shape.
	pre := `personality = "pragmatic"

[projects."/tmp/foo"]
trust_level = "trusted"
`
	cfgPath := filepath.Join(r.opts.HomeDir, ".codex", "config.toml")
	if err := os.WriteFile(cfgPath, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}
	res := r.Register("codex")
	if res.Error != nil {
		t.Fatalf("Register: %v", res.Error)
	}
	body, _ := os.ReadFile(cfgPath)
	if !bytes.Contains(body, []byte("personality")) {
		t.Errorf("personality lost from config.toml:\n%s", body)
	}
	if !bytes.Contains(body, []byte("/tmp/foo")) {
		t.Errorf("projects table lost from config.toml:\n%s", body)
	}
	if !bytes.Contains(body, []byte("hooks = true")) {
		t.Errorf("hooks=true not added:\n%s", body)
	}
}

func TestRegisterCodexConflict(t *testing.T) {
	t.Parallel()
	r := codexRegistry(t)
	// Pre-existing user-authored hook on PreToolUse — our register
	// must refuse without --force.
	hooksPath := filepath.Join(r.opts.HomeDir, ".codex", "hooks.json")
	pre := `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"/usr/local/bin/my-policy"}]}]}}`
	if err := os.WriteFile(hooksPath, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}
	res := r.Register("codex")
	if res.Error == nil {
		t.Fatalf("expected conflict error, got HooksAdded=%v", res.HooksAdded)
	}
	if !strings.Contains(res.Error.Error(), "non-observer hook") {
		t.Errorf("unexpected error: %v", res.Error)
	}
}

func TestUnregisterCodexRemovesObserverEntries(t *testing.T) {
	t.Parallel()
	r := codexRegistry(t)
	if res := r.Register("codex"); res.Error != nil {
		t.Fatalf("Register: %v", res.Error)
	}
	res := r.Unregister("codex")
	if res.Error != nil {
		t.Fatalf("Unregister: %v", res.Error)
	}
	if len(res.HooksRemoved) != len(codexEvents) {
		t.Errorf("removed %d want %d", len(res.HooksRemoved), len(codexEvents))
	}
	body, _ := os.ReadFile(res.ConfigPath)
	if bytes.Contains(body, []byte("/opt/observer/bin/observer")) {
		t.Errorf("observer entries still present after unregister:\n%s", body)
	}
}

func TestInstalledDetectsCodex(t *testing.T) {
	t.Parallel()
	r := codexRegistry(t)
	got := r.Installed()
	found := false
	for _, t := range got {
		if t == "codex" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Installed()=%v does not include codex", got)
	}
}
