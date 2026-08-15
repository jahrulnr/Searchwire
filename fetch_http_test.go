package searchwire

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestFetch429SurfacesRetryAfter: a 429 with Retry-After header must surface
// the parsed seconds on HTTPError.RetryAfter so the agent can back off.
func TestFetch429SurfacesRetryAfter(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 429,
			Status:     "429 Too Many Requests",
			Header: http.Header{
				"Content-Type": []string{"text/plain"},
				"Retry-After":  []string{"30"},
			},
			Body:    io.NopCloser(strings.NewReader("slow down")),
			Request: r,
		}, nil
	})}
	_, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/x")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %v", err)
	}
	if httpErr.RetryAfter != 30 {
		t.Fatalf("expected RetryAfter=30, got %d", httpErr.RetryAfter)
	}
}

// TestFetch429NoRetryAfterZero: without Retry-After, the field is 0 (agent
// knows there is no server-provided backoff hint).
func TestFetch429NoRetryAfterZero(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 429, Status: "429 Too Many Requests",
			Header:  http.Header{"Content-Type": []string{"text/plain"}},
			Body:    io.NopCloser(strings.NewReader("no header")),
			Request: r,
		}, nil
	})}
	_, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/x")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.RetryAfter != 0 {
		t.Fatalf("expected RetryAfter=0, got %v", err)
	}
}

// TestFetchErrorBodyParsedJSON: a JSON error envelope is parsed into
// HTTPError.ErrorBody so the agent can read structured error fields.
func TestFetchErrorBodyParsedJSON(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 400, Status: "400 Bad Request",
			Header:  http.Header{"Content-Type": []string{"application/json"}},
			Body:    io.NopCloser(strings.NewReader(`{"error":"invalid_query","code":42}`)),
			Request: r,
		}, nil
	})}
	_, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/x")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %v", err)
	}
	if httpErr.ErrorBody == nil || httpErr.ErrorBody["error"] != "invalid_query" {
		t.Fatalf("expected ErrorBody parsed, got %#v", httpErr.ErrorBody)
	}
}

// TestFetchSurfacesResponseHeaders: ETag and Last-Modified are surfaced in
// result.Headers so the agent can do conditional GETs later.
func TestFetchSurfacesResponseHeaders(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Header: http.Header{
				"Content-Type":  []string{"text/plain"},
				"ETag":          []string{`"abc123"`},
				"Last-Modified": []string{"Wed, 21 Oct 2026 07:28:00 GMT"},
			},
			Body:    io.NopCloser(strings.NewReader("ok")),
			Request: r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	if result.Headers["ETag"] != `"abc123"` {
		t.Fatalf("ETag not surfaced: %#v", result.Headers)
	}
	if result.Headers["Last-Modified"] == "" {
		t.Fatalf("Last-Modified not surfaced: %#v", result.Headers)
	}
}

// TestFetchConditionalGET304: when the caller provides If-None-Match /
// If-Modified-Since via FetchOptions and the server returns 304, the fetch
// returns a FetchResult with StatusCode 304 and empty text (no body).
func TestFetchConditionalGET304(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("If-None-Match") != `"abc123"` {
			t.Fatalf("If-None-Match not sent: %q", r.Header.Get("If-None-Match"))
		}
		return &http.Response{
			StatusCode: 304, Status: "304 Not Modified",
			Header:  http.Header{"ETag": []string{`"abc123"`}},
			Body:    io.NopCloser(strings.NewReader("")),
			Request: r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).FetchWithOptions(context.Background(), "https://example.com/x", FetchOptions{
		IfNoneMatch: `"abc123"`,
	})
	if err != nil {
		t.Fatalf("304 should not error, got %v", err)
	}
	if result.StatusCode != 304 || result.Text != "" {
		t.Fatalf("expected 304 empty body, got %#v", result)
	}
}

// TestFetchRangeRequest: when FetchOptions.Range is set, a Range header is
// sent and the partial body is returned.
func TestFetchRangeRequest(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Range") != "bytes=10-20" {
			t.Fatalf("Range header not sent: %q", r.Header.Get("Range"))
		}
		return &http.Response{
			StatusCode: 206, Status: "206 Partial Content",
			Header: http.Header{
				"Content-Type":  []string{"text/plain"},
				"Content-Range": []string{"bytes 10-20/100"},
			},
			Body:    io.NopCloser(strings.NewReader("partial")),
			Request: r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).FetchWithOptions(context.Background(), "https://example.com/big.txt", FetchOptions{
		Range: "bytes=10-20",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != 206 || result.Text != "partial" {
		t.Fatalf("expected 206 partial, got %#v", result)
	}
}

// TestFetchRedirectCountRecorded: redirects followed by the http client are
// reflected in result.Redirects (via resp.Response / chain length).
func TestFetchRedirectCountRecorded(t *testing.T) {
	// Use a client that does NOT follow redirects automatically so we can
	// count manually via a custom CheckRedirect.
	var followed int
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/start") {
				return &http.Response{
					StatusCode: 302, Status: "302 Found",
					Header: http.Header{
						"Content-Type": []string{"text/plain"},
						"Location":     []string{"https://example.com/mid"},
					},
					Body:    io.NopCloser(strings.NewReader("")),
					Request: r,
				}, nil
			}
			if strings.HasSuffix(r.URL.Path, "/mid") {
				return &http.Response{
					StatusCode: 302, Status: "302 Found",
					Header: http.Header{
						"Content-Type": []string{"text/plain"},
						"Location":     []string{"https://example.com/final"},
					},
					Body:    io.NopCloser(strings.NewReader("")),
					Request: r,
				}, nil
			}
			return &http.Response{
				StatusCode: 200, Status: "200 OK",
				Header:  http.Header{"Content-Type": []string{"text/plain"}},
				Body:    io.NopCloser(strings.NewReader("final body")),
				Request: r,
			}, nil
		}),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			followed = len(via)
			return nil
		},
	}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/start")
	if err != nil {
		t.Fatal(err)
	}
	if result.Redirects != followed || followed != 2 {
		t.Fatalf("expected Redirects=2 (followed=%d), got %d", followed, result.Redirects)
	}
	if result.FinalURL != "https://example.com/final" {
		t.Fatalf("expected final url, got %q", result.FinalURL)
	}
}

// TestFetchTruncationMarker: a truncated body ends with a visible marker so
// the agent knows the text was cut mid-stream (not just Truncated flag).
func TestFetchTruncationMarker(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Header:  http.Header{"Content-Type": []string{"text/plain"}},
			Body:    io.NopCloser(strings.NewReader(strings.Repeat("a", 100))),
			Request: r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client, MaxResponseBytes: 50}).Fetch(context.Background(), "https://example.com/big")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Fatalf("expected Truncated=true")
	}
	if !strings.HasSuffix(result.Text, "\n[truncated]") {
		t.Fatalf("expected truncation marker at end, got %q", result.Text)
	}
}
