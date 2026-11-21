package searchwire

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestExtractHTMLPageBlockStructure(t *testing.T) {
	body := []byte(`<html><head><title>Page Title</title></head><body>
<h2>Intro</h2>
<p>The core of the
<code>context</code>
package is the <a href="/pkg/context">Context</a> type.</p>
<ul><li>one</li><li>two</li></ul>
<pre>func main() {
	run()
}</pre>
</body></html>`)
	title, text, _, _ := extractHTMLPage(body)
	if title != "Page Title" {
		t.Fatalf("title = %q", title)
	}
	if strings.Contains(text, "Page Title") {
		t.Fatalf("title duplicated into text: %q", text)
	}
	for _, want := range []string{
		"## Intro",
		"The core of the context package is the Context type.",
		"- one\n- two",
		"```\nfunc main() {\n\trun()\n}\n```",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q:\n%s", want, text)
		}
	}
}

func TestResolvePageLinksResolvesAndDedupes(t *testing.T) {
	links := []Link{
		{Text: "", Href: "/docs"},
		{Text: "Docs", Href: "https://example.com/docs#top"},
		{Text: "Self", Href: "https://example.com/page#section"},
		{Text: "Mail", Href: "mailto:a@example.com"},
		{Text: "Up", Href: "../other"},
	}
	got := resolvePageLinks("https://example.com/page", links)
	if len(got) != 2 {
		t.Fatalf("links = %+v", got)
	}
	if got[0].Href != "https://example.com/docs" || got[0].Text != "Docs" {
		t.Fatalf("first link = %+v", got[0])
	}
	if got[1].Href != "https://example.com/other" {
		t.Fatalf("second link = %+v", got[1])
	}
	if isFollowableHref("#frag") || isFollowableHref("javascript:x()") || !isFollowableHref("rel/path") {
		t.Fatal("isFollowableHref misclassified")
	}
}

func TestCheckScrapeBlockedDetectsChallenge(t *testing.T) {
	challenge := []byte(`<html><script id="anubis_challenge">{}</script></html>`)
	if _, err := parseStartpageResults(challenge, 5); !errors.Is(err, ErrSourceBlocked) {
		t.Fatalf("startpage err = %v", err)
	}
	if _, err := parseBraveResults([]byte(`<html><body>Please solve the CAPTCHA</body></html>`), 5); !errors.Is(err, ErrSourceBlocked) {
		t.Fatalf("brave err = %v", err)
	}
	if res, err := parseBraveResults([]byte(`<html><body>No results found</body></html>`), 5); err != nil || len(res) != 0 {
		t.Fatalf("plain empty page: res=%v err=%v", res, err)
	}
}

func TestFuseResultsFoldsSchemeAndWWW(t *testing.T) {
	sources := []source{startpageSource{}, wikipediaSource{}}
	outcomes := []sourceOutcome{
		{name: "startpage", results: []sourceResult{{title: "A", url: "http://www.example.com/a", rank: 1}}},
		{name: "wikipedia", results: []sourceResult{{title: "A", url: "https://example.com/a", rank: 1}}},
	}
	got := fuseResults("", nil, outcomes, sources, 10)
	if len(got) != 1 {
		t.Fatalf("results = %+v", got)
	}
	if !strings.HasPrefix(got[0].URL, "https://") || len(got[0].Sources) != 2 {
		t.Fatalf("fused = %+v", got[0])
	}
}

func TestSearchToolOutputShape(t *testing.T) {
	defs := New(Config{HTTPClient: toolFixtureClient(t)}).Tools()
	def := findDef(t, defs, ToolSearch)
	if !strings.Contains(def.Description, "brave, startpage, wikipedia, github") {
		t.Fatalf("description does not list configured sources: %q", def.Description)
	}
	raw, err := def.Run(context.Background(), json.RawMessage(`{"query":"Go","limit":500}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "\n  ") {
		t.Fatalf("output should be compact JSON")
	}
	var out struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) == 0 || len(out.Results) > toolSearchMaxLimit {
		t.Fatalf("results = %d", len(out.Results))
	}
	for _, r := range out.Results {
		if _, ok := r["url"]; !ok {
			t.Fatalf("expected camelCase keys, got %v", r)
		}
		if s, _ := r["snippet"].(string); len(s) > toolSnippetMaxLen {
			t.Fatalf("snippet not capped: %d bytes", len(s))
		}
	}
}

func TestAnswerToolSchemaListsOnlyConfiguredProviders(t *testing.T) {
	clearAnswerProviderKeys(t)
	t.Setenv("BRAVE_SEARCH_API_KEY", "brave_test_key")
	def := findDef(t, New(Config{HTTPClient: &http.Client{}}).Tools(), ToolAnswer)
	var schema struct {
		Properties struct {
			Provider struct {
				Enum []string `json:"enum"`
			} `json:"provider"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Properties.Provider.Enum) != 1 || schema.Properties.Provider.Enum[0] != AnswerProviderBrave {
		t.Fatalf("enum = %v", schema.Properties.Provider.Enum)
	}
}

func TestTruncateSnippetIsRuneSafeAndBounded(t *testing.T) {
	in := strings.Repeat("héllo wörld ", 50)
	out := truncateSnippet(in, 64)
	if len(out) > 64 || !strings.HasSuffix(out, "…") {
		t.Fatalf("out = %q (%d bytes)", out, len(out))
	}
	if strings.ContainsRune(out, '\uFFFD') {
		t.Fatalf("split a rune: %q", out)
	}
}
