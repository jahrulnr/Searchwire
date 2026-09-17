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
	// 2xx HTML responses auto-attach llms.txt metadata (best-effort), so
	// this fixture is intentionally crafted so the probe sees a 404 and
	// stops. The assertion below only inspects the page's own properties.
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://example.com/page":
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
		default:
			return &http.Response{
				StatusCode: 404, Status: "404 Not Found",
				Header:  http.Header{"Content-Type": []string{"text/plain"}},
				Body:    io.NopCloser(strings.NewReader("missing")),
				Request: r,
			}, nil
		}
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
	// Budget now applies to the extracted text, not the raw download. A 100-byte
	// plain-text body with a 50-byte text budget yields 50 bytes of text.
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
	if !result.Truncated || result.Bytes != 50 || len(result.Text) != 50 {
		t.Fatalf("result = %#v, len(text)=%d", result, len(result.Text))
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

func TestFetchCollapsesBlankLineRuns(t *testing.T) {
	// Whitespace text nodes between block elements reach the extracted text
	// as runs of blank lines. Collapse them to a single empty line so the
	// result does not waste tokens on newline spam.
	body := "<!doctype html><html><head><title>Blanky</title></head><body>" +
		"<p>First</p>\n\n\n\n\n<p>Second</p>\n\n\n<p>Third</p></body></html>"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://example.com/page":
			return &http.Response{
				StatusCode: 200,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		default:
			return &http.Response{
				StatusCode: 404, Status: "404 Not Found",
				Header:  http.Header{"Content-Type": []string{"text/plain"}},
				Body:    io.NopCloser(strings.NewReader("missing")),
				Request: r,
			}, nil
		}
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/page")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Text, "\n\n\n") {
		t.Fatalf("blank-line run survived: %q", result.Text)
	}
	for _, want := range []string{"First", "Second", "Third"} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("text lost %q: %q", want, result.Text)
		}
	}
}

func TestFetchCollapsesBlankLineRunsPlainText(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("a\n\n\n\nb")),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/raw.txt")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "a\n\nb" {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestFetchNilSearcher(t *testing.T) {
	var s *Searcher
	_, err := s.Fetch(context.Background(), "https://example.com")
	if !errors.Is(err, ErrNilSearcher) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchBudgetDoesNotConsumeHTMLOverhead(t *testing.T) {
	// Regression for the truncation-ordering bug: a small max_bytes must yield
	// that much readable text, even when the raw HTML is far larger than the budget.
	const payload = "Readable paragraph content that must survive the full-html pass."
	body := "<!doctype html><html><head><title>Big Head</title><style>" + strings.Repeat("x", 2048) +
		"</style></head><body><p>" + strings.Repeat(payload+" ", 40) + "</p></body></html>"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client, MaxResponseBytes: 120}).Fetch(context.Background(), "https://example.com/page")
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "Big Head" {
		t.Fatalf("title = %q", result.Title)
	}
	if !strings.Contains(result.Text, "Readable paragraph content") {
		t.Fatalf("expected readable text, got %q", result.Text)
	}
	if !result.Truncated || len(result.Text) != 120 {
		t.Fatalf("expected 120-byte truncated text, got len=%d truncated=%v", len(result.Text), result.Truncated)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
