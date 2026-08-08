package searchwire

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseBraveFixture(t *testing.T) {
	results, err := parseBraveResults(readFixture(t, "brave-results.html"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].title != "Go Programming Language" || results[0].url != "https://go.dev/" {
		t.Fatalf("first = %#v", results[0])
	}
}

func TestBraveSearchRequestContract(t *testing.T) {
	var req *http.Request
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		req = r
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(readFixtureString(t, "brave-results.html"))),
		}, nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	if _, err := (braveSource{}).search(context.Background(), f, "Go", 5); err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodGet || !strings.Contains(req.URL.String(), "search.brave.com/search") {
		t.Fatalf("url = %s", req.URL)
	}
	if req.URL.Query().Get("q") != "Go" || req.URL.Query().Get("source") != "web" {
		t.Fatalf("query = %s", req.URL.RawQuery)
	}
	if req.Header.Get("Accept") != "text/html" || !strings.Contains(req.Header.Get("User-Agent"), "searchwire") {
		t.Fatalf("headers = %#v", req.Header)
	}
}

func TestBraveAPISearchRequestContract(t *testing.T) {
	var req *http.Request
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		req = r
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(readFixtureString(t, "brave-api-results.json"))),
		}, nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	results, err := (braveSource{apiKey: "brave_test_key"}).search(context.Background(), f, "Go context", 5)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodGet || req.URL.Host != "api.search.brave.com" || req.URL.Path != "/res/v1/web/search" {
		t.Fatalf("url = %s", req.URL)
	}
	if req.URL.Query().Get("q") != "Go context" || req.URL.Query().Get("count") != "5" {
		t.Fatalf("query = %s", req.URL.RawQuery)
	}
	if req.Header.Get("X-Subscription-Token") != "brave_test_key" {
		t.Fatalf("subscription token = %q", req.Header.Get("X-Subscription-Token"))
	}
	if req.Header.Get("Accept") != "application/json" || !strings.Contains(req.Header.Get("User-Agent"), "searchwire") {
		t.Fatalf("headers = %#v", req.Header)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].title != "Go Programming Language" || results[0].url != "https://go.dev/" {
		t.Fatalf("first = %#v", results[0])
	}
	if results[0].snippet != "Build fast, reliable, and efficient software." || results[0].rank != 1 {
		t.Fatalf("first = %#v", results[0])
	}
	if results[1].rank != 2 {
		t.Fatalf("second = %#v", results[1])
	}
}

func TestBraveAPIRejectsWrongContentType(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(`<html></html>`)),
		}, nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	_, err := (braveSource{apiKey: "brave_test_key"}).search(context.Background(), f, "Go", 5)
	if !errors.Is(err, ErrUnexpectedContentType) {
		t.Fatalf("err = %v", err)
	}
}

func TestBraveRejectsOversizedResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		body := strings.Repeat("a", int(defaultMaxResponseBytes)+10)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	_, err := (braveSource{}).search(context.Background(), f, "Go", 5)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestBraveRejectsWrongContentType(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	_, err := (braveSource{}).search(context.Background(), f, "Go", 5)
	if !errors.Is(err, ErrUnexpectedContentType) {
		t.Fatalf("err = %v", err)
	}
}

func TestBraveHandlesEmptyPage(t *testing.T) {
	results, err := parseBraveResults([]byte("<html><body></body></html>"), 10)
	if err != nil || len(results) != 0 {
		t.Fatalf("results = %#v err = %v", results, err)
	}
}

func readFixtureString(t *testing.T, name string) string {
	t.Helper()
	return string(readFixture(t, name))
}
