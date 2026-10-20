package searchwire

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFetchAutoAttachesLLMsTxtOnHTML2xx verifies the merged-tool contract:
// a successful HTML fetch auto-attaches the parsed covering llms.txt (and
// the post-redirect URL of that file) without the caller having to make
// a second round-trip.
func TestFetchAutoAttachesLLMsTxtOnHTML2xx(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://example.com/docs/page.html":
			return &http.Response{
				StatusCode: 200, Status: "200 OK",
				Header:  http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
				Body:    io.NopCloser(strings.NewReader(`<html><head><title>Doc</title></head><body>main</body></html>`)),
				Request: r,
			}, nil
		case "https://example.com/docs/llms.txt":
			return &http.Response{
				StatusCode: 200, Status: "200 OK",
				Header:  http.Header{"Content-Type": []string{"text/markdown"}},
				Body:    io.NopCloser(strings.NewReader("# Docs\n\n> S.\n\n## G\n\n- [a](https://example.com/a.md)\n")),
				Request: r,
			}, nil
		default:
			return &http.Response{
				StatusCode: 404, Status: "404 Not Found",
				Header:  http.Header{"Content-Type": []string{"text/plain"}},
				Body:    io.NopCloser(strings.NewReader("nope")),
				Request: r,
			}, nil
		}
	})}
	res, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/docs/page.html")
	if err != nil {
		t.Fatal(err)
	}
	if res.LLMsTxt == nil {
		t.Fatalf("expected LLMsTxt populated, got nil")
	}
	if res.LLMsTxt.Title != "Docs" || res.LLMsTxt.Summary != "S." {
		t.Fatalf("LLMsTxt = %#v", res.LLMsTxt)
	}
	if len(res.LLMsTxt.Sections) != 1 || res.LLMsTxt.Sections[0].Links[0].URL != "https://example.com/a.md" {
		t.Fatalf("sections = %#v", res.LLMsTxt.Sections)
	}
	if res.LLMsTxtURL != "https://example.com/docs/llms.txt" {
		t.Fatalf("LLMsTxtURL = %q", res.LLMsTxtURL)
	}
}

// TestFetchSkipsLLMsTxtOnNonHTML verifies the rule that llms.txt only
// applies to HTML responses. JSON, plain text, markdown, CSV, XML etc.
// skip the probe entirely (no extra round-trip).
func TestFetchSkipsLLMsTxtOnNonHTML(t *testing.T) {
	for _, contentType := range []string{
		"application/json",
		"text/plain",
		"text/markdown",
		"text/csv",
		"application/xml",
		"application/rss+xml",
	} {
		t.Run(contentType, func(t *testing.T) {
			var requests int
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				return &http.Response{
					StatusCode: 200, Status: "200 OK",
					Header:  http.Header{"Content-Type": []string{contentType}},
					Body:    io.NopCloser(strings.NewReader("body")),
					Request: r,
				}, nil
			})}
			res, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/x")
			if err != nil {
				t.Fatal(err)
			}
			if res.LLMsTxt != nil {
				t.Fatalf("expected no LLMsTxt for %s, got %#v", contentType, res.LLMsTxt)
			}
			if requests != 1 {
				t.Fatalf("expected exactly 1 request for %s, got %d", contentType, requests)
			}
		})
	}
}

// TestFetchSkipsLLMsTxtOnHTTPError confirms the source-first contract: a
// non-2xx source response skips the llms.txt probe entirely. The original
// HTTPError is surfaced to the caller unchanged.
func TestFetchSkipsLLMsTxtOnHTTPError(t *testing.T) {
	var requests int
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: 500, Status: "500 Internal Server Error",
			Header:  http.Header{"Content-Type": []string{"text/html"}},
			Body:    io.NopCloser(strings.NewReader("boom")),
			Request: r,
		}, nil
	})}
	_, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/oops")
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if requests != 1 {
		t.Fatalf("expected exactly 1 request (no llms.txt probe), got %d", requests)
	}
}

// TestFetchLLMsTxtFallthroughOnSubdir404 mirrors the existing library
// behavior: when <dir>/llms.txt is missing (404) the probe falls through
// to the site root, exactly like Searcher.FetchLLMsTxt does. The embedded
// result reflects whichever candidate answered.
func TestFetchLLMsTxtFallthroughOnSubdir404(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://example.com/docs/page.html":
			return &http.Response{
				StatusCode: 200, Status: "200 OK",
				Header:  http.Header{"Content-Type": []string{"text/html"}},
				Body:    io.NopCloser(strings.NewReader(`<html><head><title>P</title></head><body>main</body></html>`)),
				Request: r,
			}, nil
		case "https://example.com/docs/llms.txt":
			return &http.Response{
				StatusCode: 404, Status: "404 Not Found",
				Header:  http.Header{"Content-Type": []string{"text/plain"}},
				Body:    io.NopCloser(strings.NewReader("")),
				Request: r,
			}, nil
		case "https://example.com/llms.txt":
			return &http.Response{
				StatusCode: 200, Status: "200 OK",
				Header:  http.Header{"Content-Type": []string{"text/markdown"}},
				Body:    io.NopCloser(strings.NewReader("# Root\n")),
				Request: r,
			}, nil
		default:
			return &http.Response{
				StatusCode: 404, Status: "404 Not Found",
				Header:  http.Header{"Content-Type": []string{"text/plain"}},
				Body:    io.NopCloser(strings.NewReader("nope")),
				Request: r,
			}, nil
		}
	})}
	res, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/docs/page.html")
	if err != nil {
		t.Fatal(err)
	}
	if res.LLMsTxt == nil || res.LLMsTxt.Title != "Root" {
		t.Fatalf("expected root llms.txt to win, got %#v", res.LLMsTxt)
	}
	if res.LLMsTxtURL != "https://example.com/llms.txt" {
		t.Fatalf("LLMsTxtURL = %q", res.LLMsTxtURL)
	}
}

// TestFetchWritesTempFileWhenTruncated exercises the temp-dir fallback:
// when the extracted text is truncated, the full inline copy is also
// written to <TempDir>/<stamp>-<sha-prefix>-<slug>.txt so the agent can
// read beyond the budget with file_read.
func TestFetchWritesTempFileWhenTruncated(t *testing.T) {
	dir := t.TempDir()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Header:  http.Header{"Content-Type": []string{"text/plain"}},
			Body:    io.NopCloser(strings.NewReader(strings.Repeat("z", 100))),
			Request: r,
		}, nil
	})}
	res, err := New(Config{HTTPClient: client, TempDir: dir, MaxResponseBytes: 20}).Fetch(context.Background(), "https://example.com/big")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatalf("expected Truncated=true, got %#v", res)
	}
	if res.TempFile == "" {
		t.Fatal("expected TempFile, got empty")
	}
	if !strings.HasPrefix(res.TempFile, dir) {
		t.Fatalf("TempFile %q not under %q", res.TempFile, dir)
	}
	if filepath.Dir(res.TempFile) != dir {
		t.Fatalf("TempFile in wrong dir: %q", res.TempFile)
	}
	body, err := os.ReadFile(res.TempFile)
	if err != nil {
		t.Fatal(err)
	}
	// The artifact holds the FULL extracted text — the contract that lets
	// the agent read beyond the inline budget — not the truncated Text.
	full := strings.Repeat("z", 100)
	if string(body) != full {
		t.Fatalf("temp file should hold the full text: got %d bytes, want %d", len(body), len(full))
	}
	if string(body) == res.Text {
		t.Fatalf("temp file must not mirror the truncated inline text (%d bytes)", len(body))
	}
}

// TestFetchTempDirConfig verifies the Config.TempDir field overrides the
// os.TempDir() default; the directory is auto-created.
func TestFetchTempDirConfig(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "searchwire-artifacts")
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Header:  http.Header{"Content-Type": []string{"text/plain"}},
			Body:    io.NopCloser(strings.NewReader(strings.Repeat("a", 200))),
			Request: r,
		}, nil
	})}
	res, err := New(Config{HTTPClient: client, TempDir: dir, MaxResponseBytes: 10}).Fetch(context.Background(), "https://example.com/big")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.TempFile, dir) {
		t.Fatalf("expected TempFile under %q, got %q", dir, res.TempFile)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("expected dir to be created: %v", err)
	}
}

// TestFetchNoTempFileWhenNotTruncated makes sure the temp file is only
// written on truncation. A short, in-budget response leaves TempFile
// empty so the agent does not accumulate dead artifacts.
func TestFetchNoTempFileWhenNotTruncated(t *testing.T) {
	dir := t.TempDir()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Header:  http.Header{"Content-Type": []string{"text/plain"}},
			Body:    io.NopCloser(strings.NewReader("short body")),
			Request: r,
		}, nil
	})}
	res, err := New(Config{HTTPClient: client, TempDir: dir}).Fetch(context.Background(), "https://example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	if res.TempFile != "" {
		t.Fatalf("expected empty TempFile, got %q", res.TempFile)
	}
	if res.Truncated {
		t.Fatalf("expected Truncated=false, got true")
	}
	// And nothing was written to the dir either.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty dir, got %d entries", len(entries))
	}
}
