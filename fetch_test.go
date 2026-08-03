package searchwire

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFetchHTMLExtractsTitleAndText(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://example.com/page" {
			t.Fatalf("unexpected url %s", r.URL)
		}
		body := `<!doctype html><html><head><title>Hello Page</title>
<style>.x{color:red}</style><script>alert(1)</script></head>
<body><h1>Welcome</h1><p>Readable body text.</p></body></html>`
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}

	s := New(Config{HTTPClient: client})
	result, err := s.Fetch(context.Background(), "https://example.com/page")
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "Hello Page" {
		t.Fatalf("title = %q", result.Title)
	}
	if !strings.Contains(result.Text, "Welcome") || !strings.Contains(result.Text, "Readable body text") {
		t.Fatalf("text = %q", result.Text)
	}
	if strings.Contains(result.Text, "alert") || strings.Contains(result.Text, "color:red") {
		t.Fatalf("script/style leaked into text: %q", result.Text)
	}
	if result.StatusCode != 200 || result.Truncated {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestFetchPlainText(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("  plain   text  ")),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/raw.txt")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "plain text" {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestFetchRejectsBadScheme(t *testing.T) {
	s := New(Config{})
	_, err := s.Fetch(context.Background(), "file:///etc/passwd")
	if !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("err = %v", err)
	}
	_, err = s.Fetch(context.Background(), "")
	if !errors.Is(err, ErrEmptyURL) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchHTTPError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 404,
			Status:     "404 Not Found",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("missing")),
			Request:    r,
		}, nil
	})}
	_, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/missing")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 404 {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchRejectsBinaryContentType(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/pdf"}},
			Body:       io.NopCloser(strings.NewReader("%PDF")),
			Request:    r,
		}, nil
	})}
	_, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/doc.pdf")
	if !errors.Is(err, ErrUnexpectedContentType) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchTruncatesOversize(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("a", 100))),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client, MaxResponseBytes: 50}).Fetch(context.Background(), "https://example.com/big")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.Bytes != 50 {
		t.Fatalf("result = %#v", result)
	}
}

func TestFetchWithLimitOverride(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("b", 40))),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client, MaxResponseBytes: 100}).FetchWithLimit(context.Background(), "https://example.com/x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.Bytes != 10 {
		t.Fatalf("result = %#v", result)
	}
}

func TestFetchNilSearcher(t *testing.T) {
	var s *Searcher
	_, err := s.Fetch(context.Background(), "https://example.com")
	if !errors.Is(err, ErrNilSearcher) {
		t.Fatalf("err = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
