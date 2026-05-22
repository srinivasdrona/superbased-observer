package antigravity

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/marmutapp/superbased-observer/internal/adapter"
	"github.com/marmutapp/superbased-observer/internal/models"
)

// adapterParseResultStub is a tiny test-only helper that builds an
// adapter.ParseResult with the requested number of ToolEvents and
// TokenEvents, so numEvents() can be exercised without depending on
// the full event-construction machinery.
type adapterParseResultStub struct {
	tools  int
	tokens int
}

func (s adapterParseResultStub) toResult() adapter.ParseResult {
	res := adapter.ParseResult{}
	for i := 0; i < s.tools; i++ {
		res.ToolEvents = append(res.ToolEvents, models.ToolEvent{})
	}
	for i := 0; i < s.tokens; i++ {
		res.TokenEvents = append(res.TokenEvents, models.TokenEvent{})
	}
	return res
}

// TestWorkspaceIDFromURI pins the encoding the antigravity language_server
// uses for its --workspace_id cmdline flag. Verified against running pid
// 694 on a real WSL host (2026-05-10): cmdline arg
// `--workspace_id file_home_marmutapp_superbased_observer` for the
// workspace at `/home/marmutapp/superbased-observer`.
func TestWorkspaceIDFromURI(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		want string
	}{
		{
			name: "linux home with hyphen",
			uri:  "file:///home/marmutapp/superbased-observer",
			want: "file_home_marmutapp_superbased_observer",
		},
		{
			name: "linux home no hyphen",
			uri:  "file:///home/marmutapp/superbased",
			want: "file_home_marmutapp_superbased",
		},
		{
			name: "windows path via WSL mount",
			uri:  "file:///mnt/c/Users/auzy_/proj",
			want: "file_mnt_c_Users_auzy__proj",
		},
		{
			name: "URL-encoded characters",
			uri:  "file:///home/me/my%20project",
			want: "file_home_me_my_project",
		},
		{
			name: "vscode-remote URI",
			uri:  "vscode-remote://wsl%2BUbuntu/home/me/proj",
			want: "file_wsl_Ubuntu_home_me_proj",
		},
		{
			name: "trailing slash",
			uri:  "file:///home/me/proj/",
			want: "file_home_me_proj_",
		},
		{
			name: "alphanumerics preserved",
			uri:  "file:///home/u/Proj42",
			want: "file_home_u_Proj42",
		},
		{
			name: "empty",
			uri:  "",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := workspaceIDFromURI(c.uri)
			if got != c.want {
				t.Errorf("workspaceIDFromURI(%q) = %q, want %q", c.uri, got, c.want)
			}
		})
	}
}

// TestLanguageServerEndpoints pins the iteration order: PreferredEndpoint
// first (heuristic best-guess for backwards-compat), then http://port for
// every owned port, then https://port for every owned port. Duplicates
// dedup. Empty Ports falls back to HTTPPort/HTTPSPort.
func TestLanguageServerEndpoints(t *testing.T) {
	cases := []struct {
		name string
		ls   LanguageServer
		want []string
	}{
		{
			name: "two ports — preferred is HTTP, full iteration follows",
			ls: LanguageServer{
				PID:       694,
				HTTPPort:  37933,
				HTTPSPort: 35989,
				Ports:     []int{35989, 37933},
			},
			// PreferredEndpoint() returns http://...:37933 (HTTPPort wins).
			// Then http for every port (35989 first ascending, 37933 dedup).
			// Then https for every port (35989, 37933).
			want: []string{
				"http://127.0.0.1:37933",
				"http://127.0.0.1:35989",
				"https://127.0.0.1:35989",
				"https://127.0.0.1:37933",
			},
		},
		{
			name: "three ports — long tail of plausible candidates",
			ls: LanguageServer{
				PID:       694,
				HTTPPort:  37933,
				HTTPSPort: 35989,
				Ports:     []int{35989, 37933, 41207},
			},
			want: []string{
				"http://127.0.0.1:37933",
				"http://127.0.0.1:35989",
				"http://127.0.0.1:41207",
				"https://127.0.0.1:35989",
				"https://127.0.0.1:37933",
				"https://127.0.0.1:41207",
			},
		},
		{
			name: "single port — both protocols tried",
			ls: LanguageServer{
				PID:       846,
				HTTPSPort: 34109,
				Ports:     []int{34109},
			},
			want: []string{
				"https://127.0.0.1:34109",
				"http://127.0.0.1:34109",
			},
		},
		{
			name: "no ports — empty",
			ls:   LanguageServer{PID: 999},
			want: nil,
		},
		{
			name: "fallback to HTTPPort/HTTPSPort when Ports empty",
			ls: LanguageServer{
				PID:       100,
				HTTPPort:  8080,
				HTTPSPort: 8443,
			},
			want: []string{
				"http://127.0.0.1:8080",
				"https://127.0.0.1:8443",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.ls.Endpoints()
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Endpoints() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestSortServersByWorkspaceMatch pins the ordering: matching workspace
// first, non-matching after. wantWS == "" returns the input unchanged.
func TestSortServersByWorkspaceMatch(t *testing.T) {
	a := LanguageServer{PID: 1, WorkspaceID: "file_home_a"}
	b := LanguageServer{PID: 2, WorkspaceID: "file_home_b"}
	c := LanguageServer{PID: 3, WorkspaceID: "file_home_a"}

	cases := []struct {
		name     string
		input    []LanguageServer
		wantWS   string
		wantPIDs []int
	}{
		{
			name:     "empty wantWS preserves order",
			input:    []LanguageServer{a, b, c},
			wantWS:   "",
			wantPIDs: []int{1, 2, 3},
		},
		{
			name:     "matches sorted first",
			input:    []LanguageServer{a, b, c},
			wantWS:   "file_home_a",
			wantPIDs: []int{1, 3, 2},
		},
		{
			name:     "no matches preserves order",
			input:    []LanguageServer{a, b, c},
			wantWS:   "file_home_does_not_exist",
			wantPIDs: []int{1, 2, 3},
		},
		{
			name:     "single match preserved",
			input:    []LanguageServer{b, a, c},
			wantWS:   "file_home_b",
			wantPIDs: []int{2, 1, 3},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sortServersByWorkspaceMatch(tc.input, tc.wantWS)
			gotPIDs := make([]int, len(got))
			for i, s := range got {
				gotPIDs[i] = s.PID
			}
			if !reflect.DeepEqual(gotPIDs, tc.wantPIDs) {
				t.Errorf("PIDs = %v, want %v", gotPIDs, tc.wantPIDs)
			}
		})
	}
}

// TestNumEvents pins the empty-stub guard's count helper. Used by
// recoverViaLocalGRPC: when a server responds but extracted events ==
// 0, the response is treated as a wrong-workspace stub and the next
// server is tried.
func TestNumEvents(t *testing.T) {
	type rs = adapterParseResultStub
	cases := []struct {
		name string
		in   rs
		want int
	}{
		{"empty", rs{}, 0},
		{"only tools", rs{tools: 3}, 3},
		{"only tokens", rs{tokens: 2}, 2},
		{"both", rs{tools: 3, tokens: 2}, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := numEvents(c.in.toResult())
			if got != c.want {
				t.Errorf("numEvents = %d, want %d", got, c.want)
			}
		})
	}
}

// TestResolveWorkspaceIDToPath pins the reverse-encoding walk: given
// a language_server's --workspace_id flag, find the real path on disk
// whose workspaceIDFromURI encoding matches. Lossy-encoding inverse
// (because '/', '-', '.' all map to '_') is approached via BFS over
// candidate roots up to maxDepth.
func TestResolveWorkspaceIDToPath(t *testing.T) {
	dir := t.TempDir()
	// Build:  <tmp>/home/marmutapp/superbased-observer/
	//         <tmp>/home/marmutapp/superbased/
	//         <tmp>/home/marmutapp/.config/  (skipped: starts with .)
	//         <tmp>/home/marmutapp/node_modules/  (skipped: noisy root)
	for _, sub := range []string{
		"home/marmutapp/superbased-observer",
		"home/marmutapp/superbased",
		"home/marmutapp/.config/Foo",
		"home/marmutapp/node_modules/Bar",
	} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(dir)

	cases := []struct {
		name     string
		wsID     string
		want     string
		wantHit  bool
		maxDepth int
	}{
		{
			name:     "exact-match (hyphen variant)",
			wsID:     workspaceIDFromURI("file://" + filepath.Join(root, "home/marmutapp/superbased-observer")),
			want:     filepath.Join(root, "home/marmutapp/superbased-observer"),
			wantHit:  true,
			maxDepth: 4,
		},
		{
			name:     "exact-match (no-hyphen)",
			wsID:     workspaceIDFromURI("file://" + filepath.Join(root, "home/marmutapp/superbased")),
			want:     filepath.Join(root, "home/marmutapp/superbased"),
			wantHit:  true,
			maxDepth: 4,
		},
		{
			name:     "no match — wrong wsID",
			wsID:     "file_does_not_exist_anywhere",
			want:     "",
			wantHit:  false,
			maxDepth: 4,
		},
		{
			name:     "no match — depth too shallow to reach the target",
			wsID:     workspaceIDFromURI("file://" + filepath.Join(root, "home/marmutapp/superbased-observer")),
			want:     "",
			wantHit:  false,
			maxDepth: 1,
		},
		{
			name:     "missing prefix — wsID without file_ returns empty",
			wsID:     "home_marmutapp_superbased_observer",
			want:     "",
			wantHit:  false,
			maxDepth: 4,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveWorkspaceIDToPath(tc.wsID, []string{root}, tc.maxDepth)
			if (got != "") != tc.wantHit {
				t.Fatalf("hit=%v want=%v (got %q)", got != "", tc.wantHit, got)
			}
			if tc.wantHit && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveWorkspaceIDToPath_SkipsDotfilesAndNoise pins the BFS's
// pruning rules: directories starting with '.' (hidden) and known-
// noisy roots (node_modules, vendor, target) are skipped to keep the
// walk cheap on real filesystems.
func TestResolveWorkspaceIDToPath_SkipsDotfilesAndNoise(t *testing.T) {
	dir := t.TempDir()
	// Hide the target inside .config — should NOT be found.
	hidden := filepath.Join(dir, "home/u/.config/proj")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	wsID := workspaceIDFromURI("file://" + hidden)
	got := resolveWorkspaceIDToPath(wsID, []string{dir}, 8)
	if got != "" {
		t.Errorf("BFS should skip dotfiles; got %q", got)
	}
	// node_modules subtree skipped.
	noisy := filepath.Join(dir, "home/u/proj/node_modules/inner")
	if err := os.MkdirAll(noisy, 0o755); err != nil {
		t.Fatal(err)
	}
	wsID2 := workspaceIDFromURI("file://" + noisy)
	got2 := resolveWorkspaceIDToPath(wsID2, []string{dir}, 8)
	if got2 != "" {
		t.Errorf("BFS should skip node_modules; got %q", got2)
	}
}

// TestEndpointCacheKey pins the cache identity used to skip the
// heuristic-bad first endpoint. Two servers with same PID but
// different CSRFTokens (i.e. different process generations) must
// produce distinct keys so a stale cache entry from a killed
// language_server doesn't leak into a fresh one with the recycled PID.
func TestEndpointCacheKey(t *testing.T) {
	a := LanguageServer{PID: 694, CSRFToken: "csrf-A"}
	b := LanguageServer{PID: 694, CSRFToken: "csrf-B"}
	c := LanguageServer{PID: 846, CSRFToken: "csrf-A"}
	if endpointCacheKey(a) == endpointCacheKey(b) {
		t.Errorf("same PID but different CSRF must produce distinct keys")
	}
	if endpointCacheKey(a) == endpointCacheKey(c) {
		t.Errorf("different PID must produce distinct keys")
	}
}

// TestAdapterEndpointCacheRoundtrip pins the cache hit/miss/invalidate
// flow. Direct exercise of the unexported cache-helper methods so the
// recovery path's behavior is testable without spinning up a real
// language_server.
func TestAdapterEndpointCacheRoundtrip(t *testing.T) {
	a := New()
	const key = "694:csrf-X"
	if got := a.cachedEndpoint(key); got != "" {
		t.Errorf("fresh adapter must return empty string for unknown key, got %q", got)
	}
	a.cacheEndpoint(key, "http://127.0.0.1:35989")
	if got := a.cachedEndpoint(key); got != "http://127.0.0.1:35989" {
		t.Errorf("cached endpoint mismatch: %q", got)
	}
	a.invalidateEndpoint(key)
	if got := a.cachedEndpoint(key); got != "" {
		t.Errorf("invalidated key must return empty, got %q", got)
	}
	// Cache should accept multiple keys independently.
	a.cacheEndpoint("694:csrf-A", "http://127.0.0.1:35989")
	a.cacheEndpoint("846:csrf-B", "http://127.0.0.1:34109")
	if a.cachedEndpoint("694:csrf-A") != "http://127.0.0.1:35989" {
		t.Errorf("multi-key cache lost first entry")
	}
	if a.cachedEndpoint("846:csrf-B") != "http://127.0.0.1:34109" {
		t.Errorf("multi-key cache lost second entry")
	}
}

// TestParseTCPListenersFiltersByInode is a smoke check that the helper
// observer relies on (matches /proc/<pid>/fd inodes against
// /proc/<pid>/net/tcp LISTEN-state rows) returns only ports owned by
// the target process. Synthetic fixture; no /proc access.
func TestParseTCPListenersFiltersByInode(t *testing.T) {
	body := strings.Join([]string{
		"  sl  local_address rem_address st  tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode",
		"   0: 0100007F:8D5D 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 6095",
		"   1: 0100007F:9402 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 6096",
		"   2: 0100007F:A0F7 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 9999",
	}, "\n")
	owned := map[string]bool{"6095": true, "6096": true}
	got := parseTCPListeners(body, owned)
	sort.Ints(got)
	want := []int{36189, 37890} // 0x8D5D=36189; 0x9402=37890
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseTCPListeners = %v, want %v (filters out inode 9999)", got, want)
	}
}
