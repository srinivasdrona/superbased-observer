package stash

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStash_WriteRoundTrip(t *testing.T) {
	t.Parallel()
	s, err := New(Options{Dir: t.TempDir(), MaxTotalMB: 0})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := []byte("hello stash")
	sha, err := s.Write(body)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if sha != Sha(body) {
		t.Errorf("sha mismatch: %q vs %q", sha, Sha(body))
	}
	got, err := s.Read(sha)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("round-trip body mismatch: got %q want %q", got, body)
	}
}

func TestStash_WriteIsContentAddressed(t *testing.T) {
	t.Parallel()
	s, _ := New(Options{Dir: t.TempDir()})
	body := []byte("identical content from two callers")

	first, err := s.Write(body)
	if err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	second, err := s.Write(body)
	if err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	if first != second {
		t.Errorf("identical bodies produced different shas: %q vs %q", first, second)
	}

	entries, _ := os.ReadDir(s.Dir())
	count := 0
	for _, e := range entries {
		if !e.IsDir() && validSha(e.Name()) {
			count++
		}
	}
	if count != 1 {
		t.Errorf("identical-body dedup failed: want 1 file on disk, got %d", count)
	}
}

func TestStash_ReadMissing(t *testing.T) {
	t.Parallel()
	s, _ := New(Options{Dir: t.TempDir()})
	_, err := s.Read("0000000000000000000000000000000000000000000000000000000000000000")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("missing-blob: got %v, want ErrNotFound", err)
	}
}

func TestStash_ReadInvalidSha(t *testing.T) {
	t.Parallel()
	s, _ := New(Options{Dir: t.TempDir()})
	for _, bad := range []string{"", "short", "../etc/passwd", "ABCDEF", "not-hex"} {
		if _, err := s.Read(bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("invalid sha %q: got %v, want ErrNotFound", bad, err)
		}
	}
}

func TestStash_ReadCorrupt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, _ := New(Options{Dir: dir})
	body := []byte("real content")
	sha, _ := s.Write(body)
	// Tamper with the file on disk.
	if err := os.WriteFile(filepath.Join(dir, sha), []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper write: %v", err)
	}
	_, err := s.Read(sha)
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("corrupt-blob: got %v, want ErrCorrupt", err)
	}
}

func TestStash_GC_LRU(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// 1MB cap; each blob is ~512KB so two fit, three don't.
	s, _ := New(Options{Dir: dir, MaxTotalMB: 1})
	half := make([]byte, 512*1024)
	for i := range half {
		half[i] = 'A'
	}
	first, _ := s.Write(half)
	// nudge mtimes so the ordering is reliable on filesystems with
	// 1-second mtime granularity (Linux ext4 default since 4.0).
	if err := os.Chtimes(filepath.Join(dir, first), time.Now().Add(-3*time.Second), time.Now().Add(-3*time.Second)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	half2 := make([]byte, 512*1024)
	for i := range half2 {
		half2[i] = 'B'
	}
	second, _ := s.Write(half2)
	if err := os.Chtimes(filepath.Join(dir, second), time.Now().Add(-2*time.Second), time.Now().Add(-2*time.Second)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Push us over cap.
	half3 := make([]byte, 512*1024)
	for i := range half3 {
		half3[i] = 'C'
	}
	if _, err := s.Write(half3); err != nil {
		t.Fatalf("Write 3: %v", err)
	}
	if err := s.GC(); err != nil {
		t.Fatalf("GC: %v", err)
	}

	if _, err := s.Read(first); !errors.Is(err, ErrNotFound) {
		t.Errorf("oldest blob should have been evicted, got %v", err)
	}
	if _, err := s.Read(Sha(half3)); err != nil {
		t.Errorf("newest blob should have survived, got %v", err)
	}
}

func TestStash_GC_NoOpWhenUnderCap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, _ := New(Options{Dir: dir, MaxTotalMB: 100}) // 100MB cap, well over the few KB we'll write
	for _, b := range [][]byte{[]byte("a"), []byte("b"), []byte("c")} {
		if _, err := s.Write(b); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := s.GC(); err != nil {
		t.Fatalf("GC: %v", err)
	}
	for _, b := range [][]byte{[]byte("a"), []byte("b"), []byte("c")} {
		if _, err := s.Read(Sha(b)); err != nil {
			t.Errorf("blob %q evicted unexpectedly: %v", b, err)
		}
	}
}

func TestStash_NewRequiresDir(t *testing.T) {
	t.Parallel()
	if _, err := New(Options{}); err == nil {
		t.Error("New with empty Dir should error")
	}
}

func TestSha_Deterministic(t *testing.T) {
	t.Parallel()
	body := []byte("deterministic check")
	first := Sha(body)
	for i := 0; i < 100; i++ {
		if got := Sha(body); got != first {
			t.Fatalf("Sha non-deterministic on iteration %d: got %q want %q", i, got, first)
		}
	}
}

func TestStash_WriteCreatesDirOnFirstCall(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dir := filepath.Join(parent, "stash") // intentionally missing
	s, _ := New(Options{Dir: dir})
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dir should not exist before first write: %v", err)
	}
	if _, err := s.Write([]byte("first")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir not created on first write: %v", err)
	}
}
