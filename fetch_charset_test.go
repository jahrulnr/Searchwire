package searchwire

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestFetchHTMLLatin1Decoded: a text/html response declared as ISO-8859-1 must
// be decoded to UTF-8 so non-ASCII bytes (e.g. 0xE9 = é) become readable
// instead of mojibake.
func TestFetchHTMLLatin1Decoded(t *testing.T) {
	// "Café" in ISO-8859-1: 43 61 66 E9
	body := []byte{'<', 'h', 't', 'm', 'l', '>', 'C', 'a', 'f', 0xE9, '<', '/', 'h', 't', 'm', 'l', '>'}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/html; charset=iso-8859-1"}},
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/latin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "Café") {
		t.Fatalf("expected latin-1 decoded to 'Café', got %q", result.Text)
	}
}

// TestFetchPlainTextWindows1252Decoded: text/plain with windows-1252 charset
// decodes the smart quote 0x92 (U+2019) instead of leaving a raw byte.
func TestFetchPlainTextWindows1252Decoded(t *testing.T) {
	body := []byte{'h', 'i', 0x92, ' ', 't', 'h', 'e', 'r', 'e'}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain; charset=windows-1252"}},
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/win")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "’") {
		t.Fatalf("expected windows-1252 smart quote decoded, got %q", result.Text)
	}
}

// TestFetchHTMLMetaCharsetSniffed: when the Content-Type header omits charset
// but the HTML declares <meta charset="iso-8859-1">, the body is still decoded.
func TestFetchHTMLMetaCharsetSniffed(t *testing.T) {
	body := []byte("<html><head><meta charset=\"iso-8859-1\"></head><body>Caf" + string([]byte{0xE9}) + "</body></html>")
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/meta")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "Café") {
		t.Fatalf("expected meta charset sniffed and decoded, got %q", result.Text)
	}
}

// TestFetchJSONUTF8NotDoubleDecoded: application/json; charset=utf-8 must pass
// through unchanged (no double-decode corruption).
func TestFetchJSONUTF8NotDoubleDecoded(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(`{"k":"café"}`)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/u.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "café") {
		t.Fatalf("expected utf-8 json preserved, got %q", result.Text)
	}
}

// TestFetchJSONStringNotMisclassifiedAsHTML: when Content-Type is absent and
// the body is JSON containing "<head>" inside a string value, it must NOT be
// treated as HTML (which would strip tags and lose the JSON structure).
func TestFetchJSONStringNotMisclassifiedAsHTML(t *testing.T) {
	body := `{"desc":"<head>page</head>"}`
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{}, // no Content-Type
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/no-ct")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, `{"desc"`) {
		t.Fatalf("expected JSON preserved (not HTML-parsed), got %q", result.Text)
	}
	if strings.Contains(result.Text, "page") && !strings.Contains(result.Text, "<head>") {
		t.Fatalf("HTML parser ran on JSON and stripped tags: %q", result.Text)
	}
}

// TestFetchHTMLFragmentStillSniffed: a body with no Content-Type that starts
// with <head> (an HTML fragment) is still detected as HTML.
func TestFetchHTMLFragmentStillSniffed(t *testing.T) {
	body := "<head><title>T</title></head><body>hello</body>"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
	result, err := New(Config{HTTPClient: client}).Fetch(context.Background(), "https://example.com/frag")
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "T" {
		t.Fatalf("expected HTML fragment sniffed with title T, got title=%q text=%q", result.Title, result.Text)
	}
}
