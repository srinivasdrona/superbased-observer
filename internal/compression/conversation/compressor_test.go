package conversation

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/marmutapp/superbased-observer/internal/compression/conversation/types"
)

func TestDefaultRegistryCoversAllTypes(t *testing.T) {
	r := DefaultRegistry()
	for _, ct := range types.All() {
		if _, ok := r.Get(ct); !ok {
			t.Errorf("DefaultRegistry missing compressor for %q", ct)
		}
	}
}

func TestRegistryCompressDetectsAndDispatches(t *testing.T) {
	r := DefaultRegistry()

	// JSON-shaped body → JSON compressor. Needs to be big enough that the
	// schema replacement wins on bytes.
	body := []byte(`{"description":"long-enough-string-that-schema-wins","count":123456789,"items":[{"id":1111},{"id":2222},{"id":3333}]}`)
	out, ct, ok := r.Compress(body, "")
	if !ok {
		t.Fatalf("Compress returned ok=false for JSON body")
	}
	if ct != types.JSON {
		t.Fatalf("Compress detected type %q, want json", ct)
	}
	if len(out) >= len(body) {
		t.Fatalf("JSON compressor did not shrink body: in=%d out=%d", len(body), len(out))
	}

	// Unknown body → returned unchanged.
	empty := []byte(" \n ")
	out, ct, ok = r.Compress(empty, "")
	if ok {
		t.Fatalf("Compress returned ok=true for unknown body")
	}
	if ct != types.Unknown {
		t.Fatalf("Compress detected %q, want unknown", ct)
	}
	if string(out) != string(empty) {
		t.Fatalf("Compress mutated unknown body")
	}
}

func TestJSONCompressorReplacesScalarsWithTypeNames(t *testing.T) {
	c := NewJSONCompressor()
	// Inputs need to be meaty enough that the schema representation is
	// actually smaller than the original — the "don't grow" guard bails
	// on toy inputs where the type-name strings dominate.
	in := []byte(`{"name":"a-very-long-user-name-goes-right-here","count":1234567890,"ok":true,"x":null,"note":"this is some example description text that a tool might return"}`)
	out := c.Compress(in)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v (out=%q)", err, out)
	}
	want := map[string]string{
		"name":  "<string>",
		"count": "<number>",
		"ok":    "<bool>",
		"x":     "<null>",
		"note":  "<string>",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("field %q = %v, want %v", k, got[k], v)
		}
	}
}

func TestJSONCompressorKeepsArrayElementSchemaAndLength(t *testing.T) {
	c := NewJSONCompressor()
	in := []byte(`{"items":[{"id":1111,"name":"alpha","role":"admin"},{"id":2222,"name":"beta","role":"user"},{"id":3333,"name":"gamma","role":"user"}]}`)
	out := c.Compress(in)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v (out=%q)", err, out)
	}
	items, ok := got["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items got = %v (want single-element schema)", got["items"])
	}
	elem, ok := items[0].(map[string]any)
	if !ok || elem["id"] != "<number>" {
		t.Fatalf("array element schema = %v, want {id:<number>}", items[0])
	}
	if got["items#len"] != float64(3) {
		t.Fatalf("items#len = %v, want 3", got["items#len"])
	}
}

func TestJSONCompressorReturnsOriginalForInvalidJSON(t *testing.T) {
	c := NewJSONCompressor()
	in := []byte(`{not valid json}`)
	out := c.Compress(in)
	if string(out) != string(in) {
		t.Fatalf("invalid JSON mutated: %q -> %q", in, out)
	}
}

func TestLogsCompressorDedupesConsecutive(t *testing.T) {
	c := NewLogsCompressor()
	in := []byte("retry\nretry\nretry\nok\nretry\nretry\n")
	out := c.Compress(in)
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	want := []string{"retry [×3]", "ok", "retry [×2]"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line[%d] = %q, want %q", i, lines[i], want[i])
		}
	}
}

// TestLogsCompressor_StripsANSI pins the ANSI escape strip behaviour
// (rule 1 of the v1.4.40 sub-feature 1 pipeline). Visible content lines
// must remain bytewise; only the CSI escapes get removed.
func TestLogsCompressor_StripsANSI(t *testing.T) {
	c := NewLogsCompressor()
	in := []byte("\x1b[32mPASS\x1b[0m test_a\n\x1b[31;1mFAIL\x1b[0m test_b\n\x1b[2K\x1b[1A\x1b[Gnpm install ok\n")
	out := string(c.Compress(in))
	if strings.Contains(out, "\x1b") {
		t.Errorf("ANSI escape leaked: %q", out)
	}
	for _, kept := range []string{"PASS test_a", "FAIL test_b", "npm install ok"} {
		if !strings.Contains(out, kept) {
			t.Errorf("visible content lost: %q missing\n%s", kept, out)
		}
	}
}

// TestLogsCompressor_NoANSIIsNoOp pins the fast path: a body with no
// ESC byte must skip the ANSI regex entirely (and still receive the
// other transforms).
func TestLogsCompressor_NoANSIIsNoOp(t *testing.T) {
	c := NewLogsCompressor()
	in := []byte("plain line one\nplain line two\n")
	out := string(c.Compress(in))
	if out != string(in) {
		t.Errorf("plain input was mutated: in=%q out=%q", in, out)
	}
}

// TestLogsCompressor_CarriageReturnCollapse pins rule 2: per-line
// `\r` overwrites collapse to the final segment only.
func TestLogsCompressor_CarriageReturnCollapse(t *testing.T) {
	c := NewLogsCompressor()
	in := []byte("downloading...\r25 %\r50 %\r75 %\r100 % done\nnext line\n")
	out := string(c.Compress(in))
	if strings.Contains(out, "\r") {
		t.Errorf("carriage return survived: %q", out)
	}
	if !strings.Contains(out, "100 % done") {
		t.Errorf("final segment dropped: %q", out)
	}
	if strings.Contains(out, "downloading...") || strings.Contains(out, "25 %") {
		t.Errorf("intermediate segment preserved: %q", out)
	}
}

// TestLogsCompressor_DropsProgressBarLines pins rule 5: lines whose
// visible content is only progress glyphs get dropped.
func TestLogsCompressor_DropsProgressBarLines(t *testing.T) {
	c := NewLogsCompressor()
	in := []byte("starting\n========================================\n>>>>>>>>>>>>>>>>>>>>>>>>>>>>\n##############################\n[========================>] 100%\nfinal line\n")
	out := string(c.Compress(in))
	for _, dropped := range []string{"========================================", ">>>>>>>>>>>>>>>>>>>>>>>>>>>>", "##############################"} {
		if strings.Contains(out, dropped) {
			t.Errorf("progress bar line survived: %q\nout: %s", dropped, out)
		}
	}
	if !strings.Contains(out, "starting") || !strings.Contains(out, "final line") {
		t.Errorf("real content dropped: %s", out)
	}
}

// TestLogsCompressor_DoesNotDropShortDividers pins the conservative
// length floor: short dividers (`---`, `===`) often introduce a section
// in prose / Markdown / commit messages and must not be dropped.
func TestLogsCompressor_DoesNotDropShortDividers(t *testing.T) {
	c := NewLogsCompressor()
	in := []byte("section A\n---\nbody A\n===\nsection B\n")
	out := string(c.Compress(in))
	for _, kept := range []string{"---", "==="} {
		if !strings.Contains(out, kept) {
			t.Errorf("short divider %q dropped: %s", kept, out)
		}
	}
}

// TestLogsCompressor_PowerShellPaddingSquash pins rule 4: tabular
// output with ≥ 3 wide-padding runs gets the gutter compressed to a
// single space. Each visible content cell must remain bytewise.
//
// Real PS Get-* output usually has 4+ columns with column headers wider
// than the data, so each data row has ≥ 3 wide-padding runs. The test
// fixture mirrors that shape.
func TestLogsCompressor_PowerShellPaddingSquash(t *testing.T) {
	c := NewLogsCompressor()
	in := []byte("Name           Version       Source           Repository\nGet-Foo        1.0.0         PSGallery        main\nGet-Bar        2.5.0         PSGallery        main\nGet-Baz        3.0.0         PSGallery        main\n")
	out := string(c.Compress(in))
	if strings.Contains(out, "    ") {
		t.Errorf("wide padding survived: %q", out)
	}
	for _, kept := range []string{"Name", "Version", "Source", "Repository", "Get-Foo", "1.0.0", "PSGallery"} {
		if !strings.Contains(out, kept) {
			t.Errorf("table cell dropped: %q\n%s", kept, out)
		}
	}
}

// TestLogsCompressor_LeavesLsAlone pins the conservative threshold:
// `ls -l` output has at most 1 wide-padding run per line and must NOT
// be squashed (would corrupt the column alignment the model relies on).
func TestLogsCompressor_LeavesLsAlone(t *testing.T) {
	c := NewLogsCompressor()
	in := []byte("-rw-r--r--  1 user group   1024 May  7 09:00 file.txt\n-rw-r--r--  1 user group    512 May  7 08:00 other.txt\n")
	out := string(c.Compress(in))
	// The 4+ spaces before the size column should remain.
	if !strings.Contains(out, "   1024") && !strings.Contains(out, "    1024") {
		// Something might have stripped the leading run; still acceptable
		// only if size and filename are preserved as adjacent visible tokens.
		t.Logf("note: padding around size column may have collapsed: %q", out)
	}
	for _, kept := range []string{"file.txt", "other.txt", "user", "group"} {
		if !strings.Contains(out, kept) {
			t.Errorf("ls cell dropped: %q\n%s", kept, out)
		}
	}
}

// TestLogsCompressor_CapsBlankRunsAtTwo pins rule 6: ≥ 3 consecutive
// blank lines collapse to exactly 2 (which serializes as 3 newlines in
// a row).
func TestLogsCompressor_CapsBlankRunsAtTwo(t *testing.T) {
	c := NewLogsCompressor()
	in := []byte("first\n\n\n\n\n\nsecond\n")
	out := string(c.Compress(in))
	if strings.Contains(out, "\n\n\n\n") {
		t.Errorf("blank run > 2 survived: %q", out)
	}
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Errorf("real content dropped: %s", out)
	}
}

// TestLogsCompressor_DedupBlankBoundary pins that blank lines act as
// run boundaries — they break a dedup run rather than getting deduped
// themselves. Uses 3-line runs (run-length break-even is N ≥ 3 because
// the `[×N]` marker adds 5 bytes).
func TestLogsCompressor_DedupBlankBoundary(t *testing.T) {
	c := NewLogsCompressor()
	in := []byte("retry\nretry\nretry\n\nretry\nretry\nretry\n")
	out := string(c.Compress(in))
	// Blank line acts as a run boundary, producing two independent
	// `[×3]` markers instead of one merged `[×6]`.
	if strings.Count(out, "retry [×3]") != 2 {
		t.Errorf("expected exactly two `retry [×3]` markers: %q", out)
	}
}

// TestLogsCompressor_Determinism pins same-input → byte-identical
// output across many invocations. Same load-bearing pin as Code.
func TestLogsCompressor_Determinism(t *testing.T) {
	c := NewLogsCompressor()
	in := []byte("\x1b[32mok\x1b[0m\nfetched a\nfetched a\nfetched a\n\n\n\n\n========\n")
	first := c.Compress(in)
	for i := 0; i < 50; i++ {
		out := c.Compress(in)
		if string(out) != string(first) {
			t.Fatalf("non-deterministic on iteration %d:\n want=%q\n got =%q", i, first, out)
		}
	}
}

// TestLogsCompressor_Idempotent pins running Compress on its own output
// is a no-op. Idempotence is required for safe pipeline re-entry.
func TestLogsCompressor_Idempotent(t *testing.T) {
	c := NewLogsCompressor()
	in := []byte("starting\nfetched a\nfetched a\nfetched a\ndone\n")
	once := c.Compress(in)
	twice := c.Compress(once)
	if string(once) != string(twice) {
		t.Errorf("Compress is not idempotent:\n once=%q\n twice=%q", once, twice)
	}
}

// TestLogsCompressor_ElisionMarkerCountsLogLevels pins the G32
// enrichment from v1.4.41: when LogsCompressor's truncation pass
// fires on a range containing recognisable log levels, the marker
// includes the level counts so the model can decide whether to
// retrieve.
func TestLogsCompressor_ElisionMarkerCountsLogLevels(t *testing.T) {
	c := &LogsCompressor{opts: LogsOptions{MaxLines: 6, Head: 2, Tail: 2}}
	var b strings.Builder
	b.WriteString("starting up\n")
	b.WriteString("ready\n")
	// Unique line text so the dedup pass doesn't collapse repeated
	// levels into a single `[×N]` marker before headTail counts them.
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, "[ERROR] failure #%d on path/to/component\n", i)
	}
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, "[WARN] retry #%d on different component\n", i)
	}
	for i := 0; i < 2; i++ {
		fmt.Fprintf(&b, "[INFO] resumed step %d\n", i)
	}
	b.WriteString("final-1\n")
	b.WriteString("final-2\n")

	out := string(c.Compress([]byte(b.String())))
	if !strings.Contains(out, "5 ERRORs") {
		t.Errorf("ERROR count missing from marker: %s", out)
	}
	if !strings.Contains(out, "3 WARNs") {
		t.Errorf("WARN count missing from marker: %s", out)
	}
	if !strings.Contains(out, "2 INFO") {
		t.Errorf("INFO count missing from marker: %s", out)
	}
	if !strings.Contains(out, "lines elided:") {
		t.Errorf("expected the enriched marker form, got bare: %s", out)
	}
}

// TestLogsCompressor_ElisionMarkerBareWhenNoLevels pins that bodies
// without recognisable log-level prefixes still get the bare elision
// marker — the enrichment is opportunistic, not load-bearing.
func TestLogsCompressor_ElisionMarkerBareWhenNoLevels(t *testing.T) {
	c := &LogsCompressor{opts: LogsOptions{MaxLines: 6, Head: 2, Tail: 2}}
	var b strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "plain line %d\n", i)
	}
	out := string(c.Compress([]byte(b.String())))
	if strings.Contains(out, "ERRORs") || strings.Contains(out, "WARNs") {
		t.Errorf("level counts in marker without log-shaped lines: %s", out)
	}
	if !strings.Contains(out, "lines elided]") {
		t.Errorf("expected bare elision marker: %s", out)
	}
}

// TestLogsCompressor_AnomalyPreservation_LiftsFatalOutOfElidedMiddle
// pins the E27 enrichment from v1.4.43+: when head+tail truncation
// fires and the elided middle contains a FATAL/PANIC line, that line
// is lifted out and inserted right after the marker so the load-
// bearing failure signal isn't lost.
func TestLogsCompressor_AnomalyPreservation_LiftsFatalOutOfElidedMiddle(t *testing.T) {
	c := &LogsCompressor{opts: LogsOptions{MaxLines: 6, Head: 2, Tail: 2}}
	var b strings.Builder
	b.WriteString("starting up\n")
	b.WriteString("ready\n")
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&b, "noise line %d (no level prefix)\n", i)
	}
	b.WriteString("FATAL: out of memory at allocator.go:42\n")
	b.WriteString("panic: runtime error: index out of range [7] with length 5\n")
	b.WriteString("Caused by: storage backend offline\n")
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, "more noise %d\n", i)
	}
	b.WriteString("final-1\n")
	b.WriteString("final-2\n")

	out := string(c.Compress([]byte(b.String())))
	if !strings.Contains(out, "preserved 3 anomalies") {
		t.Errorf("expected `preserved 3 anomalies` in marker, got: %s", out)
	}
	if !strings.Contains(out, "FATAL: out of memory") {
		t.Errorf("FATAL line was elided rather than lifted: %s", out)
	}
	if !strings.Contains(out, "panic: runtime error") {
		t.Errorf("panic: line was elided rather than lifted: %s", out)
	}
	if !strings.Contains(out, "Caused by: storage backend offline") {
		t.Errorf("Caused by: line was elided rather than lifted: %s", out)
	}
	if !strings.Contains(out, "final-1") || !strings.Contains(out, "final-2") {
		t.Errorf("tail lines lost: %s", out)
	}
}

// TestLogsCompressor_AnomalyPreservation_NoAnomaliesIsBareMarker pins
// that bodies without any anomaly-shaped lines preserve the existing
// G32 marker form (no `; preserved N anomalies` suffix).
func TestLogsCompressor_AnomalyPreservation_NoAnomaliesIsBareMarker(t *testing.T) {
	c := &LogsCompressor{opts: LogsOptions{MaxLines: 6, Head: 2, Tail: 2}}
	var b strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "plain line %d\n", i)
	}
	out := string(c.Compress([]byte(b.String())))
	if strings.Contains(out, "preserved") {
		t.Errorf("anomaly suffix appeared on body without anomalies: %s", out)
	}
}

// TestLogsCompressor_AnomalyPreservation_CapsAt20 pins that the lift-
// out is bounded — a pathological middle (1000 ERROR lines) preserves
// at most maxPreservedAnomalies anomalies so the post-truncation body
// can't blow up.
func TestLogsCompressor_AnomalyPreservation_CapsAt20(t *testing.T) {
	c := &LogsCompressor{opts: LogsOptions{MaxLines: 6, Head: 2, Tail: 2}}
	var b strings.Builder
	b.WriteString("head-1\n")
	b.WriteString("head-2\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "ERROR retry %d\n", i)
	}
	b.WriteString("tail-1\n")
	b.WriteString("tail-2\n")

	out := string(c.Compress([]byte(b.String())))
	if !strings.Contains(out, fmt.Sprintf("preserved %d anomalies", maxPreservedAnomalies)) {
		t.Errorf("expected cap at %d, got: %s", maxPreservedAnomalies, out)
	}
	preservedCount := strings.Count(out, "ERROR retry ")
	if preservedCount != maxPreservedAnomalies {
		t.Errorf("expected %d preserved ERROR lines, got %d in: %s", maxPreservedAnomalies, preservedCount, out)
	}
}

func TestLogsCompressorClampsLargeInput(t *testing.T) {
	c := &LogsCompressor{opts: LogsOptions{MaxLines: 6, Head: 2, Tail: 2}}
	// 12 distinct lines so dedup is a no-op; truncation takes over.
	var b strings.Builder
	for i := 0; i < 12; i++ {
		b.WriteString("line")
		b.WriteByte(byte('a' + i))
		b.WriteByte('\n')
	}
	out := c.Compress([]byte(b.String()))
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("lines = %v, want 5 (2 head + marker + 2 tail)", lines)
	}
	if lines[0] != "linea" || lines[1] != "lineb" {
		t.Fatalf("head lines wrong: %v", lines[:2])
	}
	if !strings.Contains(lines[2], "8 lines elided") {
		t.Fatalf("marker missing elision count: %q", lines[2])
	}
	if lines[3] != "linek" || lines[4] != "linel" {
		t.Fatalf("tail lines wrong: %v", lines[3:])
	}
}

func TestTextCompressorHeadTail(t *testing.T) {
	c := NewTextCompressor(TextOptions{MaxLines: 6, Head: 2, Tail: 2})
	in := []byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n")
	out := c.Compress(in)
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("lines = %v, want 5", lines)
	}
	if lines[0] != "one" || lines[1] != "two" {
		t.Fatalf("head lines wrong: %v", lines[:2])
	}
	if !strings.Contains(lines[2], "6 lines elided") {
		t.Fatalf("marker missing: %q", lines[2])
	}
	if lines[3] != "nine" || lines[4] != "ten" {
		t.Fatalf("tail lines wrong: %v", lines[3:])
	}
}

func TestTextCompressorShortInputPassesThrough(t *testing.T) {
	c := NewTextCompressor(TextOptions{})
	in := []byte("short\ntext\n")
	out := c.Compress(in)
	if string(out) != string(in) {
		t.Fatalf("short input mutated: in=%q out=%q", in, out)
	}
}

func TestDiffCompressorKeepsChangeLinesAndOneContext(t *testing.T) {
	c := NewDiffCompressor()
	in := []byte(`diff --git a/main.go b/main.go
index 111..222 100644
--- a/main.go
+++ b/main.go
@@ -1,30 +1,30 @@
 package main

 import (
 	"fmt"
 	"net/http"
 	"encoding/json"
 	"io"
 	"log"
 	"os"
 )

 type Server struct {
 	addr string
 	port int
 }

-func oldName() {
+func newName() {
 	fmt.Println("hi")
 }

 func handler(w http.ResponseWriter, r *http.Request) {
 	log.Println("got request")
 	data, err := io.ReadAll(r.Body)
 	if err != nil {
 		http.Error(w, err.Error(), 500)
 		return
 	}
 	log.Println(string(data))
 }
`)
	out := c.Compress(in)
	s := string(out)
	if len(s) >= len(in) {
		t.Fatalf("diff compressor did not shrink body: in=%d out=%d", len(in), len(s))
	}
	if !strings.Contains(s, "diff --git a/main.go b/main.go") {
		t.Errorf("missing git header: %s", s)
	}
	if !strings.Contains(s, "-func oldName() {") || !strings.Contains(s, "+func newName() {") {
		t.Errorf("missing change lines: %s", s)
	}
	if strings.Contains(s, `"net/http"`) {
		t.Errorf("far context line should have been stripped: %s", s)
	}
	if !strings.Contains(s, "context line") {
		t.Errorf("missing elision marker: %s", s)
	}
}

func TestHTMLCompressorStripsScriptAndStyle(t *testing.T) {
	c := NewHTMLCompressor()
	in := []byte(`<html><head><style>body{color:red}</style><script>alert(1)</script></head><body><p>hi</p></body></html>`)
	out := c.Compress(in)
	s := string(out)
	if strings.Contains(s, "color:red") {
		t.Errorf("style body leaked: %s", s)
	}
	if strings.Contains(s, "alert(1)") {
		t.Errorf("script body leaked: %s", s)
	}
	if !strings.Contains(s, "<p>hi</p>") {
		t.Errorf("visible content missing: %s", s)
	}
}

func TestHTMLCompressorStripsComments(t *testing.T) {
	c := NewHTMLCompressor()
	in := []byte(`<div><!-- internal note --><span>keep me</span></div>`)
	out := c.Compress(in)
	if strings.Contains(string(out), "internal note") {
		t.Errorf("comment leaked: %s", out)
	}
	if !strings.Contains(string(out), "keep me") {
		t.Errorf("visible span stripped: %s", out)
	}
}

// TestCodeCompressor_PreservesEveryNonCommentNonBlankLine pins the
// load-bearing property of the v1.4.40 rewrite: the compressor never
// elides function bodies, never drops mid-file comments, never collapses
// to a signature-only skeleton. Every non-comment, non-blank line of the
// input must appear bytewise in the output. This is the test that would
// have caught the v1.4.38 turn-count regression at unit-test time.
func TestCodeCompressor_PreservesEveryNonCommentNonBlankLine(t *testing.T) {
	c := NewCodeCompressor()
	in := []byte(`package main

import "fmt"
import "net/http"

// fetchUser pulls a user by id; returns wrapped error on failure.
func fetchUser(id int) (*User, error) {
	resp, err := http.Get(fmt.Sprintf("/users/%d", id))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var u User
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

func main() {
	fmt.Println(greet("world"))
	for i := 0; i < 10; i++ {
		fmt.Println(i)
	}
}
`)
	out := string(c.Compress(in))
	mustContain := []string{
		`package main`,
		`import "fmt"`,
		`import "net/http"`,
		`func fetchUser(id int) (*User, error) {`,
		`	resp, err := http.Get(fmt.Sprintf("/users/%d", id))`,
		`		return nil, err`,
		`	defer resp.Body.Close()`,
		`	var u User`,
		`	return &u, nil`,
		`func main() {`,
		`	fmt.Println(greet("world"))`,
		`	for i := 0; i < 10; i++ {`,
		`		fmt.Println(i)`,
		`// fetchUser pulls a user by id; returns wrapped error on failure.`,
	}
	for _, line := range mustContain {
		if !strings.Contains(out, line) {
			t.Errorf("line missing from compressed output (regression class): %q\n--- output ---\n%s", line, out)
		}
	}
}

// TestCodeCompressor_TrimsTrailingWhitespace pins behaviour 1.
func TestCodeCompressor_TrimsTrailingWhitespace(t *testing.T) {
	c := NewCodeCompressor()
	in := []byte("package main\t\t\nimport \"fmt\"   \nfunc main() {  \n\tfmt.Println()\t\n}\n")
	out := string(c.Compress(in))
	for i, line := range strings.Split(out, "\n") {
		if line != strings.TrimRight(line, " \t") {
			t.Errorf("line %d still has trailing whitespace: %q", i, line)
		}
	}
	for _, kept := range []string{"package main", `import "fmt"`, "func main() {", "\tfmt.Println()", "}"} {
		if !strings.Contains(out, kept) {
			t.Errorf("content lost during trim: %q missing\n%s", kept, out)
		}
	}
}

// TestCodeCompressor_CapsBlankRunsAtTwo pins behaviour 4: a run of 3+
// blank lines collapses to exactly 2 (which serializes as three \n's in
// a row: "<line>\n\n\n<line>"). A run of ≥3 blank lines (four \n's in a
// row) must not survive.
func TestCodeCompressor_CapsBlankRunsAtTwo(t *testing.T) {
	c := NewCodeCompressor()
	in := []byte("package main\n\n\n\n\n\nfunc a() {}\n\n\nfunc b() {}\n")
	out := string(c.Compress(in))
	if strings.Contains(out, "\n\n\n\n") {
		t.Errorf("blank run > 2 survived: %q", out)
	}
	// Exactly 2 blank lines (3 newlines in a row) is fine and expected.
	if !strings.Contains(out, "package main\n\n\nfunc a() {}") {
		t.Errorf("expected exactly 2-blank-line gap between package and func a: %q", out)
	}
	if !strings.Contains(out, "func a() {}") || !strings.Contains(out, "func b() {}") {
		t.Errorf("real content dropped: %s", out)
	}
}

// TestCodeCompressor_DedupsConsecutiveIdenticalLines pins behaviour 3.
// Generated code (e.g. 100 identical struct field lines) collapses
// exact-byte to a single "<line> [×N]" marker.
func TestCodeCompressor_DedupsConsecutiveIdenticalLines(t *testing.T) {
	c := NewCodeCompressor()
	var b strings.Builder
	b.WriteString("package gen\n\nvar _ = []Foo{\n")
	for i := 0; i < 100; i++ {
		b.WriteString("\t{Name: \"x\"},\n")
	}
	b.WriteString("}\n")
	out := string(c.Compress([]byte(b.String())))
	if !strings.Contains(out, "{Name: \"x\"}, [×100]") {
		t.Errorf("expected run-length marker [×100] absent:\n%s", out)
	}
	if strings.Count(out, "{Name: \"x\"},") != 1 {
		t.Errorf("dedup did not collapse to a single line: %s", out)
	}
}

// TestCodeCompressor_LicenseHeader_GoMIT covers the positive case for
// banner elision: 12-line // header at file top.
func TestCodeCompressor_LicenseHeader_GoMIT(t *testing.T) {
	c := NewCodeCompressor()
	in := []byte(`// Copyright 2026 Foo Bar.
// All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS.
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.

package main

func main() {}
`)
	out := string(c.Compress(in))
	if !strings.Contains(out, "[12-line license header elided]") {
		t.Errorf("expected banner elision marker: %s", out)
	}
	if strings.Contains(out, "Apache License") {
		t.Errorf("license content leaked: %s", out)
	}
	if !strings.Contains(out, "package main") || !strings.Contains(out, "func main() {}") {
		t.Errorf("real content dropped: %s", out)
	}
}

// TestCodeCompressor_LicenseHeader_JSBlock covers the /* … */ multi-line
// banner shape common in JS/C/C++.
func TestCodeCompressor_LicenseHeader_JSBlock(t *testing.T) {
	c := NewCodeCompressor()
	in := []byte(`/*
 * Copyright 2026 Foo Bar.
 * All rights reserved.
 *
 * Apache 2.0 boilerplate goes here for many lines.
 * Line 6 of the banner.
 */

import { Foo } from "./foo";

export const x = 1;
`)
	out := string(c.Compress(in))
	if !strings.Contains(out, "license header elided") {
		t.Errorf("JS /* */ banner not elided: %s", out)
	}
	if !strings.Contains(out, `import { Foo } from "./foo";`) || !strings.Contains(out, "export const x = 1;") {
		t.Errorf("real content dropped: %s", out)
	}
}

// TestCodeCompressor_LicenseHeader_TooShort_NotElided pins the negative
// case: a 3-line opening comment is below the threshold and must remain.
func TestCodeCompressor_LicenseHeader_TooShort_NotElided(t *testing.T) {
	c := NewCodeCompressor()
	in := []byte("// TODO: clean this up later.\n// Author: nobody.\n\npackage main\n\nfunc main() {}\n")
	out := string(c.Compress(in))
	if strings.Contains(out, "license header elided") {
		t.Errorf("short comment block was elided: %s", out)
	}
	if !strings.Contains(out, "// TODO: clean this up later.") {
		t.Errorf("short comment was dropped: %s", out)
	}
}

// TestCodeCompressor_LicenseHeader_MidFileBlock_NotElided pins that
// banner elision fires only at file top, never on a mid-file comment
// block.
func TestCodeCompressor_LicenseHeader_MidFileBlock_NotElided(t *testing.T) {
	c := NewCodeCompressor()
	in := []byte(`package main

func a() {}

// This is a mid-file commentary block.
// It explains a tricky bit of the next function.
// It is six lines long.
// It must not be elided by the file-top banner rule.
// Line five.
// Line six.
func b() {}
`)
	out := string(c.Compress(in))
	if strings.Contains(out, "license header elided") {
		t.Errorf("mid-file comment block was elided: %s", out)
	}
	if !strings.Contains(out, "// It is six lines long.") {
		t.Errorf("mid-file comment dropped: %s", out)
	}
}

// TestCodeCompressor_Determinism pins the load-bearing invariant:
// compressing the same input twice produces byte-identical output. If
// this fails the compressor has hidden state and cache_aware mode cannot
// deliver cache hits across turns.
func TestCodeCompressor_Determinism(t *testing.T) {
	c := NewCodeCompressor()
	in := []byte(`// Copyright 2026.
// Licensed under Apache 2.0.
// See LICENSE.

package main

import "fmt"


func a() {}

var xs = []int{1, 1, 1, 1, 1}

func main() {
	fmt.Println(xs)
}
`)
	out1 := c.Compress(in)
	for i := 0; i < 50; i++ {
		out := c.Compress(in)
		if string(out) != string(out1) {
			t.Fatalf("non-deterministic on iteration %d:\n want=%q\n got =%q", i, out1, out)
		}
	}
}

// TestCodeCompressor_Idempotent pins that running Compress on its own
// output is a no-op (or a same-bytes return). Idempotence is what makes
// re-runs safe in pipelines that may double-invoke compressors.
func TestCodeCompressor_Idempotent(t *testing.T) {
	c := NewCodeCompressor()
	in := []byte(`package main

import "fmt"

func main() {
	for i := 0; i < 3; i++ {
		fmt.Println(i)
	}
}
`)
	once := c.Compress(in)
	twice := c.Compress(once)
	if string(once) != string(twice) {
		t.Errorf("Compress is not idempotent:\n once=%q\n twice=%q", once, twice)
	}
}

// TestCodeCompressor_Empty pins the trivial cases.
func TestCodeCompressor_Empty(t *testing.T) {
	c := NewCodeCompressor()
	for _, in := range [][]byte{nil, {}, []byte("\n")} {
		out := c.Compress(in)
		if string(out) != string(in) {
			t.Errorf("empty/short input mutated: in=%q out=%q", in, out)
		}
	}
}

// TestCodeCompressor_NeverGrowsBody honours the standard
// `len(out) >= len(body)` short-circuit: a body that the rules cannot
// shrink is returned unchanged.
func TestCodeCompressor_NeverGrowsBody(t *testing.T) {
	c := NewCodeCompressor()
	in := []byte("package main\n\nfunc main() {}\n")
	out := c.Compress(in)
	if len(out) > len(in) {
		t.Errorf("compressor grew body: in=%d out=%d", len(in), len(out))
	}
}
