package main

import (
	"strings"
	"testing"
)

// TestPrepareCodexArgs_InjectsOpenAIBaseURL pins the default behavior:
// when the user passes no -c openai_base_url override, the launcher
// prepends -c openai_base_url='"<proxy>/v1"' to argv.
func TestPrepareCodexArgs_InjectsOpenAIBaseURL(t *testing.T) {
	in := []string{"exec", "say hi"}
	out, info := prepareCodexArgs(in, "http://127.0.0.1:8830")

	if !info.OverrideInjected {
		t.Error("expected OverrideInjected=true")
	}
	if info.OverrideAlreadyPresent {
		t.Error("expected OverrideAlreadyPresent=false")
	}
	if len(out) != len(in)+2 {
		t.Fatalf("expected argv to grow by 2, got %d → %d (%v)", len(in), len(out), out)
	}
	if out[0] != "-c" {
		t.Errorf("expected -c at out[0], got %q", out[0])
	}
	want := `openai_base_url="http://127.0.0.1:8830/v1"`
	if out[1] != want {
		t.Errorf("override value: got %q, want %q", out[1], want)
	}
	if out[2] != "exec" || out[3] != "say hi" {
		t.Errorf("user args reordered: %v", out)
	}
}

// TestPrepareCodexArgs_StripsTrailingSlash verifies a trailing slash on
// proxyURL doesn't produce `//v1`.
func TestPrepareCodexArgs_StripsTrailingSlash(t *testing.T) {
	out, _ := prepareCodexArgs(nil, "http://127.0.0.1:8830/")
	if !strings.HasSuffix(out[1], `:8830/v1"`) {
		t.Errorf("expected single /v1 suffix, got %q", out[1])
	}
	if strings.Contains(out[1], "//v1") {
		t.Errorf("double slash leaked: %q", out[1])
	}
}

// TestPrepareCodexArgs_RespectsUserBaseURLOverride pins the
// "user wins" rule: if the user passes their own -c openai_base_url,
// the launcher doesn't double-inject.
func TestPrepareCodexArgs_RespectsUserBaseURLOverride(t *testing.T) {
	in := []string{"-c", `openai_base_url="http://other:9999/v1"`, "exec", "x"}
	out, info := prepareCodexArgs(in, "http://127.0.0.1:8830")

	if !info.OverrideAlreadyPresent {
		t.Error("expected OverrideAlreadyPresent=true")
	}
	if info.OverrideInjected {
		t.Error("expected OverrideInjected=false (user already set it)")
	}
	if len(out) != len(in) {
		t.Errorf("argv grew unexpectedly: %d → %d (%v)", len(in), len(out), out)
	}
	for _, kv := range out {
		if strings.Contains(kv, "127.0.0.1:8830") {
			t.Errorf("launcher injected proxy URL despite user override: %q", kv)
		}
	}
}

// TestPrepareCodexArgs_RespectsUserModelProvider pins the broader
// "user has set up routing" rule: passing -c model_provider=... also
// counts as the user having taken control of routing, so no inject.
func TestPrepareCodexArgs_RespectsUserModelProvider(t *testing.T) {
	in := []string{"-c", `model_provider="my_custom"`, "exec"}
	_, info := prepareCodexArgs(in, "http://127.0.0.1:8830")
	if !info.OverrideAlreadyPresent {
		t.Errorf("expected OverrideAlreadyPresent=true for -c model_provider; got %+v", info)
	}
}

// TestPrepareCodexArgs_RespectsLongConfigForm covers the --config k=v
// long-form variant (cobra accepts both -c and --config in codex's CLI).
func TestPrepareCodexArgs_RespectsLongConfigForm(t *testing.T) {
	in := []string{"--config", `openai_base_url="http://other"`, "exec"}
	_, info := prepareCodexArgs(in, "http://127.0.0.1:8830")
	if !info.OverrideAlreadyPresent {
		t.Errorf("--config form should be detected; got %+v", info)
	}
}

// TestPrepareCodexArgs_RespectsCombinedForm covers `-c=key=value` (no
// space). Codex accepts this per its --help.
func TestPrepareCodexArgs_RespectsCombinedForm(t *testing.T) {
	in := []string{`-c=openai_base_url="http://other"`, "exec"}
	_, info := prepareCodexArgs(in, "http://127.0.0.1:8830")
	if !info.OverrideAlreadyPresent {
		t.Errorf("combined -c=key=value form should be detected; got %+v", info)
	}
}

// TestPrepareCodexArgs_UnrelatedConfigDoesNotBlockInject pins that
// -c flags for non-routing fields (e.g., model="...") DON'T block the
// openai_base_url injection. Otherwise users would unintentionally
// suppress observer routing by setting any other config.
func TestPrepareCodexArgs_UnrelatedConfigDoesNotBlockInject(t *testing.T) {
	in := []string{"-c", `model="gpt-5-codex"`, "exec"}
	_, info := prepareCodexArgs(in, "http://127.0.0.1:8830")
	if info.OverrideAlreadyPresent {
		t.Errorf("-c model=... should NOT block injection; got %+v", info)
	}
	if !info.OverrideInjected {
		t.Errorf("expected OverrideInjected=true with unrelated -c flag; got %+v", info)
	}
}

// TestIsCodexRoutingOverride covers the predicate directly.
func TestIsCodexRoutingOverride(t *testing.T) {
	cases := []struct {
		kv   string
		want bool
	}{
		{`openai_base_url="x"`, true},
		{`model_provider="x"`, true},
		{`model="gpt-5"`, false},
		{`features.foo=true`, false},
		{`malformed`, false}, // no =
		{``, false},
	}
	for _, tc := range cases {
		if got := isCodexRoutingOverride(tc.kv); got != tc.want {
			t.Errorf("isCodexRoutingOverride(%q) = %v, want %v", tc.kv, got, tc.want)
		}
	}
}
