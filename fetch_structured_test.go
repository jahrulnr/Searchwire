package searchwire

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestFetchJSONPrettyPreserved verifies that a compact JSON body is returned
// pretty-printed so the agent can read structure, and that newlines are
// preserved (not collapsed by collapseWhitespace).
func TestFetchJSONPrettyPreserved(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"name":"searchwire","stars":42,"tags":["go","search"]}`
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/data.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "\"name\":") {
		t.Fatalf("expected pretty-printed JSON keys, got %q", result.Text)
	}
	if !strings.Contains(result.Text, "\n") {
		t.Fatalf("expected newlines preserved in JSON, got %q", result.Text)
	}
}

// TestFetchJSONAlreadyPretty keeps an already-indented JSON body as-is.
func TestFetchJSONAlreadyPretty(t *testing.T) {
	pretty := `{
  "a": 1,
  "b": 2
}`
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(pretty)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/p.json")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != pretty {
		t.Fatalf("expected pretty JSON preserved verbatim, got %q", result.Text)
	}
}

// TestFetchJSONInvalidReturnsRaw: invalid JSON must not error; return the raw
// body so the agent still sees something instead of a hard failure.
func TestFetchJSONInvalidReturnsRaw(t *testing.T) {
	raw := `{not valid json`
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(raw)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/bad.json")
	if err != nil {
		t.Fatalf("invalid JSON should not error, got %v", err)
	}
	if result.Text != raw {
		t.Fatalf("expected raw body for invalid JSON, got %q", result.Text)
	}
}

// TestFetchJSONVendorSuffix allows +json vendor content types.
func TestFetchJSONVendorSuffix(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/vnd.api+json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/api")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "\"ok\"") {
		t.Fatalf("expected +json allowed and pretty-printed, got %q", result.Text)
	}
}

// TestFetchXMLPreservesText: application/xml returns tag-stripped text with
// newlines preserved between elements.
func TestFetchXMLPreservesText(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `<?xml version="1.0"?><feed><title>My Feed</title><entry><title>One</title></entry></feed>`
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "My Feed") || !strings.Contains(result.Text, "One") {
		t.Fatalf("expected XML text content, got %q", result.Text)
	}
	if strings.Contains(result.Text, "<title>") {
		t.Fatalf("expected tags stripped, got %q", result.Text)
	}
}

// TestFetchRSSAllowed: application/rss+xml is accepted.
func TestFetchRSSAllowed(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `<?xml version="1.0"?><rss><channel><title>RSS</title></channel></rss>`
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/rss+xml"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/rss")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "RSS") {
		t.Fatalf("expected RSS text, got %q", result.Text)
	}
}

// TestFetchAtomAllowed: application/atom+xml is accepted.
func TestFetchAtomAllowed(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Atom</title></feed>`
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/atom+xml"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/atom")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "Atom") {
		t.Fatalf("expected Atom text, got %q", result.Text)
	}
}

// TestFetchMarkdownPreservesNewlines: text/markdown keeps line breaks.
func TestFetchMarkdownPreservesNewlines(t *testing.T) {
	md := "# Title\n\nSome paragraph.\n\n- item one\n- item two\n"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/markdown"}},
			Body:       io.NopCloser(strings.NewReader(md)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/readme.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "# Title") {
		t.Fatalf("expected markdown heading preserved, got %q", result.Text)
	}
	if !strings.Contains(result.Text, "- item one\n- item two") {
		t.Fatalf("expected markdown list newlines preserved, got %q", result.Text)
	}
}

// TestFetchCSVPreservesNewlines: text/csv keeps row breaks.
func TestFetchCSVPreservesNewlines(t *testing.T) {
	csv := "a,b,c\n1,2,3\n4,5,6\n"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/csv"}},
			Body:       io.NopCloser(strings.NewReader(csv)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/data.csv")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "a,b,c\n1,2,3") {
		t.Fatalf("expected CSV rows preserved, got %q", result.Text)
	}
}

// TestFetchPlainTextPreservesNewlines: text/plain keeps line breaks while
// collapsing runs of spaces/tabs.
func TestFetchPlainTextPreservesNewlines(t *testing.T) {
	raw := "line one\nline   two\n\tindented\n"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(raw)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/log.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "line one\nline two") {
		t.Fatalf("expected newlines preserved and inline spaces collapsed, got %q", result.Text)
	}
}

// TestFetchAcceptHeaderIncludesJSON verifies the Accept header advertises
// JSON and XML so content-negotiating servers return structured responses.
func TestFetchAcceptHeaderIncludesJSON(t *testing.T) {
	var seenAccept string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seenAccept = r.Header.Get("Accept")
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    r,
		}, nil
	})}
	if _, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/x"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"application/json", "application/xml", "application/rss+xml", "application/atom+xml", "text/markdown", "text/csv"} {
		if !strings.Contains(seenAccept, want) {
			t.Fatalf("Accept header missing %q: %q", want, seenAccept)
		}
	}
}
