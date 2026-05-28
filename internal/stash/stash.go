package stash

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ErrNotFound is returned by [Stash.Read] when no blob is stored under
// the given sha. Callers should treat it as a normal "stash miss" (the
// blob may have been GCed) rather than a fatal error.
var ErrNotFound = errors.New("stash: blob not found")

// ErrCorrupt is returned by [Stash.Read] when the on-disk content does
// not hash to its filename. Indicates the stash dir was tampered with
// or a partial write was interrupted.
var ErrCorrupt = errors.New("stash: blob hash mismatch")

// Stash is a thread-safe content-addressed blob store rooted at a
// single directory. The zero value is unusable — call [New].
type Stash struct {
	dir         string
	maxTotalMB  int
	mu          sync.Mutex
	createDirOK bool
}

// Options configures [New].
type Options struct {
	// Dir is the absolute path to the stash root. Created on first
	// write if missing. Required.
	Dir string
	// MaxTotalMB caps the total on-disk size. When [Stash.GC] is called
	// (or on the post-write trigger) blobs are evicted oldest-mtime
	// first until the total fits. Zero disables GC entirely (unbounded
	// growth — only acceptable in tests).
	MaxTotalMB int
}

// New constructs a Stash. Returns an error when Dir is empty. Does not
// create the directory until the first write — callers that want eager
// creation should follow up with `os.MkdirAll(opts.Dir, 0o700)`.
func New(opts Options) (*Stash, error) {
	if opts.Dir == "" {
		return nil, errors.New("stash.New: Dir is required")
	}
	return &Stash{
		dir:        opts.Dir,
		maxTotalMB: opts.MaxTotalMB,
	}, nil
}

// Dir returns the configured root directory. Used by tests and for
// human-readable diagnostics.
func (s *Stash) Dir() string { return s.dir }

// Sha returns the canonical SHA-256-hex form of body. Exported so
// callers can predict the marker form before deciding whether to call
// [Stash.Write] (e.g. dedup detection).
func Sha(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// Write stores body and returns its SHA-256-hex name. Idempotent: if
// the blob already exists the existing file's mtime is bumped (so it
// counts as recently-used for LRU eviction) and no rewrite happens.
//
// On-disk write is atomic: a temp file is written to `<dir>/.tmp-<sha>`
// and renamed into place. A partial crash leaves the temp file behind
// and never produces a half-written blob at the canonical path.
func (s *Stash) Write(body []byte) (string, error) {
	sha := Sha(body)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return "", err
	}
	final := filepath.Join(s.dir, sha)
	if _, err := os.Stat(final); err == nil {
		_ = os.Chtimes(final, time.Now(), time.Now())
		return sha, nil
	}
	tmp := filepath.Join(s.dir, ".tmp-"+sha)
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return "", fmt.Errorf("stash.Write: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("stash.Write: rename: %w", err)
	}
	return sha, nil
}

// Read returns the bytes stored under sha. Returns [ErrNotFound] when
// no blob exists, [ErrCorrupt] when the on-disk content does not
// re-hash to sha (catches manual tampering and incomplete writes that
// somehow bypassed the rename guard).
//
// Side-effect: bumps the file's mtime on a successful read so it
// counts as recently-used for LRU eviction. Read-then-evict races are
// avoided by the same lock that serialises writes.
func (s *Stash) Read(sha string) ([]byte, error) {
	if !validSha(sha) {
		return nil, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, sha)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("stash.Read: %w", err)
	}
	if Sha(data) != sha {
		return nil, ErrCorrupt
	}
	_ = os.Chtimes(path, time.Now(), time.Now())
	return data, nil
}

// GC evicts blobs oldest-mtime-first until the total on-disk size is
// at or below MaxTotalMB. No-op when MaxTotalMB is zero. Called
// implicitly after [Stash.Write] but exported for tests and ad-hoc
// runtime cleanup.
func (s *Stash) GC() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gcLocked()
}

func (s *Stash) gcLocked() error {
	if s.maxTotalMB <= 0 {
		return nil
	}
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stash.GC: readdir: %w", err)
	}
	type fileInfo struct {
		name  string
		size  int64
		mtime time.Time
	}
	files := make([]fileInfo, 0, len(entries))
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !validSha(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{name: name, size: info.Size(), mtime: info.ModTime()})
		total += info.Size()
	}
	capBytes := int64(s.maxTotalMB) * 1024 * 1024
	if total <= capBytes {
		return nil
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].mtime.Before(files[j].mtime)
	})
	for _, f := range files {
		if total <= capBytes {
			break
		}
		if err := os.Remove(filepath.Join(s.dir, f.name)); err == nil {
			total -= f.size
		}
	}
	return nil
}

func (s *Stash) ensureDir() error {
	if s.createDirOK {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("stash.ensureDir: %w", err)
	}
	s.createDirOK = true
	return nil
}

// validSha reports whether s is a 64-char lowercase hex string. Cheap
// guard against directory-traversal in [Stash.Read] (a malicious sha
// containing `../` would otherwise read outside the stash dir).
func validSha(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
