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

func TestParseTavilyResults(t *testing.T) {
	results, err := parseTavilyResults(readFixture(t, "tavily-results.json"), 10)
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

func TestTavilySearchRequestContract(t *testing.T) {
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
			Body:       io.NopCloser(strings.NewReader(readFixtureString(t, "tavily-results.json"))),
		}, nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	results, err := (tavilySource{apiKey: "tavily_test_key"}).search(context.Background(), f, "Go context", 5)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPost || req.URL.String() != "https://api.tavily.com/search" {
		t.Fatalf("url = %s %s", req.Method, req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer tavily_test_key" {
		t.Fatalf("authorization = %q", req.Header.Get("Authorization"))
	}
	if req.Header.Get("Accept") != "application/json" || !strings.Contains(req.Header.Get("User-Agent"), "searchwire") {
		t.Fatalf("headers = %#v", req.Header)
	}
	var payload map[string]any
	if err := json.Unmarshal(reqBody, &payload); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if payload["query"] != "Go context" {
		t.Fatalf("query = %v", payload["query"])
	}
	if max, ok := payload["max_results"].(float64); !ok || max != 5 {
		t.Fatalf("max_results = %v", payload["max_results"])
	}
	if len(results) != 3 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].title != "Go Programming Language" || results[0].url != "https://go.dev/" {
		t.Fatalf("first = %#v", results[0])
	}
}

func TestTavilyConfigFromEnv(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "tavily_test_key")
	settings := TavilyConfig{APIKeyEnv: "TAVILY_API_KEY"}.resolved()
	if settings.apiKey != "tavily_test_key" {
		t.Fatalf("api key = %q", settings.apiKey)
	}
}

func TestTavilyConfigExplicitKeyOverridesEnv(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "from-env")
	settings := TavilyConfig{APIKey: "from-config"}.resolved()
	if settings.apiKey != "from-config" {
		t.Fatalf("api key = %q", settings.apiKey)
	}
}

func TestTavilyDisabledByDefault(t *testing.T) {
	settings := TavilyConfig{}.resolved()
	if settings.enabled {
		t.Fatalf("expected disabled by default, got %#v", settings)
	}
}

func TestTavilySourceNotAddedWithoutKey(t *testing.T) {
	sources := sourcesFromConfig(Config{Tavily: TavilyConfig{}})
	if len(sources) != 4 {
		t.Fatalf("sources = %d", len(sources))
	}
	if containsString(sourceNames(sources), "tavily") {
		t.Fatalf("tavily should not be registered without a key, got %#v", sourceNames(sources))
	}
}

func TestTavilyNeverExposesAPIKeyInErrors(t *testing.T) {
	const apiKey = "tavily_secret_test_key"
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"detail":"invalid key ` + apiKey + `"}`)),
		}, nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	_, err := (tavilySource{apiKey: apiKey}).search(context.Background(), f, "Go", 5)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("api key leaked in error: %v", err)
	}
}

func TestTavilyRejectsWrongContentType(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(`<html></html>`)),
		}, nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	_, err := (tavilySource{apiKey: "tavily_test_key"}).search(context.Background(), f, "Go", 5)
	if !errors.Is(err, ErrUnexpectedContentType) {
		t.Fatalf("err = %v", err)
	}
}

func TestTavilyMalformedJSON(t *testing.T) {
	if _, err := parseTavilyResults([]byte(`{`), 10); err == nil {
		t.Fatal("expected error")
	}
}

func TestTavilySkipsUnsafeURLs(t *testing.T) {
	body := []byte(`{"results":[{"title":"x","url":"javascript:alert(1)"}]}`)
	results, err := parseTavilyResults(body, 10)
	if err != nil || len(results) != 0 {
		t.Fatalf("results = %#v err = %v", results, err)
	}
}
