// codex.go — `observer codex` launcher subcommand.
//
// Codex CLI 0.129.0 supports two auth paths (API key sk-... and
// ChatGPT-Plus subscription JWT) and routes both through `/v1/responses`
// — the API-key form against api.openai.com directly, the JWT form
// against chatgpt.com/backend-api/codex/responses. Both forms can be
// redirected at observer's proxy by overriding the built-in `openai`
// provider's base URL via the `openai_base_url` top-level config field.
//
// The launcher injects `-c openai_base_url='"<proxy>/v1"'` into codex's
// argv so observer's proxy intercepts the request body. The proxy
// detects the auth shape (sk- vs eyJ JWT) and path-translates to
// chatgpt.com when needed (see internal/proxy/provider.go::isChatGPTAuthRequest
// + translateChatGPTPath). Same upstream billing — observer just gets
// to see (and compress) the body.
//
// Distinct from `observer claude`: codex doesn't have an OAuth token
// re-export problem because both auth shapes already ride the standard
// Authorization Bearer header that the proxy intercepts unmodified.
// All we need is to point the base URL at the proxy.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/marmutapp/superbased-observer/internal/config"
)

// newCodexCmd implements `observer codex` — runs the user's `codex`
// binary with `-c openai_base_url='"<proxy>/v1"'` injected so the
// Responses API call lands at the observer proxy.
func newCodexCmd() *cobra.Command {
	var (
		configPath string
		proxyURL   string
		codexPath  string
	)
	cmd := &cobra.Command{
		Use:   "codex [-- codex-args...]",
		Short: "Launch Codex CLI with traffic routed through the observer proxy",
		Long: "Wraps `codex` with `-c openai_base_url='\"<proxy>/v1\"'` injected\n" +
			"into argv so the Responses API request lands at the observer proxy.\n" +
			"Both auth paths (API-key sk-... and ChatGPT-Plus JWT) flow through\n" +
			"the same override — the proxy detects the bearer shape and routes\n" +
			"to api.openai.com vs chatgpt.com/backend-api/codex/responses\n" +
			"automatically.\n\n" +
			"All arguments after the subcommand are forwarded to codex. Use\n" +
			"`--` to separate observer flags from codex flags:\n" +
			"    observer codex -- exec \"hello world\"\n\n" +
			"Requires a running observer proxy. Start one with `observer start`\n" +
			"or `observer proxy start` first.",
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCodexLauncher(codexLauncherOptions{
				configPath: configPath,
				proxyURL:   proxyURL,
				codexPath:  codexPath,
				codexArgs:  args,
				stderr:     cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Path to config.toml (defaults to ~/.observer/config.toml)")
	cmd.Flags().StringVar(&proxyURL, "proxy", "", "Override the observer proxy URL (default: http://127.0.0.1:<cfg.proxy.port>)")
	cmd.Flags().StringVar(&codexPath, "codex-path", "", "Path to the codex binary (default: resolve `codex` on PATH)")
	cmd.Flags().SetInterspersed(false)
	return cmd
}

type codexLauncherOptions struct {
	configPath string
	proxyURL   string
	codexPath  string
	codexArgs  []string
	stderr     interface{ Write([]byte) (int, error) }
}

// runCodexLauncher resolves the proxy URL, prepares the child argv with
// the openai_base_url override, and execs codex. Exit code is forwarded
// via exitErr (same shape as `observer run`).
func runCodexLauncher(opts codexLauncherOptions) error {
	cfg, err := config.Load(config.LoadOptions{GlobalPath: opts.configPath})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	proxyURL := opts.proxyURL
	if proxyURL == "" {
		port := cfg.Proxy.Port
		if port <= 0 {
			port = 8820
		}
		proxyURL = "http://127.0.0.1:" + strconv.Itoa(port)
	}

	bin := opts.codexPath
	if bin == "" {
		resolved, lookErr := exec.LookPath("codex")
		if lookErr != nil {
			return fmt.Errorf("locate codex binary: %w (set --codex-path)", lookErr)
		}
		bin = resolved
	}

	// Soft-warn if the proxy isn't reachable.
	if !proxyReachable(proxyURL, 250*time.Millisecond) {
		fmt.Fprintf(opts.stderr,
			"observer codex: warning — proxy not reachable at %s (start it with `observer start`)\n",
			proxyURL)
	}

	args, info := prepareCodexArgs(opts.codexArgs, proxyURL)
	if info.OverrideAlreadyPresent {
		fmt.Fprintf(opts.stderr,
			"observer codex: routing via existing -c openai_base_url override (no inject; user-provided)\n")
	} else {
		fmt.Fprintf(opts.stderr,
			"observer codex: routing via %s (-c openai_base_url injected; auth shape detected by proxy)\n",
			proxyURL)
	}

	child := exec.Command(bin, args...)
	child.Env = os.Environ()
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return exitErr(ee.ExitCode())
		}
		return fmt.Errorf("exec codex: %w", err)
	}
	return nil
}

// codexArgsInfo records what the launcher injected into codex's argv.
type codexArgsInfo struct {
	OverrideInjected       bool
	OverrideAlreadyPresent bool // user passed their own -c openai_base_url
}

// prepareCodexArgs prepends `-c openai_base_url='"<proxy>/v1"'` to
// codex's argv, unless the user already supplied an `openai_base_url`
// override (via -c openai_base_url=... OR -c model_provider=... — both
// imply intentional routing). Anything the user explicitly set wins;
// the launcher never overrides explicit state.
//
// The override value is TOML-encoded (a string literal must be wrapped
// in quotes inside the TOML value).
func prepareCodexArgs(parent []string, proxyURL string) ([]string, codexArgsInfo) {
	info := codexArgsInfo{}
	if hasUserCodexConfigOverride(parent) {
		info.OverrideAlreadyPresent = true
		// Pass parent through unchanged. User's intent wins.
		return append([]string{}, parent...), info
	}
	// Strip a trailing slash from proxyURL before appending /v1 so we
	// don't end up with `//v1`.
	base := strings.TrimRight(proxyURL, "/")
	override := "openai_base_url=\"" + base + "/v1\""
	out := make([]string, 0, len(parent)+2)
	out = append(out, "-c", override)
	out = append(out, parent...)
	info.OverrideInjected = true
	return out, info
}

// hasUserCodexConfigOverride detects whether the user passed their own
// `openai_base_url` or `model_provider` override in argv. We respect
// either as "user has set up routing" — don't inject.
//
// Matches both `-c key=value` and `--config key=value` shapes, plus the
// space-separated form `-c key=value` where the override comes as the
// next argv slot (`-c`, `key=value`).
func hasUserCodexConfigOverride(args []string) bool {
	for i, a := range args {
		// Combined forms: -c=key=value / --config=key=value
		// (Cobra-style; codex accepts both per its --help.)
		switch {
		case strings.HasPrefix(a, "-c="):
			if isCodexRoutingOverride(a[len("-c="):]) {
				return true
			}
		case strings.HasPrefix(a, "--config="):
			if isCodexRoutingOverride(a[len("--config="):]) {
				return true
			}
		case a == "-c" || a == "--config":
			if i+1 < len(args) && isCodexRoutingOverride(args[i+1]) {
				return true
			}
		}
	}
	return false
}

// isCodexRoutingOverride returns true when `kv` (a `key=value` blob
// codex parses as TOML) sets a routing-relevant field.
func isCodexRoutingOverride(kv string) bool {
	eq := strings.IndexByte(kv, '=')
	if eq <= 0 {
		return false
	}
	key := strings.TrimSpace(kv[:eq])
	switch key {
	case "openai_base_url", "model_provider":
		return true
	}
	return false
}

// proxyReachable + splitHostPortFromURL are reused from claude.go in
// the same package — codex routes through the same proxy as claude.
