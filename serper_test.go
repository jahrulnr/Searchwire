package searchwire

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseSerperResults(t *testing.T) {
	results, err := parseSerperResults(readFixture(t, "serper-results.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].title != "Go Programming Language" || results[0].url != "https://go.dev/" {
		t.Fatalf("first = %#v", results[0])
	}
	if results[0].snippet != "The Go programming language." || results[0].rank != 1 {
		t.Fatalf("first = %#v", results[0])
	}
	if results[2].rank != 3 {
		t.Fatalf("third = %#v", results[2])
	}
}

func TestSerperSearchRequestContract(t *testing.T) {
	var req *http.Request
	var reqBody []byte
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		req = r
		if r.Body != nil {
			reqBody, _ = io.ReadAll(r.Body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(readFixtureString(t, "serper-results.json"))),
		}, nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	results, err := (serperSource{apiKey: "serper_test_key"}).search(context.Background(), f, "Go context", 5)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPost || req.URL.String() != "https://google.serper.dev/search" {
		t.Fatalf("url = %s %s", req.Method, req.URL)
	}
	if req.Header.Get("X-API-KEY") != "serper_test_key" {
		t.Fatalf("api key header = %q", req.Header.Get("X-API-KEY"))
	}
	if req.Header.Get("Accept") != "application/json" || !strings.Contains(req.Header.Get("User-Agent"), "searchwire") {
		t.Fatalf("headers = %#v", req.Header)
	}
	var payload map[string]any
	if err := json.Unmarshal(reqBody, &payload); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if payload["q"] != "Go context" {
		t.Fatalf("q = %v", payload["q"])
	}
	if num, ok := payload["num"].(float64); !ok || num != 5 {
		t.Fatalf("num = %v", payload["num"])
	}
	if len(results) != 3 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].title != "Go Programming Language" || results[0].url != "https://go.dev/" {
		t.Fatalf("first = %#v", results[0])
	}
}

func TestSerperConfigFromEnv(t *testing.T) {
	t.Setenv("SERPER_API_KEY", "serper_test_key")
	settings := SerperConfig{APIKeyEnv: "SERPER_API_KEY"}.resolved()
	if settings.apiKey != "serper_test_key" {
		t.Fatalf("api key = %q", settings.apiKey)
	}
}

func TestSerperConfigExplicitKeyOverridesEnv(t *testing.T) {
	t.Setenv("SERPER_API_KEY", "from-env")
	settings := SerperConfig{APIKey: "from-config"}.resolved()
	if settings.apiKey != "from-config" {
		t.Fatalf("api key = %q", settings.apiKey)
	}
}

func TestSerperDisabledByDefault(t *testing.T) {
	settings := SerperConfig{}.resolved()
	if settings.enabled {
		t.Fatalf("expected disabled by default, got %#v", settings)
	}
}

func TestSerperSourceNotAddedWithoutKey(t *testing.T) {
	sources := sourcesFromConfig(Config{Serper: SerperConfig{}})
	if len(sources) != 4 {
		t.Fatalf("sources = %d", len(sources))
	}
	if containsString(sourceNames(sources), "serper") {
		t.Fatalf("serper should not be registered without a key, got %#v", sourceNames(sources))
	}
}

func TestSerperNeverExposesAPIKeyInErrors(t *testing.T) {
	const apiKey = "serper_secret_test_key"
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid key ` + apiKey + `"}`)),
		}, nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	_, err := (serperSource{apiKey: apiKey}).search(context.Background(), f, "Go", 5)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("api key leaked in error: %v", err)
	}
}

func TestSerperRejectsWrongContentType(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(`<html></html>`)),
		}, nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	_, err := (serperSource{apiKey: "serper_test_key"}).search(context.Background(), f, "Go", 5)
	if !errors.Is(err, ErrUnexpectedContentType) {
		t.Fatalf("err = %v", err)
	}
}

func TestSerperMalformedJSON(t *testing.T) {
	if _, err := parseSerperResults([]byte(`{`), 10); err == nil {
		t.Fatal("expected error")
	}
}

func TestSerperSkipsUnsafeURLs(t *testing.T) {
	body := []byte(`{"organic":[{"title":"x","link":"javascript:alert(1)"}]}`)
	results, err := parseSerperResults(body, 10)
	if err != nil || len(results) != 0 {
		t.Fatalf("results = %#v err = %v", results, err)
	}
}
