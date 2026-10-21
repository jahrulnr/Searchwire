package searchwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestFetchToolBoundsPageLinksAndLLMsTxt(t *testing.T) {
	var page strings.Builder
	page.WriteString("<html><body><p>hi</p>")
	for i := 0; i < toolFetchMaxLinks+10; i++ {
		fmt.Fprintf(&page, `<a href="https://example.com/p%d">link %d</a>`, i, i)
	}
	page.WriteString("</body></html>")
	var llms strings.Builder
	llms.WriteString("# Docs\n\n## Big\n")
	for i := 0; i < toolFetchMaxLLMsTxtLinks+25; i++ {
		fmt.Fprintf(&llms, "- [d%d](https://example.com/d%d)\n", i, i)
	}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/llms.txt") {
			return okBody("text/plain", []byte(llms.String())), nil
		}
		return okBody("text/html", []byte(page.String())), nil
	})}
	defs := New(Config{HTTPClient: client}).Tools()
	raw, err := findDef(t, defs, ToolFetch).Run(context.Background(), json.RawMessage(`{"url":"https://example.com/"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Links        []json.RawMessage `json:"links"`
		LinksOmitted int               `json:"linksOmitted"`
		LLMsTxt      struct {
			Sections []struct {
				Links []json.RawMessage `json:"links"`
			} `json:"sections"`
			LinksOmitted int `json:"linksOmitted"`
		} `json:"llmsTxt"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Links) != toolFetchMaxLinks || out.LinksOmitted != 10 {
		t.Fatalf("links = %d omitted = %d", len(out.Links), out.LinksOmitted)
	}
	total := 0
	for _, sec := range out.LLMsTxt.Sections {
		total += len(sec.Links)
	}
	if total != toolFetchMaxLLMsTxtLinks || out.LLMsTxt.LinksOmitted != 25 {
		t.Fatalf("llmsTxt links = %d omitted = %d", total, out.LLMsTxt.LinksOmitted)
	}
}

func TestFetchToolErrorBodyIsBounded(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("<p>not found</p>", 8000))),
		}, nil
	})}
	defs := New(Config{HTTPClient: client}).Tools()
	_, err := findDef(t, defs, ToolFetch).Run(context.Background(), json.RawMessage(`{"url":"https://example.com/"}`))
	if err == nil {
		t.Fatal("expected error")
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err type = %T", err)
	}
	if len(err.Error()) > toolErrorMaxLen*2 {
		t.Fatalf("error text = %d chars — upstream body must be bounded", len(err.Error()))
	}
	if !strings.HasSuffix(httpErr.Body, "…") {
		t.Fatalf("body = %q…", httpErr.Body[:80])
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

// TestFetchToolScopesRootLLMsTxtCatalog verifies that a site-root llms.txt
// catalog covering none of the fetched path is reduced to its header:
// url/title/summary stay (the catalog exists and is worth knowing about)
// but its link sections are omitted instead of flooding the document with
// domain-wide navigation unrelated to the page.
func TestFetchToolScopesRootLLMsTxtCatalog(t *testing.T) {
	catalog := `# BigCo

> Whole-site docs index.

## Docs

- [Feature](https://example.com/features/x): f
- [Blog](https://blog.example.com/post)

## More

- [API](https://example.com/api/ref)
`
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		var resp *http.Response
		switch r.URL.Path {
		case "/llms.txt":
			resp = okBody("text/plain", []byte(catalog))
		case "/jahrulnr/llms.txt", "/features/llms.txt":
			resp = &http.Response{
				StatusCode: 404, Status: "404 Not Found",
				Header: http.Header{"Content-Type": []string{"text/plain"}},
				Body:   io.NopCloser(strings.NewReader("missing")),
			}
		default:
			resp = okBody("text/html", []byte(`<html><body><p>page</p></body></html>`))
		}
		resp.Request = r
		return resp, nil
	})}
	defs := New(Config{HTTPClient: client}).Tools()
	fetch := findDef(t, defs, ToolFetch)

	// A repo page under /jahrulnr: the catalog links to /features, /api and
	// a different host — nothing under the page's directory, so sections
	// are dropped.
	raw, err := fetch.Run(context.Background(), json.RawMessage(`{"url":"https://example.com/jahrulnr/NusaShell"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		LLMsTxt *struct {
			URL          string            `json:"url"`
			Title        string            `json:"title"`
			Sections     []json.RawMessage `json:"sections"`
			LinksOmitted int               `json:"linksOmitted"`
		} `json:"llmsTxt"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.LLMsTxt == nil || out.LLMsTxt.URL == "" || out.LLMsTxt.Title != "BigCo" {
		t.Fatalf("llmsTxt = %#v", out.LLMsTxt)
	}
	if len(out.LLMsTxt.Sections) != 0 || out.LLMsTxt.LinksOmitted != 3 {
		t.Fatalf("sections = %d omitted = %d", len(out.LLMsTxt.Sections), out.LLMsTxt.LinksOmitted)
	}

	// A page under /features is covered by the catalog (a link points into
	// the same directory) — sections pass through in full.
	raw, err = fetch.Run(context.Background(), json.RawMessage(`{"url":"https://example.com/features/x"}`))
	if err != nil {
		t.Fatal(err)
	}
	var covered struct {
		LLMsTxt *struct {
			Sections []struct {
				Name  string            `json:"name"`
				Links []json.RawMessage `json:"links"`
			} `json:"sections"`
		} `json:"llmsTxt"`
	}
	if err := json.Unmarshal(raw, &covered); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if covered.LLMsTxt == nil || len(covered.LLMsTxt.Sections) != 2 {
		t.Fatalf("covered llmsTxt = %#v", covered.LLMsTxt)
	}
}
