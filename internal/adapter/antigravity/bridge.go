package antigravity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// bridgeBinaryName is the file name of the Windows-side helper
// observer ships in `bin/`. Cross-compiled at build time via
// `make build` (target adds `bin/antigravity-bridge.exe`).
const bridgeBinaryName = "antigravity-bridge.exe"

// invokeBridgeFromWSL runs `antigravity-bridge.exe convert <uuid>`
// via powershell.exe (the helper itself runs Windows-side and uses
// Win32 APIs to discover language_server processes + their listening
// ports). Returns the bridge's stdout (Markdown) on success.
//
// Resolution order for the helper binary:
//
//  1. $OBSERVER_ANTIGRAVITY_BRIDGE — explicit override.
//  2. <observer-binary-dir>/antigravity-bridge.exe — the canonical
//     location when observer is installed via npm or `make build`.
//  3. <CWD>/bin/antigravity-bridge.exe — when running from the
//     development tree.
//
// If the resolved path is WSL-native (under /home/... rather than
// /mnt/<drive>/...), the bridge is auto-copied to a Windows-
// reachable temp location since powershell.exe can't reliably
// invoke WSL-native binaries by path on all distros.
//
// Returns ErrBridgeNotInstalled when no candidate exists; the
// caller surfaces this as an actionable warning rather than a
// hard error.
func invokeBridgeFromWSL(ctx context.Context, conversationID string, timeout time.Duration) (string, error) {
	out, err := invokeBridge(ctx, "convert", conversationID, timeout)
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return "", errors.New("bridge returned empty stdout")
	}
	return string(out), nil
}

// invokeBridgeStructuredFromWSL runs `antigravity-bridge.exe
// structured <uuid>` and returns the raw response payload (gRPC frame
// header already stripped by the bridge). The payload is binary
// protobuf — never decode as UTF-8.
//
// Failure modes mirror invokeBridgeFromWSL: bridge missing, no
// running language_server, gRPC error, etc. All non-zero exits
// surface as a single wrapped error so the caller can log and move
// on (the structured fetch is best-effort; markdown recovery already
// succeeded).
func invokeBridgeStructuredFromWSL(ctx context.Context, conversationID string, timeout time.Duration) ([]byte, error) {
	out, err := invokeBridge(ctx, "structured", conversationID, timeout)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("bridge returned empty stdout")
	}
	return out, nil
}

// invokeBridge runs the bridge with the given subcommand and returns
// raw stdout bytes. Subcommand-specific empty-stdout / decode logic
// stays in the caller.
func invokeBridge(ctx context.Context, subcommand, conversationID string, timeout time.Duration) ([]byte, error) {
	bridge, err := locateBridgeBinary()
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		return nil, fmt.Errorf("powershell.exe not on PATH: %w", err)
	}

	winPath, err := bridgePathForPowerShell(bridge)
	if err != nil {
		return nil, err
	}

	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"& '"+winPath+"' "+subcommand+" '"+conversationID+"'")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: exit %v; stderr: %s", filepath.Base(bridge), subcommand, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// bridgePathForPowerShell returns a Windows-shaped path that
// powershell.exe can invoke. If the source bridge path is already
// under /mnt/<drive>/, we just translate it. If it's WSL-native
// (under /home/...), we copy it once to a Windows-reachable temp
// directory and cache the destination — subsequent calls reuse
// the cached copy as long as it's same-size.
//
// Cache location: /mnt/c/Users/<u>/AppData/Local/Temp/observer/
// antigravity-bridge.exe. Falls back to /mnt/c/Windows/Temp/
// observer/ if no Users dirs exist.
func bridgePathForPowerShell(bridge string) (string, error) {
	if winPath := wslPathToWindows(bridge); winPath != "" {
		return winPath, nil
	}
	// WSL-native source. Pick a Windows-reachable destination.
	dest, err := windowsCacheDestination()
	if err != nil {
		return "", fmt.Errorf("bridge cache dest: %w", err)
	}
	if err := copyIfNewer(bridge, dest); err != nil {
		return "", fmt.Errorf("copy bridge to %s: %w", dest, err)
	}
	winPath := wslPathToWindows(dest)
	if winPath == "" {
		return "", fmt.Errorf("cached bridge path %q didn't translate", dest)
	}
	return winPath, nil
}

// windowsCacheDestination returns the /mnt/c/... target for the
// auto-copied bridge binary. Picks the first Users/<u>/AppData/
// Local/Temp dir we can write to, falling back to Windows/Temp.
//
// The result is cached process-wide via sync.Once: the original
// implementation re-probed every call, racing concurrent backfill
// invocations on a shared `.observer-write-probe` marker (one
// invocation removed the marker before the other read it, the loser
// returned `no writable /mnt/c destination`). Caching collapses
// per-process invocations to a single probe; the marker filename is
// PID-suffixed so cross-process first-calls don't race either.
var (
	cachedDestOnce sync.Once
	cachedDest     string
	cachedDestErr  error
)

func windowsCacheDestination() (string, error) {
	cachedDestOnce.Do(func() {
		cachedDest, cachedDestErr = resolveWindowsCacheDestination()
	})
	return cachedDest, cachedDestErr
}

func resolveWindowsCacheDestination() (string, error) {
	candidates := []string{}
	if entries, err := os.ReadDir("/mnt/c/Users"); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if name == "All Users" || name == "Default" || name == "Default User" || name == "Public" || name == "WsiAccount" {
				continue
			}
			candidates = append(candidates,
				"/mnt/c/Users/"+name+"/AppData/Local/Temp/observer")
		}
	}
	candidates = append(candidates, "/mnt/c/Windows/Temp/observer")
	pidSuffix := strconv.Itoa(os.Getpid())
	for _, dir := range candidates {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		// Probe write access by creating a PID-suffixed marker so
		// concurrent processes don't race on the same filename.
		marker := filepath.Join(dir, ".observer-write-probe."+pidSuffix)
		if err := os.WriteFile(marker, []byte("ok"), 0o644); err == nil {
			_ = os.Remove(marker)
			return filepath.Join(dir, bridgeBinaryName), nil
		}
	}
	return "", errors.New("no writable /mnt/c destination for bridge cache")
}

// copyIfNewer copies src to dst when dst is missing or has a
// different size than src. Idempotent + safe to call repeatedly;
// a same-size cached copy is reused.
func copyIfNewer(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if dstInfo, err := os.Stat(dst); err == nil && dstInfo.Size() == srcInfo.Size() {
		return nil
	}
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, body, 0o755) //nolint:gosec // executable; needs +x
}

// ErrBridgeNotInstalled is returned by locateBridgeBinary when
// the Windows-side helper isn't found. Callers should surface it
// as an installation hint rather than an internal error.
var ErrBridgeNotInstalled = errors.New("antigravity-bridge.exe not found — run `make build` to compile it, or set OBSERVER_ANTIGRAVITY_BRIDGE")

// locateBridgeBinary resolves the path to the bridge helper using
// the documented search order. Returns the first existing file or
// ErrBridgeNotInstalled.
func locateBridgeBinary() (string, error) {
	if env := strings.TrimSpace(os.Getenv("OBSERVER_ANTIGRAVITY_BRIDGE")); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), bridgeBinaryName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(cwd, "bin", bridgeBinaryName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", ErrBridgeNotInstalled
}

// wslPathToWindows converts /mnt/<drive>/path/to/file.exe to
// <DRIVE>:\path\to\file.exe. Returns "" for paths not under /mnt.
func wslPathToWindows(p string) string {
	if !strings.HasPrefix(p, "/mnt/") || len(p) < 7 {
		return ""
	}
	drive := strings.ToUpper(string(p[5]))
	rest := strings.ReplaceAll(p[6:], "/", `\`)
	if !strings.HasPrefix(rest, `\`) {
		rest = `\` + rest
	}
	return drive + ":" + rest
}
