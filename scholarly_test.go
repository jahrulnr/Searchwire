package searchwire

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseOpenAlexFixture(t *testing.T) {
	results, err := parseOpenAlexResults(readFixture(t, "openalex-works.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	r := results[0]
	if r.title != "Corrective Retrieval Augmented Generation" || r.url != "https://doi.org/10.18653/2026.findings-aacl.151" {
		t.Fatalf("first = %#v", r)
	}
	for _, want := range []string{"[2026", "749 citations", "open-access PDF: https://aclanthology.org/", "] Large language models"} {
		if !strings.Contains(r.snippet, want) {
			t.Fatalf("snippet missing %q: %q", want, r.snippet)
		}
	}
}

func TestOpenAlexAbstractRebuildsWordOrder(t *testing.T) {
	got := openAlexAbstract(map[string][]int{"world": {1}, "hello": {0, 2}})
	if got != "hello world hello" {
		t.Fatalf("got %q", got)
	}
}

func TestParseArXivFixture(t *testing.T) {
	results, err := parseArXivResults(readFixture(t, "arxiv-feed.xml"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	for _, r := range results {
		if r.title == "" || !strings.HasPrefix(r.url, "https://") || strings.HasSuffix(r.url, "v1") || strings.HasSuffix(r.url, "v3") {
			t.Fatalf("result = %#v", r)
		}
		if !strings.Contains(r.snippet, "preprint") || !strings.Contains(r.snippet, "open-access PDF: https://arxiv.org/pdf/") {
			t.Fatalf("snippet = %q", r.snippet)
		}
	}
}

func TestParseArXivSkipsErrorEntry(t *testing.T) {
	feed := `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>http://arxiv.org/api/errors#incorrect_id</id><title>Error</title><summary>bad query</summary></entry></feed>`
	results, err := parseArXivResults([]byte(feed), 5)
	if err != nil || len(results) != 0 {
		t.Fatalf("results = %#v err = %v", results, err)
	}
}

func TestArXivRequestANDsTerms(t *testing.T) {
	var got string
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		got = r.URL.Query().Get("search_query")
		return okBody("application/atom+xml", []byte(`<feed xmlns="http://www.w3.org/2005/Atom"></feed>`)), nil
	})}
	f := newFetcher(client, defaultUserAgent, defaultMaxResponseBytes)
	src := arXivSource{limiter: &intervalLimiter{}}
	if _, err := src.search(context.Background(), f, "retrieval augmented generation", 3); err != nil {
		t.Fatal(err)
	}
	if got != "all:retrieval AND all:augmented AND all:generation" {
		t.Fatalf("search_query = %q", got)
	}
}

func TestIntervalLimiterSpacesCallsAndHonorsContext(t *testing.T) {
	l := &intervalLimiter{interval: 40 * time.Millisecond}
	start := time.Now()
	for range 3 {
		if err := l.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 75*time.Millisecond {
		t.Fatalf("3 calls took %v; expected >= 2 intervals", elapsed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.next = time.Now().Add(time.Hour)
	if err := l.wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseEuropePMCFixture(t *testing.T) {
	results, err := parseEuropePMCResults(readFixture(t, "europepmc-search.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	// The PMC and MEDLINE records of one paper collapse by title.
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	if !strings.Contains(results[0].snippet, "Frontiers in research metrics and analytics") {
		t.Fatalf("snippet = %q", results[0].snippet)
	}
	if !strings.Contains(results[1].snippet, "preprint") || !strings.HasPrefix(results[1].url, "https://doi.org/10.20944/") {
		t.Fatalf("preprint = %#v", results[1])
	}
}

func TestParseCrossrefFixture(t *testing.T) {
	results, err := parseCrossrefResults(readFixture(t, "crossref-works.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].url != "https://doi.org/10.21275/sr251104092705" || !strings.Contains(results[0].snippet, "International Journal of Science and Research") {
		t.Fatalf("first = %#v", results[0])
	}
	for _, r := range results {
		if strings.Contains(r.snippet, "<jats") {
			t.Fatalf("JATS markup leaked: %q", r.snippet)
		}
	}
}

func TestParseSemanticScholarFixture(t *testing.T) {
	results, err := parseSemanticScholarResults(readFixture(t, "semanticscholar-search.json"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].url != "https://doi.org/10.48550/arxiv.2005.11401" || !strings.Contains(results[0].snippet, "TL;DR: A general-purpose") {
		t.Fatalf("first = %#v", results[0])
	}
	if results[1].url != "https://www.semanticscholar.org/paper/def" {
		t.Fatalf("fallback url = %q", results[1].url)
	}
}

func TestSemanticScholarRequiresKey(t *testing.T) {
	t.Setenv("SEMANTIC_SCHOLAR_API_KEY", "")
	if containsString(sourceNames(sourcesFromConfig(Config{})), "semanticscholar") {
		t.Fatal("registered without key")
	}
	names := sourceNames(sourcesFromConfig(Config{SemanticScholar: SemanticScholarConfig{APIKey: "k"}}))
	if !containsString(names, "semanticscholar") {
		t.Fatalf("names = %v", names)
	}
}

func TestDOIURLNormalizes(t *testing.T) {
	for in, want := range map[string]string{
		"10.1145/ABC":                 "https://doi.org/10.1145/abc",
		"https://doi.org/10.1145/ABC": "https://doi.org/10.1145/abc",
		"doi:10.1/x":                  "https://doi.org/10.1/x",
		"not-a-doi":                   "",
		"":                            "",
	} {
		if got := doiURL(in); got != want {
			t.Fatalf("doiURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScholarlySourcesAreOptIn(t *testing.T) {
	s := New(Config{})
	defaults := s.DefaultSources()
	for _, name := range []string{"openalex", "arxiv", "europepmc", "crossref"} {
		if containsString(defaults, name) {
			t.Fatalf("%s in default fan-out: %v", name, defaults)
		}
		if !containsString(s.Sources(), name) {
			t.Fatalf("%s not registered: %v", name, s.Sources())
		}
	}
	s = New(Config{DefaultSources: []string{"OpenAlex", " crossref ", "nope"}})
	if got := s.DefaultSources(); len(got) != 2 || got[0] != "openalex" || got[1] != "crossref" {
		t.Fatalf("configured defaults = %v", got)
	}
}

func TestScholarlyHitsFuseByDOI(t *testing.T) {
	sources := []source{openAlexSource{}, crossrefSource{}}
	outcomes := []sourceOutcome{
		{name: "openalex", results: []sourceResult{{title: "Paper", url: doiURL("10.1/ABC"), rank: 1}}},
		{name: "crossref", results: []sourceResult{{title: "Paper", url: doiURL("https://doi.org/10.1/abc"), rank: 1}}},
	}
	got := fuseResults("", nil, outcomes, sources, 10)
	if len(got) != 1 || len(got[0].Sources) != 2 {
		t.Fatalf("fused = %+v", got)
	}
}

func TestSearchReturnsPartialResultsOnDeadline(t *testing.T) {
	s := newSearcherWithSources([]source{
		fakeSource{nameValue: "fast", searchFn: okSource("https://example.com/fast")},
		fakeSource{nameValue: "stuck", searchFn: func(ctx context.Context, _ *fetcher, _ string, _ int) ([]sourceResult, error) {
			<-ctx.Done()
			time.Sleep(50 * time.Millisecond) // ignores cancellation for a while
			return nil, ctx.Err()
		}},
	}, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	resp, err := s.Search(ctx, "q")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(resp.Results) != 1 || len(resp.Errors) != 1 || resp.Errors[0].Source != "stuck" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestSearchToolSourcesArgument(t *testing.T) {
	var hosts []string
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		hosts = append(hosts, r.URL.Host)
		return okBody("application/json", readFixture(t, "crossref-works.json")), nil
	})}
	def := findDef(t, New(Config{HTTPClient: client}).Tools(), ToolSearch)
	if !strings.Contains(def.Description, "Opt-in sources (") || !strings.Contains(def.Description, "openalex") {
		t.Fatalf("description = %q", def.Description)
	}
	if !strings.Contains(string(def.InputSchema), "crossref") {
		t.Fatalf("schema does not list sources: %s", def.InputSchema)
	}
	raw, err := def.Run(context.Background(), json.RawMessage(`{"query":"rag","sources":["crossref"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0] != "api.crossref.org" || !strings.Contains(string(raw), `"sources":["crossref"]`) {
		t.Fatalf("hosts = %v out = %s", hosts, raw)
	}
	hosts = nil
	raw, err = def.Run(context.Background(), json.RawMessage(`{"query":"rag","sources":["crossref","scopus"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || !strings.Contains(string(raw), `"warnings":["unknown source \"scopus\" ignored`) {
		t.Fatalf("hosts = %v out = %s", hosts, raw)
	}
}

func TestSearchAllUnknownSourcesFallsBackToDefaults(t *testing.T) {
	s := newSearcherWithSources([]source{fakeSource{nameValue: "fast", searchFn: okSource("https://example.com/fast")}}, Config{})
	resp, err := s.SearchWithOptions(context.Background(), "q", SearchOptions{Sources: []string{"scopus"}})
	if err != nil || len(resp.Results) != 1 {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "searched the default sources instead") {
		t.Fatalf("warnings = %v", resp.Warnings)
	}
}

func TestSanitizeSnippetSeparatesBlocks(t *testing.T) {
	if got := sanitizeSnippet("<h4>Background</h4>There is <b>evidence</b>."); got != "Background There is evidence." {
		t.Fatalf("got %q", got)
	}
}
