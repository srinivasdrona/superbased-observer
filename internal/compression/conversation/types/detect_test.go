package types

import "testing"

func TestDetect(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		filename string
		want     ContentType
	}{
		{
			name: "json object",
			body: `{"name":"foo","count":3}`,
			want: JSON,
		},
		{
			name: "json array with whitespace",
			body: "  \n [1, 2, 3]  ",
			want: JSON,
		},
		{
			name: "jsonl trumps unknown sniff",
			body: `{"a":1}` + "\n" + `{"b":2}`,
			// Two standalone objects is not strict JSON, but the first line
			// sniffs as json; since json.Valid fails we expect Text. Filename
			// hint promotes it to JSON.
			filename: "events.jsonl",
			want:     JSON,
		},
		{
			name: "plain text with braces falls through",
			body: `{this is not json — it's prose with {curly} decorations and no parse path.`,
			want: Text,
		},
		{
			name:     "code by filename",
			body:     "package main\n\nfunc main() {}\n",
			filename: "main.go",
			want:     Code,
		},
		{
			name:     "code filename overrides sniff",
			body:     "not parseable as anything special",
			filename: "handler.py",
			want:     Code,
		},
		{
			name:     "json sniff overrides code filename",
			body:     `{"error":"E_NOTFOUND"}`,
			filename: "main.go",
			want:     JSON,
		},
		{
			name: "logs by pattern — bracketed timestamp",
			body: "[2025-03-12 11:22:33] starting up\n[2025-03-12 11:22:34] listening on :8080\n[2025-03-12 11:22:35] ready\n",
			want: Logs,
		},
		{
			name: "logs by pattern — ISO timestamp",
			body: "2025-03-12T11:22:33Z INFO started\n2025-03-12T11:22:34Z WARN retry\n2025-03-12T11:22:35Z ERROR gave up\n",
			want: Logs,
		},
		{
			name: "logs by leveled tag",
			body: "[INFO] hello\n[WARN] careful\n[ERROR] oh no\n[INFO] ok\n",
			want: Logs,
		},
		{
			name:     "logs by extension",
			body:     "line one\nline two\n",
			filename: "server.log",
			want:     Logs,
		},
		{
			name: "diff by @@ hunk + --- header",
			body: "--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,3 @@\n-old\n+new\n context\n",
			want: Diff,
		},
		{
			name: "diff by git header",
			body: "diff --git a/x b/x\nindex 111..222 100644\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-foo\n+bar\n",
			want: Diff,
		},
		{
			name: "markdown rule is not diff",
			body: "# Title\n\n---\n\nparagraph\n",
			want: Text,
		},
		{
			name: "html with doctype",
			body: "<!DOCTYPE html>\n<html><body>hi</body></html>",
			want: HTML,
		},
		{
			name: "html without doctype",
			body: "<html>\n<body>\n<p>ok</p>\n</body>\n</html>\n",
			want: HTML,
		},
		{
			name:     "html by extension",
			body:     "<p>just a fragment</p>",
			filename: "snippet.html",
			want:     HTML,
		},
		{
			name:     "markdown filename is text",
			body:     "# readme",
			filename: "README.md",
			want:     Text,
		},
		{
			name: "empty body is unknown",
			body: "",
			want: Unknown,
		},
		{
			name: "whitespace-only body is unknown",
			body: "   \n\n\t  \n",
			want: Unknown,
		},
		{
			name: "plain prose is text",
			body: "Here are my findings. The main issue is that the cache layer does not invalidate properly on writes.",
			want: Text,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect([]byte(tc.body), tc.filename)
			if got != tc.want {
				t.Fatalf("Detect() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDetect_LineNumberedReadOutput pins that Claude Code's Read-tool
// `<num>\t<content>\n` envelope is transparently unwrapped during
// detection. Pre-fix this returned Text or Logs (the line-numbered
// digits looked like log timestamps); post-fix the underlying file
// content drives classification.
func TestDetect_LineNumberedReadOutput(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		filename string
		want     ContentType
	}{
		{
			name:     "line-numbered JSON file",
			body:     "1\t{\n2\t  \"name\": \"express\",\n3\t  \"version\": \"5.2.1\"\n4\t}",
			filename: "package.json",
			want:     JSON,
		},
		{
			name:     "line-numbered Go file (filename hint)",
			body:     "1\tpackage foo\n2\t\n3\tfunc Bar() {}\n",
			filename: "/repo/foo.go",
			want:     Code,
		},
		{
			name:     "line-numbered JSON without filename hint",
			body:     "1\t{\n2\t  \"a\": 1\n3\t}",
			filename: "",
			want:     JSON,
		},
		{
			name:     "non-line-numbered short body unchanged",
			body:     "hello world",
			filename: "",
			want:     Text,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect([]byte(tc.body), tc.filename); got != tc.want {
				t.Errorf("Detect(%q, %q) = %v, want %v", tc.body, tc.filename, got, tc.want)
			}
		})
	}
}

// TestDetect_BashOutputShapes pins that common Bash tool output
// patterns (ls -la, grep, find, wc -l) classify as Logs so they go
// through LogsCompressor's dedupe + head/tail strategy.
func TestDetect_BashOutputShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ContentType
	}{
		{
			name: "ls -la output",
			body: "total 192\ndrwxr-xr-x  6 user group  4096 May  7 11:00 .\ndrwxrwxrwt 33 root root  20480 May  7 12:00 ..\n-rw-r--r--  1 user group   1024 May  7 09:00 file.txt\n-rw-r--r--  1 user group    512 May  7 08:00 other.txt",
			want: Logs,
		},
		{
			name: "grep -n output",
			body: "lib/foo.go:12:func Bar() {\nlib/foo.go:34:  return Baz()\nlib/baz.go:8:func Baz() {\nlib/baz.go:9:  return 42\n}",
			want: Logs,
		},
		{
			name: "find paths",
			body: "./lib/foo.go\n./lib/bar.go\n./lib/baz.go\n./test/foo_test.go",
			want: Logs,
		},
		{
			name: "wc -l output",
			body: "  631 lib/application.js\n   81 lib/express.js\n  527 lib/request.js\n  104 lib/response.js",
			want: Logs,
		},
		{
			name: "single-line echo not classified",
			body: "hello world",
			want: Text,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect([]byte(tc.body), ""); got != tc.want {
				t.Errorf("Detect(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestDetect_DiffShapes pins diff classification across the shapes
// Claude Code actually emits: bare unified diff from `git diff` via
// Bash, line-numbered diff from Read of a .diff/.patch file, and
// `diff --git` headed diffs.
func TestDetect_DiffShapes(t *testing.T) {
	bareDiff := "--- a/foo.go\n+++ b/foo.go\n@@ -1,3 +1,3 @@\n func Foo() {\n-  return 1\n+  return 2\n }"
	gitDiff := "diff --git a/foo.go b/foo.go\nindex abc..def 100644\n--- a/foo.go\n+++ b/foo.go\n@@ -1,3 +1,3 @@\n-old\n+new"
	lineNumberedPatch := "1\t--- a/foo.go\n2\t+++ b/foo.go\n3\t@@ -1,3 +1,3 @@\n4\t func Foo() {\n5\t-  return 1\n6\t+  return 2\n7\t }"

	cases := []struct {
		name     string
		body     string
		filename string
		want     ContentType
	}{
		{name: "bare unified diff (git diff via Bash)", body: bareDiff, want: Diff},
		{name: "diff --git header", body: gitDiff, want: Diff},
		{name: "line-numbered diff via Read", body: lineNumberedPatch, want: Diff},
		{name: "line-numbered .patch via Read with filename hint", body: lineNumberedPatch, filename: "fix.patch", want: Diff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect([]byte(tc.body), tc.filename); got != tc.want {
				t.Errorf("Detect(%q, %q) = %v, want %v", tc.body, tc.filename, got, tc.want)
			}
		})
	}
}

func TestStripLineNumbers(t *testing.T) {
	in := "1\t{\n2\t  \"a\": 1\n3\t}\n"
	want := "{\n  \"a\": 1\n}\n"
	if got := string(StripLineNumbers([]byte(in))); got != want {
		t.Errorf("StripLineNumbers:\n  got: %q\n  want: %q", got, want)
	}
}

func TestIsLineNumbered_TooShort(t *testing.T) {
	// Two-line bodies are below the threshold (3 lines minimum) — guard
	// against mis-detecting "1\tfoo\n2\tbar" as line-numbered when it
	// might just be a tab-indented snippet.
	if IsLineNumbered([]byte("1\tfoo\n2\tbar")) {
		t.Errorf("two-line body should not classify as line-numbered")
	}
}

func TestAllReturnsEveryCompressibleType(t *testing.T) {
	got := All()
	wantSet := map[ContentType]bool{
		JSON: true, Code: true, Logs: true, Text: true, Diff: true, HTML: true,
	}
	if len(got) != len(wantSet) {
		t.Fatalf("All() length = %d, want %d", len(got), len(wantSet))
	}
	for _, ct := range got {
		if !wantSet[ct] {
			t.Errorf("unexpected type in All(): %q", ct)
		}
		delete(wantSet, ct)
	}
	if len(wantSet) > 0 {
		t.Errorf("All() missing types: %v", wantSet)
	}
}
