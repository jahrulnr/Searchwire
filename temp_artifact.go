package searchwire

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// resolveTempDir returns the directory used to materialize fetch artifacts.
// Priority: Config.TempDir > os.TempDir()/searchwire. The returned path is
// not guaranteed to exist; callers should create it (TempDir is idempotent
// here so the first writer also creates it).
func (s *Searcher) resolveTempDir() string {
	if s == nil {
		return filepath.Join(os.TempDir(), tempDirName)
	}
	if dir := strings.TrimSpace(s.tempDir); dir != "" {
		return dir
	}
	return filepath.Join(os.TempDir(), tempDirName)
}

// writeTempArtifact writes content to a file under the configured temp dir
// and returns the absolute path. The file is created with mode 0600 (the
// agent's own process is the only reader; nothing else on the box needs
// the content). The name is deterministic-ish:
//
//	<UTC-yyyymmddTHHMMSSZ>-<sha256-prefix>-<safe-name>.txt
//
// where <safe-name> is a slug derived from sourceURL (max 32 chars; non-
// [a-z0-9-] replaced with -). The sha256 prefix prevents collisions across
// concurrent fetches of identical URLs and gives the agent a stable handle
// when listing the directory.
//
// Errors creating the directory or writing the file are returned to the
// caller; the returned FetchResult simply omits the TempFile field when
// the write fails so the agent still gets the (truncated) text inline.
func (s *Searcher) writeTempArtifact(sourceURL, content string) (string, error) {
	if content == "" {
		return "", nil
	}
	dir := s.resolveTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("searchwire: create temp dir: %w", err)
	}
	sum := sha256.Sum256([]byte(sourceURL + "\x00" + content))
	prefix := hex.EncodeToString(sum[:])[:12]
	name := artifactName(sourceURL)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	path := filepath.Join(dir, fmt.Sprintf("%s-%s-%s.txt", stamp, prefix, name))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("searchwire: write temp artifact: %w", err)
	}
	return path, nil
}

// artifactName derives a safe filesystem slug from a URL. Host and last
// path segment are combined so the agent can recognise the source at a
// glance (e.g. "example-com_docs_llms.txt"). Non-alphanumeric runes are
// collapsed to "-". Empty result returns "artifact".
func artifactName(rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return "artifact"
	}
	// Strip scheme so "https://example.com/docs/" becomes "example.com/docs/".
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	raw = strings.TrimRight(raw, "/")
	if raw == "" {
		return "artifact"
	}
	var b strings.Builder
	prevDash := false
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		return "artifact"
	}
	if len(out) > 48 {
		out = out[:48]
		out = strings.TrimRight(out, "-")
	}
	return strings.ToLower(out)
}
