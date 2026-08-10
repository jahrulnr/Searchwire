package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jahrulnr/searchwire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func fixturePath(name string) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", name)
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(fixturePath(name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixtureClient(t *testing.T) *http.Client {
	t.Helper()
	brave := readFixture(t, "brave-results.html")
	startpage := readFixture(t, "startpage-results.html")
	wikipedia := readFixture(t, "wikipedia-results.json")
	repos := readFixture(t, "github-repos.json")
	issues := readFixture(t, "github-issues.json")

	return &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		host := r.URL.Host
		path := r.URL.Path
		switch {
		case strings.Contains(host, "search.brave.com"):
			return okHTML(brave), nil
		case strings.Contains(host, "startpage.com"):
			return okHTML(startpage), nil
		case strings.Contains(host, "wikipedia.org"):
			return okJSON(wikipedia), nil
		case strings.Contains(host, "api.github.com") && strings.Contains(path, "/search/repositories"):
			return okJSON(repos), nil
		case strings.Contains(host, "api.github.com") && strings.Contains(path, "/search/issues"):
			return okJSON(issues), nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Status:     "404 Not Found",
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Body:       io.NopCloser(strings.NewReader("unknown host")),
			}, nil
		}
	})}
}

func failingClient() *http.Client {
	return &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", 500))),
		}, nil
	})}
}

func okHTML(body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}

func okJSON(body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}

func TestSearchHappyPath(t *testing.T) {
	cfg := searchwire.Config{HTTPClient: fixtureClient(t)}
	out, err := runSearch(context.Background(), cfg, searchInput{Query: "Go"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Query != "Go" {
		t.Fatalf("query = %q", out.Query)
	}
	if len(out.Results) == 0 {
		t.Fatalf("expected results, got %#v", out)
	}

	handler := makeSearchHandler(cfg)
	result, structured, err := handler(context.Background(), nil, searchInput{Query: "Go"})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil && result.IsError {
		t.Fatalf("unexpected tool error: %#v", result)
	}
	if len(structured.Results) == 0 {
		t.Fatalf("structured results empty: %#v", structured)
	}
	if result == nil || len(result.Content) == 0 {
		t.Fatal("expected text content fallback")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, `"results"`) {
		t.Fatalf("text fallback = %#v", result.Content[0])
	}
	var decoded searchOutput
	if err := json.Unmarshal([]byte(text.Text), &decoded); err != nil {
		t.Fatalf("text fallback JSON: %v", err)
	}
	if len(decoded.Results) == 0 {
		t.Fatal("decoded text fallback missing results")
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	cfg := searchwire.Config{HTTPClient: fixtureClient(t)}
	_, err := runSearch(context.Background(), cfg, searchInput{Query: "   "})
	if !errors.Is(err, searchwire.ErrEmptyQuery) && !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("err = %v", err)
	}

	handler := makeSearchHandler(cfg)
	result, _, err := handler(context.Background(), nil, searchInput{Query: ""})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected IsError result, got %#v", result)
	}
}

func TestSearchAllSourcesFail(t *testing.T) {
	cfg := searchwire.Config{HTTPClient: failingClient()}
	_, err := runSearch(context.Background(), cfg, searchInput{Query: "Go"})
	var searchErr *searchwire.SearchError
	if !errors.As(err, &searchErr) {
		t.Fatalf("err = %v", err)
	}
	if len(searchErr.Failures) == 0 {
		t.Fatal("expected failures")
	}
	for _, f := range searchErr.Failures {
		if len(f.Error) > maxErrorLen+8 {
			t.Fatalf("error not truncated: %q", f.Error)
		}
		if strings.Contains(f.Error, strings.Repeat("x", 300)) {
			t.Fatalf("response body leaked: %q", f.Error)
		}
	}

	handler := makeSearchHandler(cfg)
	result, _, err := handler(context.Background(), nil, searchInput{Query: "Go"})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected IsError result, got %#v", result)
	}
}

func TestSearchLimitOverride(t *testing.T) {
	var sawLimit atomic.Bool
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		if lim := q.Get("gsrlimit"); lim == "2" {
			sawLimit.Store(true)
		}
		if lim := q.Get("per_page"); lim == "2" {
			sawLimit.Store(true)
		}
		host := r.URL.Host
		switch {
		case strings.Contains(host, "search.brave.com"):
			return okHTML(readFixture(t, "brave-results.html")), nil
		case strings.Contains(host, "startpage.com"):
			return okHTML(readFixture(t, "startpage-results.html")), nil
		case strings.Contains(host, "wikipedia.org"):
			return okJSON(readFixture(t, "wikipedia-results.json")), nil
		case strings.Contains(host, "api.github.com"):
			return okJSON([]byte(`{"items":[]}`)), nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader("missing")),
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
			}, nil
		}
	})}

	limit := 2
	out, err := runSearch(context.Background(), searchwire.Config{HTTPClient: client, Limit: 10}, searchInput{
		Query: "Go",
		Limit: &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawLimit.Load() {
		t.Fatal("limit override was not observed on source requests")
	}
	if len(out.Results) > 2 {
		t.Fatalf("results = %d, want <= 2", len(out.Results))
	}
}

func TestTokenNeverAppearsInOutput(t *testing.T) {
	const secret = "ghp_secret_token_do_not_leak"
	t.Setenv("GITHUB_TOKEN", secret)

	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		auth := r.Header.Get("Authorization")
		body := "unauthorized: " + secret + " and " + auth
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	cfg := searchwire.Config{HTTPClient: client}
	_, err := runSearch(context.Background(), cfg, searchInput{Query: "Go"})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("token leaked in error: %v", err)
	}

	handler := makeSearchHandler(cfg)
	result, out, err := handler(context.Background(), nil, searchInput{Query: "Go"})
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(out)
	if strings.Contains(string(blob), secret) {
		t.Fatalf("token leaked in structured output: %s", blob)
	}
	if result != nil {
		for _, c := range result.Content {
			if text, ok := c.(*mcp.TextContent); ok && strings.Contains(text.Text, secret) {
				t.Fatalf("token leaked in tool content: %s", text.Text)
			}
		}
	}

	// Partial-failure path: one source succeeds, others include secret in body.
	mixed := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "wikipedia.org") {
			return okJSON(readFixture(t, "wikipedia-results.json")), nil
		}
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Status:     "403 Forbidden",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("denied for " + secret)),
		}, nil
	})}
	out, err = runSearch(context.Background(), searchwire.Config{HTTPClient: mixed}, searchInput{Query: "Go"})
	if err != nil {
		t.Fatal(err)
	}
	blob, _ = json.Marshal(out)
	if strings.Contains(string(blob), secret) {
		t.Fatalf("token leaked in partial failure output: %s", blob)
	}
}

func TestRedactSecretsIncludesBraveAPIKey(t *testing.T) {
	const secret = "brave_secret_token_do_not_leak"
	t.Setenv("BRAVE_SEARCH_API_KEY", secret)

	redacted := redactSecrets("invalid X-Subscription-Token: " + secret)
	if strings.Contains(redacted, secret) || !strings.Contains(redacted, "[redacted]") {
		t.Fatalf("redacted = %q", redacted)
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv(envLimit, "3")
	t.Setenv(envTimeout, "2s")
	t.Setenv(envMaxBytes, "4096")
	cfg, maxBytes := configFromEnv()
	if cfg.Limit != 3 {
		t.Fatalf("limit = %d", cfg.Limit)
	}
	if cfg.Timeout.String() != "2s" {
		t.Fatalf("timeout = %v", cfg.Timeout)
	}
	if cfg.MaxResponseBytes != 4096 || maxBytes != 4096 {
		t.Fatalf("max bytes cfg=%d adapter=%d", cfg.MaxResponseBytes, maxBytes)
	}
}

func TestNewServerRegistersTools(t *testing.T) {
	t.Setenv(envBraveAPIKey, "")
	server := newServer(searchwire.Config{HTTPClient: fixtureClient(t)}, 0)
	if server == nil {
		t.Fatal("nil server")
	}
}

func TestAnswerToolRegistrationFollowsResolvedAPIKey(t *testing.T) {
	t.Setenv(envBraveAPIKey, "")
	t.Setenv(envOpenRouterAPIKey, "")
	t.Setenv(envOpenAIAPIKey, "")
	t.Setenv(envPerplexityAPIKey, "")
	t.Setenv(envAnthropicAPIKey, "")
	t.Setenv(envXAIAPIKey, "")
	t.Setenv(envXAIAPIKey, "")
	withoutKey := listToolNames(t, newServer(searchwire.Config{HTTPClient: fixtureClient(t)}, 0))
	if containsToolName(withoutKey, answerToolName) {
		t.Fatalf("tools without key = %#v", withoutKey)
	}

	withKey := listToolNames(t, newServer(searchwire.Config{
		HTTPClient: fixtureClient(t),
		Brave:      searchwire.BraveConfig{APIKey: "brave_test_key"},
	}, 0))
	if !containsToolName(withKey, answerToolName) {
		t.Fatalf("tools with key = %#v", withKey)
	}
}

func TestAnswerToolRegistrationSupportsEachLLMProvider(t *testing.T) {
	t.Setenv(envBraveAPIKey, "")
	t.Setenv(envOpenRouterAPIKey, "")
	t.Setenv(envOpenAIAPIKey, "")
	t.Setenv(envPerplexityAPIKey, "")
	t.Setenv(envAnthropicAPIKey, "")
	for name, cfg := range map[string]searchwire.Config{
		"openrouter": {OpenRouter: searchwire.OpenRouterConfig{APIKey: "openrouter_test_key"}},
		"openai":     {OpenAI: searchwire.OpenAIConfig{APIKey: "openai_test_key"}},
		"perplexity": {Perplexity: searchwire.PerplexityConfig{APIKey: "perplexity_test_key"}},
		"anthropic":  {Anthropic: searchwire.AnthropicConfig{APIKey: "anthropic_test_key"}},
		"xai":        {XAI: searchwire.XAIConfig{APIKey: "xai_test_key"}},
	} {
		t.Run(name, func(t *testing.T) {
			tools := listToolNames(t, newServer(cfg, 0))
			if !containsToolName(tools, answerToolName) {
				t.Fatalf("tools = %#v", tools)
			}
		})
	}
}

func TestAnswerToolRegistrationUsesCustomAPIKeyEnv(t *testing.T) {
	t.Setenv(envBraveAPIKey, "")
	t.Setenv("SEARCHWIRE_TEST_BRAVE_KEY", "brave_test_key")
	tools := listToolNames(t, newServer(searchwire.Config{
		HTTPClient: fixtureClient(t),
		Brave: searchwire.BraveConfig{
			APIKeyEnv: "SEARCHWIRE_TEST_BRAVE_KEY",
		},
	}, 0))
	if !containsToolName(tools, answerToolName) {
		t.Fatalf("tools = %#v", tools)
	}
}

func TestAnswerHappyPath(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/res/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		return okJSON(readFixture(t, "brave-answer.json")), nil
	})}
	cfg := searchwire.Config{
		HTTPClient: client,
		Brave:      searchwire.BraveConfig{APIKey: "brave_test_key"},
	}
	out, err := runAnswer(context.Background(), cfg, answerInput{Question: "What is Go?"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Question != "What is Go?" || out.Answer != "Go is an open source programming language." || out.Provider != "brave" {
		t.Fatalf("out = %#v", out)
	}

	handler := makeAnswerHandler(cfg)
	result, structured, err := handler(context.Background(), nil, answerInput{Question: "What is Go?"})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.IsError || structured.Answer == "" {
		t.Fatalf("result = %#v structured = %#v", result, structured)
	}
}

func listToolNames(t *testing.T, server *mcp.Server) []string {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "searchwire-test", Version: "0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(listed.Tools))
	for i, tool := range listed.Tools {
		names[i] = tool.Name
	}
	return names
}

func containsToolName(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

func TestFetchHappyHTML(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body := `<html><head><title>Fetched</title></head><body><p>Hello fetch</p></body></html>`
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
	cfg := searchwire.Config{HTTPClient: client}
	out, err := runFetch(context.Background(), cfg, 0, fetchInput{URL: "https://example.com/page"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != "Fetched" || !strings.Contains(out.Text, "Hello fetch") {
		t.Fatalf("out = %#v", out)
	}

	handler := makeFetchHandler(cfg, 0)
	result, structured, err := handler(context.Background(), nil, fetchInput{URL: "https://example.com/page"})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil && result.IsError {
		t.Fatalf("unexpected error %#v", result)
	}
	if structured.Title != "Fetched" {
		t.Fatalf("structured = %#v", structured)
	}
}

func TestFetchPlainText(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("plain body")),
			Request:    r,
		}, nil
	})}
	out, err := runFetch(context.Background(), searchwire.Config{HTTPClient: client}, 0, fetchInput{URL: "https://example.com/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "plain body" {
		t.Fatalf("text = %q", out.Text)
	}
}

func TestFetchBadScheme(t *testing.T) {
	handler := makeFetchHandler(searchwire.Config{}, 0)
	result, _, err := handler(context.Background(), nil, fetchInput{URL: "ftp://example.com/x"})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected IsError, got %#v", result)
	}
}

func TestFetchHTTPError(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 500,
			Status:     "500 Internal Server Error",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("boom")),
			Request:    r,
		}, nil
	})}
	handler := makeFetchHandler(searchwire.Config{HTTPClient: client}, 0)
	result, _, err := handler(context.Background(), nil, fetchInput{URL: "https://example.com/err"})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected IsError, got %#v", result)
	}
}

func TestFetchMaxBytesTruncate(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("z", 100))),
			Request:    r,
		}, nil
	})}
	max := int64(20)
	out, err := runFetch(context.Background(), searchwire.Config{HTTPClient: client, MaxResponseBytes: 1000}, 0, fetchInput{
		URL:      "https://example.com/big",
		MaxBytes: &max,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || out.Bytes != 20 || out.Text != "zzzzzzzzzzzzzzzzzzzz" {
		t.Fatalf("out = %#v", out)
	}
}

func TestFetchTokenNeverInOutput(t *testing.T) {
	const secret = "ghp_fetch_secret_token"
	t.Setenv("GITHUB_TOKEN", secret)
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 401,
			Status:     "401 Unauthorized",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("denied " + secret)),
			Request:    r,
		}, nil
	})}
	handler := makeFetchHandler(searchwire.Config{HTTPClient: client}, 0)
	result, _, err := handler(context.Background(), nil, fetchInput{URL: "https://example.com/sec"})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected error %#v", result)
	}
	for _, c := range result.Content {
		if text, ok := c.(*mcp.TextContent); ok && strings.Contains(text.Text, secret) {
			t.Fatalf("token leaked: %s", text.Text)
		}
	}
}
