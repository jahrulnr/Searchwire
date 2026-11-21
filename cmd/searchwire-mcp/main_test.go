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
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jahrulnr/searchwire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func TestAnswerTimeoutFromEnv(t *testing.T) {
	t.Setenv("SEARCHWIRE_TIMEOUT", "4s")
	t.Setenv("SEARCHWIRE_ANSWER_TIMEOUT", "45s")
	cfg := configFromEnv()
	if cfg.Timeout != 4*time.Second || cfg.AnswerTimeout != 45*time.Second {
		t.Fatalf("timeout=%s answer timeout=%s", cfg.Timeout, cfg.AnswerTimeout)
	}
}

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

// The tool contract (names, schemas, runners) lives in the library; these
// types mirror its wire JSON so tests assert the contract, not internals.
const (
	searchToolName = searchwire.ToolSearch
	fetchToolName  = searchwire.ToolFetch
	answerToolName = searchwire.ToolAnswer
)

type searchInput struct {
	Query string `json:"query"`
	Limit *int   `json:"limit,omitempty"`
}

type searchOutput struct {
	Query   string                   `json:"query"`
	Results []searchwire.Result      `json:"results"`
	Errors  []searchwire.SourceError `json:"errors,omitempty"`
}

type fetchInput struct {
	URL      string `json:"url"`
	MaxBytes *int64 `json:"max_bytes,omitempty"`
}

type fetchOutput struct {
	URL         string            `json:"url"`
	FinalURL    string            `json:"finalUrl,omitempty"`
	StatusCode  int               `json:"statusCode"`
	ContentType string            `json:"contentType,omitempty"`
	Title       string            `json:"title,omitempty"`
	Text        string            `json:"text"`
	Links       []searchwire.Link `json:"links,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Redirects   int               `json:"redirects,omitempty"`
	Truncated   bool              `json:"truncated"`
	Bytes       int               `json:"bytes"`
	DescribedBy string            `json:"describedBy,omitempty"`
	MarkdownAlt string            `json:"markdownAlt,omitempty"`
	LLMsTxt     *llmsTxtOutput    `json:"llmsTxt,omitempty"`
	TempFile    string            `json:"tempFile,omitempty"`
}

type llmsTxtOutput struct {
	URL         string              `json:"url"`
	FinalURL    string              `json:"finalUrl,omitempty"`
	StatusCode  int                 `json:"statusCode"`
	Title       string              `json:"title"`
	Summary     string              `json:"summary,omitempty"`
	Description string              `json:"description,omitempty"`
	Sections    []llmsSectionOutput `json:"sections,omitempty"`
}

type llmsLinkOutput struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Notes string `json:"notes,omitempty"`
}

type llmsSectionOutput struct {
	Name  string           `json:"name"`
	Links []llmsLinkOutput `json:"links,omitempty"`
}

type answerInput struct {
	Question string `json:"question"`
	Provider string `json:"provider,omitempty"`
}

type answerOutput struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Provider string `json:"provider"`
}

func findToolDef(cfg searchwire.Config, name string) (searchwire.ToolDef, bool) {
	for _, def := range searchwire.New(cfg).Tools() {
		if def.Name == name {
			return def, true
		}
	}
	return searchwire.ToolDef{}, false
}

func runTool(ctx context.Context, cfg searchwire.Config, name string, in, out any) error {
	def, ok := findToolDef(cfg, name)
	if !ok {
		return errors.New("tool not registered: " + name)
	}
	args, err := json.Marshal(in)
	if err != nil {
		return err
	}
	raw, err := def.Run(ctx, args)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func runSearch(ctx context.Context, cfg searchwire.Config, in searchInput) (searchOutput, error) {
	var out searchOutput
	err := runTool(ctx, cfg, searchToolName, in, &out)
	return out, err
}

func runFetch(ctx context.Context, cfg searchwire.Config, _ int64, in fetchInput) (fetchOutput, error) {
	var out fetchOutput
	err := runTool(ctx, cfg, fetchToolName, in, &out)
	return out, err
}

func runAnswer(ctx context.Context, cfg searchwire.Config, in answerInput) (answerOutput, error) {
	var out answerOutput
	err := runTool(ctx, cfg, answerToolName, in, &out)
	return out, err
}

// makeToolHandler adapts a library ToolDef to the same triple the previous
// typed MCP handlers returned, so tests keep asserting result + structured.
func makeToolHandler[In, Out any](cfg searchwire.Config, name string) func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
		var out Out
		def, ok := findToolDef(cfg, name)
		if !ok {
			return nil, out, errors.New("tool not registered: " + name)
		}
		args, err := json.Marshal(input)
		if err != nil {
			return nil, out, err
		}
		raw, runErr := def.Run(ctx, args)
		if runErr != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: sanitizeError(runErr.Error())}},
			}, out, nil
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, out, err
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: string(raw)}},
			StructuredContent: raw,
		}, out, nil
	}
}

func makeSearchHandler(cfg searchwire.Config) func(context.Context, *mcp.CallToolRequest, searchInput) (*mcp.CallToolResult, searchOutput, error) {
	return makeToolHandler[searchInput, searchOutput](cfg, searchToolName)
}

func makeFetchHandler(cfg searchwire.Config, _ int64) func(context.Context, *mcp.CallToolRequest, fetchInput) (*mcp.CallToolResult, fetchOutput, error) {
	return makeToolHandler[fetchInput, fetchOutput](cfg, fetchToolName)
}

func makeAnswerHandler(cfg searchwire.Config) func(context.Context, *mcp.CallToolRequest, answerInput) (*mcp.CallToolResult, answerOutput, error) {
	return makeToolHandler[answerInput, answerOutput](cfg, answerToolName)
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
	cfg := configFromEnv()
	if cfg.Limit != 3 {
		t.Fatalf("limit = %d", cfg.Limit)
	}
	if cfg.Timeout.String() != "2s" {
		t.Fatalf("timeout = %v", cfg.Timeout)
	}
	if cfg.MaxResponseBytes != 4096 {
		t.Fatalf("max bytes cfg=%d", cfg.MaxResponseBytes)
	}
}

func TestNewServerRegistersTools(t *testing.T) {
	t.Setenv(envBraveAPIKey, "")
	server := newServer(searchwire.Config{HTTPClient: fixtureClient(t)})
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
	t.Setenv(envOmniAPIKey, "")
	t.Setenv(envOpenCodeAPIKey, "")
	t.Setenv(envOpenCode2APIKey, "")
	t.Setenv("OPENAI", "")
	withoutKey := listToolNames(t, newServer(searchwire.Config{HTTPClient: fixtureClient(t)}))
	if containsToolName(withoutKey, answerToolName) {
		t.Fatalf("tools without key = %#v", withoutKey)
	}

	withKey := listToolNames(t, newServer(searchwire.Config{
		HTTPClient: fixtureClient(t),
		Brave:      searchwire.BraveConfig{APIKey: "brave_test_key"},
	}))
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
	t.Setenv(envOmniAPIKey, "")
	t.Setenv(envOpenCodeAPIKey, "")
	t.Setenv(envOpenCode2APIKey, "")
	t.Setenv("OPENAI", "")
	for name, cfg := range map[string]searchwire.Config{
		"openrouter": {OpenRouter: searchwire.OpenRouterConfig{APIKey: "openrouter_test_key"}},
		"openai":     {OpenAI: searchwire.OpenAIConfig{APIKey: "openai_test_key"}},
		"perplexity": {Perplexity: searchwire.PerplexityConfig{APIKey: "perplexity_test_key"}},
		"anthropic":  {Anthropic: searchwire.AnthropicConfig{APIKey: "anthropic_test_key"}},
		"xai":        {XAI: searchwire.XAIConfig{APIKey: "xai_test_key"}},
		"omniroute":  {OmniRoute: searchwire.OmniRouteConfig{APIKey: "omni_test_key"}},
		"opencode":   {OpenCode: searchwire.OpenCodeConfig{APIKey: "opencode_test_key"}},
	} {
		t.Run(name, func(t *testing.T) {
			tools := listToolNames(t, newServer(cfg))
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
	}))
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
	return slices.Contains(names, target)
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
	if !out.Truncated || out.Bytes != 20 || out.Text != "zzzzzzzz\n[truncated]" {
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

func TestFetchPopulatesEmbeddedLLMsTxt(t *testing.T) {
	// The previous llms.txt tool was merged into the fetch tool:
	// when the source is HTML (2xx) and a covering llms.txt exists, the
	// parsed file is embedded in fetchOutput.llmsTxt so agents do not need
	// a second round-trip. The probe is best-effort: any failure (404,
	// invalid file, non-HTML source) leaves the field nil.
	var seen []string
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.URL.String())
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
				StatusCode: 200, Status: "200 OK",
				Header:  http.Header{"Content-Type": []string{"text/markdown"}},
				Body:    io.NopCloser(strings.NewReader("# Docs\n\n> summary.\n\n## Guides\n\n- [a](https://example.com/a.md): notes\n")),
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
	out, err := runFetch(context.Background(), searchwire.Config{HTTPClient: client}, 0, fetchInput{URL: "https://example.com/docs/page.html"})
	if err != nil {
		t.Fatal(err)
	}
	if out.LLMsTxt == nil {
		t.Fatalf("expected embedded llmsTxt, got nil (seen=%v)", seen)
	}
	if out.LLMsTxt.Title != "Docs" || out.LLMsTxt.Summary != "summary." {
		t.Fatalf("llmsTxt = %#v", out.LLMsTxt)
	}
	if len(out.LLMsTxt.Sections) != 1 || out.LLMsTxt.Sections[0].Links[0].Notes != "notes" {
		t.Fatalf("sections = %#v", out.LLMsTxt.Sections)
	}
	// Verify the probe did not recurse: only the page + the most-specific
	// llms.txt should have been requested.
	want := []string{
		"https://example.com/docs/page.html",
		"https://example.com/docs/llms.txt",
	}
	if len(seen) != len(want) || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("requests = %v, want %v", seen, want)
	}
}

func TestFetchSkipsLLMsTxtWhenSourceNotHTML(t *testing.T) {
	// Non-HTML responses must not trigger the llms.txt probe. A JSON
	// source has no notion of "covering llms.txt" and following the
	// pointer would just be noise.
	var seen []string
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.URL.String())
		return &http.Response{
			StatusCode: 200, Status: "200 OK",
			Header:  http.Header{"Content-Type": []string{"application/json"}},
			Body:    io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request: r,
		}, nil
	})}
	out, err := runFetch(context.Background(), searchwire.Config{HTTPClient: client}, 0, fetchInput{URL: "https://example.com/data.json"})
	if err != nil {
		t.Fatal(err)
	}
	if out.LLMsTxt != nil {
		t.Fatalf("expected no llmsTxt for JSON, got %#v", out.LLMsTxt)
	}
	if len(seen) != 1 {
		t.Fatalf("expected 1 request, got %v", seen)
	}
}

func TestFetchSkipsLLMsTxtOnSourceHTTPError(t *testing.T) {
	// 4xx/5xx source responses skip the llms.txt probe entirely: the
	// fetch tool surfaces the source error and the embedded
	// llmsTxt field is left nil. No extra round-trip is made.
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 500, Status: "500 Internal Server Error",
			Header:  http.Header{"Content-Type": []string{"text/html"}},
			Body:    io.NopCloser(strings.NewReader("boom")),
			Request: r,
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

func TestFetchSkipsLLMsTxtWhenLLMsTxtMissing(t *testing.T) {
	// When the source page is 2xx HTML but no covering llms.txt exists
	// (all probes 404), the embedded llmsTxt field is left nil and the
	// fetch still returns the source page successfully. No extra error
	// is surfaced to the caller — a missing llms.txt is a non-event.
	var seen []string
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.URL.String())
		switch r.URL.String() {
		case "https://example.com/page.html":
			return &http.Response{
				StatusCode: 200, Status: "200 OK",
				Header:  http.Header{"Content-Type": []string{"text/html"}},
				Body:    io.NopCloser(strings.NewReader(`<html><head><title>P</title></head><body>main</body></html>`)),
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
	out, err := runFetch(context.Background(), searchwire.Config{HTTPClient: client}, 0, fetchInput{URL: "https://example.com/page.html"})
	if err != nil {
		t.Fatal(err)
	}
	if out.LLMsTxt != nil {
		t.Fatalf("expected no llmsTxt when all probes 404, got %#v", out.LLMsTxt)
	}
	// page.html is at the site root, so the only candidate is
	// /llms.txt (1 probe, not 2). Total: 1 page + 1 probe = 2 requests.
	if len(seen) != 2 {
		t.Fatalf("expected 2 requests (page + 1 root probe), got %v", seen)
	}
}

func TestFetchSkipsLLMsTxtWhenSubdirLLMsTxtMissing(t *testing.T) {
	// For a deeper URL the probe tries <dir>/llms.txt first, then
	// /llms.txt at the root. Both 404 here, so the field stays nil and
	// the caller sees exactly 3 requests (page + 2 probes).
	var seen []string
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.URL.String())
		switch r.URL.String() {
		case "https://example.com/docs/page.html":
			return &http.Response{
				StatusCode: 200, Status: "200 OK",
				Header:  http.Header{"Content-Type": []string{"text/html"}},
				Body:    io.NopCloser(strings.NewReader(`<html><head><title>P</title></head><body>main</body></html>`)),
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
	out, err := runFetch(context.Background(), searchwire.Config{HTTPClient: client}, 0, fetchInput{URL: "https://example.com/docs/page.html"})
	if err != nil {
		t.Fatal(err)
	}
	if out.LLMsTxt != nil {
		t.Fatalf("expected no llmsTxt, got %#v", out.LLMsTxt)
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 requests (page + dir probe + root probe), got %v", seen)
	}
}

func TestFetchWritesTempFileWhenTruncated(t *testing.T) {
	// When the response is truncated, the full extracted text is also
	// materialized to a file under the configured temp dir (default
	// os.TempDir()/searchwire) so the agent can read the rest with
	// file_read instead of refetching.
	dir := t.TempDir()
	cfg := searchwire.Config{
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200, Status: "200 OK",
				Header:  http.Header{"Content-Type": []string{"text/plain"}},
				Body:    io.NopCloser(strings.NewReader(strings.Repeat("z", 100))),
				Request: r,
			}, nil
		})},
		TempDir: dir,
	}
	max := int64(20)
	out, err := runFetch(context.Background(), cfg, 0, fetchInput{URL: "https://example.com/big", MaxBytes: &max})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated {
		t.Fatalf("expected truncated, got %#v", out)
	}
	if out.TempFile == "" {
		t.Fatalf("expected TempFile, got empty")
	}
	if !strings.HasPrefix(out.TempFile, dir) {
		t.Fatalf("TempFile not in %s: %s", dir, out.TempFile)
	}
	contents, err := os.ReadFile(out.TempFile)
	if err != nil {
		t.Fatal(err)
	}
	// The temp file holds the FULL extracted text so the agent can read
	// beyond the inline budget — not the truncated Text that carries the
	// [truncated] marker.
	if string(contents) == out.Text {
		t.Fatalf("temp file must not mirror truncated inline text: %q", contents)
	}
	if len(contents) <= len(out.Text) {
		t.Fatalf("temp file (%d bytes) should exceed inline text (%d bytes)", len(contents), len(out.Text))
	}
}

func TestConfigFromEnvReadsTempDir(t *testing.T) {
	t.Setenv(envTempDir, "/tmp/sw-mcp-test")
	cfg := configFromEnv()
	if cfg.TempDir != "/tmp/sw-mcp-test" {
		t.Fatalf("TempDir = %q", cfg.TempDir)
	}
}

func TestSearchwireLLMsTxtToolRemoved(t *testing.T) {
	// The standalone llms.txt tool was intentionally removed from the MCP
	// surface: llms.txt probing is now an automatic side effect of the
	// fetch tool on 2xx HTML responses. Agents read the embedded llmsTxt
	// field instead of calling a separate tool.
	server := newServer(searchwire.Config{HTTPClient: fixtureClient(t)})
	for _, name := range listToolNames(t, server) {
		if name == "searchwire_llms_txt" {
			t.Fatalf("searchwire_llms_txt must not be registered; tools = %v", listToolNames(t, server))
		}
	}
}

func TestToolNamesDropSearchwirePrefix(t *testing.T) {
	// Tool names drop the "searchwire_" prefix because the MCP server
	// name already carries it. Address tools as "<server>.<tool>"
	// (e.g. "searchwire.search") so a prefixed tool name would be
	// doubly redundant ("searchwire.searchwire_search"). This is a
	// regression guard: renaming back to prefixed names would break
	// every client that adopted the unprefixed convention.
	tools := listToolNames(t, newServer(searchwire.Config{HTTPClient: fixtureClient(t)}))
	want := map[string]bool{
		"search": false,
		"fetch":  false,
		"answer": false, // absent without a key, registered with one
	}
	for _, name := range tools {
		if _, ok := want[name]; ok {
			want[name] = true
		}
		if strings.HasPrefix(name, "searchwire_") {
			t.Fatalf("tool %q still carries the searchwire_ prefix; tools = %v", name, tools)
		}
	}
	if !want["search"] || !want["fetch"] {
		t.Fatalf("expected search + fetch to be registered; tools = %v, want map = %v", tools, want)
	}
}

func TestConfigFromEnvReadsDefaultSources(t *testing.T) {
	t.Setenv(envDefaultSources, " brave, openalex ,,arxiv ")
	cfg := configFromEnv()
	if len(cfg.DefaultSources) != 3 || cfg.DefaultSources[1] != "openalex" || cfg.DefaultSources[2] != "arxiv" {
		t.Fatalf("DefaultSources = %#v", cfg.DefaultSources)
	}
}
