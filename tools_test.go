package searchwire

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// toolFixtureClient serves every built-in source from testdata.
func toolFixtureClient(t *testing.T) *http.Client {
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
			return okBody("text/html; charset=utf-8", brave), nil
		case strings.Contains(host, "startpage.com"):
			return okBody("text/html; charset=utf-8", startpage), nil
		case strings.Contains(host, "wikipedia.org"):
			return okBody("application/json", wikipedia), nil
		case strings.Contains(host, "api.github.com") && strings.Contains(path, "/search/repositories"):
			return okBody("application/json", repos), nil
		case strings.Contains(host, "api.github.com") && strings.Contains(path, "/search/issues"):
			return okBody("application/json", issues), nil
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

func okBody(contentType string, body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}

func findDef(t *testing.T, defs []ToolDef, name string) ToolDef {
	t.Helper()
	for _, def := range defs {
		if def.Name == name {
			return def
		}
	}
	t.Fatalf("tool %q not found", name)
	return ToolDef{}
}

func TestToolsAlwaysExposesSearchAndFetch(t *testing.T) {
	clearAnswerProviderKeys(t)
	defs := New(Config{HTTPClient: toolFixtureClient(t)}).Tools()
	var names []string
	for _, def := range defs {
		names = append(names, def.Name)
	}
	if len(names) != 2 || names[0] != ToolSearch || names[1] != ToolFetch {
		t.Fatalf("names = %#v", names)
	}
	for _, def := range defs {
		if def.Description == "" || def.Run == nil {
			t.Fatalf("incomplete def: %#v", def)
		}
		var schema map[string]any
		if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
			t.Fatalf("schema for %s: %v", def.Name, err)
		}
		if schema["type"] != "object" {
			t.Fatalf("schema type for %s = %v", def.Name, schema["type"])
		}
	}
}

func TestToolsGatesAnswerOnProvider(t *testing.T) {
	clearAnswerProviderKeys(t)
	if defs := New(Config{}).Tools(); len(defs) != 2 {
		t.Fatalf("names without provider = %#v", defs)
	}
	t.Setenv("BRAVE_SEARCH_API_KEY", "brave_test_key")
	defs := New(Config{HTTPClient: toolFixtureClient(t)}).Tools()
	findDef(t, defs, ToolAnswer)
}

func TestSearchToolRunDecodesArgsAndReturnsJSON(t *testing.T) {
	defs := New(Config{HTTPClient: toolFixtureClient(t)}).Tools()
	def := findDef(t, defs, ToolSearch)
	raw, err := def.Run(context.Background(), json.RawMessage(`{"query":"Go","limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Query   string   `json:"query"`
		Results []Result `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Query != "Go" || len(out.Results) == 0 || len(out.Results) > 2 {
		t.Fatalf("out = %#v", out)
	}
}

func TestFetchToolRunDecodesArgsAndReturnsJSON(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("page body")),
		}, nil
	})}
	defs := New(Config{HTTPClient: client}).Tools()
	raw, err := findDef(t, defs, ToolFetch).Run(context.Background(), json.RawMessage(`{"url":"https://example.com/x"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		URL  string `json:"url"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Text != "page body" {
		t.Fatalf("out = %#v", out)
	}
}

func TestToolRunRejectsMalformedArgs(t *testing.T) {
	defs := New(Config{HTTPClient: toolFixtureClient(t)}).Tools()
	for _, def := range defs {
		if _, err := def.Run(context.Background(), json.RawMessage(`{nope`)); err == nil {
			t.Fatalf("%s accepted malformed args", def.Name)
		}
	}
}

func TestAnswerToolRunDecodesArgsAndReturnsJSON(t *testing.T) {
	clearAnswerProviderKeys(t)
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return answerJSONResponse(`{"choices":[{"message":{"content":"grounded"}}]}`), nil
	})}
	defs := New(Config{HTTPClient: client, Brave: BraveConfig{APIKey: "k"}}).Tools()
	raw, err := findDef(t, defs, ToolAnswer).Run(context.Background(), json.RawMessage(`{"question":"q"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Answer != "grounded" || out.Provider != "brave" {
		t.Fatalf("out = %#v", out)
	}
}

func TestToolErrorsRedactConfiguredSecrets(t *testing.T) {
	const secret = "tok_secret_do_not_leak"
	t.Setenv("GITHUB_TOKEN", secret)
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("unauthorized: " + secret)),
		}, nil
	})}
	defs := New(Config{HTTPClient: client}).Tools()
	_, err := findDef(t, defs, ToolSearch).Run(context.Background(), json.RawMessage(`{"query":"x"}`))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v", err)
	}
}
